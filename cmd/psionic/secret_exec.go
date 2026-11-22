package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
)

const (
	defaultSecretExecTimeout     = 5 * time.Second
	maxSecretExecTimeout         = 120 * time.Second
	defaultSecretExecOutputBytes = 1 << 20
	maxSecretExecOutputBytes     = 20 << 20
	maxSecretExecRequestBytes    = 256 << 10
	maxSecretExecArgs            = 128
	maxSecretExecTrustedDirs     = 64
	maxSecretExecPassedEnv       = 128
	maxSecretExecArgumentBytes   = 1024
	secretExecStopGracePeriod    = 300 * time.Millisecond
)

type secretExecRequest struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Provider        string   `json:"provider"`
	IDs             []string `json:"ids"`
}

type secretExecOptions struct {
	command     string
	args        []string
	env         []string
	request     []byte
	timeout     time.Duration
	noOutput    time.Duration
	maxOutput   int
	jsonOnly    bool
	requestedID string
}

type secretExecCapture struct {
	mu       sync.Mutex
	stdout   bytes.Buffer
	combined int
	limit    int
	exceeded chan struct{}
	onOutput chan struct{}
	once     sync.Once
}

func (c *secretExecCapture) write(target *bytes.Buffer, data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.combined += len(data)
	if c.combined > c.limit {
		c.once.Do(func() { close(c.exceeded) })
		return len(data), nil
	}
	if target != nil {
		_, _ = target.Write(data)
	}
	select {
	case c.onOutput <- struct{}{}:
	default:
	}
	return len(data), nil
}

func (c *secretExecCapture) stdoutWriter() io.Writer {
	return secretExecWriter{capture: c, stdout: true}
}
func (c *secretExecCapture) stderrWriter() io.Writer { return secretExecWriter{capture: c} }

type secretExecWriter struct {
	capture *secretExecCapture
	stdout  bool
}

func (w secretExecWriter) Write(data []byte) (int, error) {
	if w.stdout {
		return w.capture.write(&w.capture.stdout, data)
	}
	return w.capture.write(nil, data)
}

func (s *fileSettings) execToken(ref *secretRef, lookupEnv func(string) (string, bool)) (string, error) {
	alias, provider, err := s.secretProviderForRef("exec", ref.provider)
	if err != nil || provider == nil {
		return "", fmt.Errorf("exec secret provider is unavailable")
	}
	opts, err := prepareSecretExec(alias, provider, ref.exec, lookupEnv)
	if err != nil {
		return "", err
	}
	return runSecretExec(opts)
}

func resolveSecretExec(alias string, provider *secretProvider, id string, lookupEnv func(string) (string, bool)) (string, error) {
	opts, err := prepareSecretExec(alias, provider, id, lookupEnv)
	if err != nil {
		return "", err
	}
	return runSecretExec(opts)
}

func validateSecretExecConfig(alias string, provider *secretProvider, id string, lookupEnv func(string) (string, bool)) error {
	_, err := prepareSecretExec(alias, provider, id, lookupEnv)
	return err
}

func prepareSecretExec(alias string, provider *secretProvider, id string, lookupEnv func(string) (string, bool)) (secretExecOptions, error) {
	var opts secretExecOptions
	if !validExecSecretID(id) {
		return opts, fmt.Errorf("invalid exec secret reference")
	}
	command, err := substituteConfigEnvironment(provider.Command, lookupEnv)
	if err != nil || !filepath.IsAbs(command) {
		return opts, fmt.Errorf("invalid exec provider command")
	}
	command, err = filepath.Abs(command)
	if err != nil || validateSecretExecCommand(command, provider.TrustedDirs) != nil {
		return opts, fmt.Errorf("exec provider command is unavailable")
	}
	if len(provider.Args) > maxSecretExecArgs || len(provider.TrustedDirs) > maxSecretExecTrustedDirs || len(provider.PassEnv) > maxSecretExecPassedEnv {
		return opts, fmt.Errorf("invalid exec provider arguments")
	}
	args := make([]string, len(provider.Args))
	for i, arg := range provider.Args {
		if len(arg) > maxSecretExecArgumentBytes {
			return opts, fmt.Errorf("invalid exec provider arguments")
		}
		args[i], err = substituteConfigEnvironment(arg, lookupEnv)
		if err != nil {
			return opts, fmt.Errorf("invalid exec provider arguments")
		}
	}
	timeout := defaultSecretExecTimeout
	if provider.TimeoutMs != nil {
		if *provider.TimeoutMs < 1 || time.Duration(*provider.TimeoutMs)*time.Millisecond > maxSecretExecTimeout {
			return opts, fmt.Errorf("invalid exec provider timeout")
		}
		timeout = time.Duration(*provider.TimeoutMs) * time.Millisecond
	}
	noOutputTimeout := timeout
	if provider.NoOutputTimeoutMs != nil {
		if *provider.NoOutputTimeoutMs < 1 || time.Duration(*provider.NoOutputTimeoutMs)*time.Millisecond > maxSecretExecTimeout {
			return opts, fmt.Errorf("invalid exec provider idle timeout")
		}
		noOutputTimeout = time.Duration(*provider.NoOutputTimeoutMs) * time.Millisecond
	}
	maxOutput := defaultSecretExecOutputBytes
	if provider.MaxOutputBytes != nil {
		maxOutput = *provider.MaxOutputBytes
	}
	if maxOutput < 1 || maxOutput > maxSecretExecOutputBytes {
		return opts, fmt.Errorf("invalid exec provider output limit")
	}
	request, err := json.Marshal(secretExecRequest{ProtocolVersion: 1, Provider: alias, IDs: []string{id}})
	if err != nil || len(request) > maxSecretExecRequestBytes {
		return opts, fmt.Errorf("exec provider request is too large")
	}
	env := make([]string, 0, len(provider.PassEnv)+len(provider.Env))
	seen := make(map[string]struct{}, len(provider.PassEnv)+len(provider.Env))
	for _, name := range provider.PassEnv {
		if !secretEnvName.MatchString(name) {
			return opts, fmt.Errorf("invalid exec provider environment allowlist")
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if value, ok := lookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	for name, value := range provider.Env {
		if !secretEnvName.MatchString(name) {
			return opts, fmt.Errorf("invalid exec provider environment")
		}
		resolved, err := substituteConfigEnvironment(value, lookupEnv)
		if err != nil {
			return opts, fmt.Errorf("invalid exec provider environment")
		}
		if _, exists := seen[name]; exists {
			for i, entry := range env {
				if strings.HasPrefix(entry, name+"=") {
					env[i] = name + "=" + resolved
					break
				}
			}
		} else {
			seen[name] = struct{}{}
			env = append(env, name+"="+resolved)
		}
	}
	return secretExecOptions{
		command: command, args: args, env: env, request: request, timeout: timeout,
		noOutput: noOutputTimeout, maxOutput: maxOutput,
		jsonOnly: provider.JSONOnly == nil || *provider.JSONOnly, requestedID: id,
	}, nil
}

func validExecSecretID(id string) bool {
	if len(id) == 0 || len(id) > 256 || !isAlphaNumeric(id[0]) {
		return false
	}
	for _, char := range id {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:/#-", char)) {
			return false
		}
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func isAlphaNumeric(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func runSecretExec(opts secretExecOptions) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	process := startSecretExec(ctx, opts.command, opts.args, opts.env, opts.request)
	capture := &secretExecCapture{limit: opts.maxOutput, exceeded: make(chan struct{}), onOutput: make(chan struct{}, 1)}
	process.Stdout = capture.stdoutWriter()
	process.Stderr = capture.stderrWriter()
	wait := make(chan error, 1)
	go func() { wait <- process.Run() }()
	idle := time.NewTimer(opts.noOutput)
	defer idle.Stop()
	resetIdle := func() {
		if !idle.Stop() {
			select {
			case <-idle.C:
			default:
			}
		}
		idle.Reset(opts.noOutput)
	}
	for {
		select {
		case waitErr := <-wait:
			if waitErr != nil {
				return "", fmt.Errorf("exec provider exited unsuccessfully")
			}
			return parseSecretExecOutput(capture, opts)
		case <-capture.onOutput:
			resetIdle()
		case <-capture.exceeded:
			stopSecretExec(process)
			<-wait
			return "", fmt.Errorf("exec provider output exceeded its limit")
		case <-idle.C:
			stopSecretExec(process)
			<-wait
			return "", fmt.Errorf("exec provider produced no output before timeout")
		case <-ctx.Done():
			stopSecretExec(process)
			<-wait
			return "", fmt.Errorf("exec provider timed out")
		}
	}
}

func parseSecretExecOutput(capture *secretExecCapture, opts secretExecOptions) (string, error) {
	capture.mu.Lock()
	output := append([]byte(nil), capture.stdout.Bytes()...)
	capture.mu.Unlock()
	defer secretmem.Wipe(output[:cap(output)])
	defer func() {
		secretmem.Wipe(capture.stdout.Bytes())
		capture.stdout.Reset()
	}()
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(output, []byte("\xef\xbb\xbf")))
	if len(trimmed) == 0 {
		return "", fmt.Errorf("exec provider returned empty output")
	}
	var parsed any
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		if !opts.jsonOnly {
			return string(trimmed), nil
		}
		return "", fmt.Errorf("exec provider returned invalid JSON")
	}
	if !opts.jsonOnly {
		if scalar, ok := parsed.(string); ok {
			return scalar, nil
		}
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return "", fmt.Errorf("exec provider returned an invalid response")
	}
	protocolVersion, ok := object["protocolVersion"].(float64)
	if !ok || protocolVersion != 1 {
		return "", fmt.Errorf("exec provider returned an invalid response")
	}
	values, ok := object["values"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("exec provider returned an invalid response")
	}
	if errors, ok := object["errors"].(map[string]any); ok {
		if _, failed := errors[opts.requestedID]; failed {
			return "", fmt.Errorf("exec provider reported a secret resolution error")
		}
	}
	value, ok := values[opts.requestedID].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("exec provider did not return a string value")
	}
	return value, nil
}
