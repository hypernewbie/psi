package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretExecProviderHelper(t *testing.T) {
	if os.Getenv("PSIONIC_TEST_SECRET_EXEC") != "1" {
		return
	}
	var request secretExecRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil || request.ProtocolVersion != 1 || request.Provider != "local" || len(request.IDs) != 1 || request.IDs[0] != "synthetic/token" {
		os.Exit(2)
	}
	if os.Getenv("PSIONIC_TEST_INHERITED") != "" || os.Getenv("PSIONIC_TEST_ALLOWED") != "allowed" {
		os.Exit(3)
	}
	if os.Getenv("PSIONIC_TEST_SECRET_EXEC_MODE") == "plain" {
		fmt.Print("synthetic-token\n")
		os.Exit(0)
	}
	fmt.Printf(`{"protocolVersion":1,"values":{"%s":"synthetic-token"}}`, request.IDs[0])
	os.Exit(0)
}

func TestSecretExecProviderRequestAndResponse(t *testing.T) {
	t.Setenv("PSIONIC_TEST_INHERITED", "must-not-pass")
	t.Setenv("PSIONIC_TEST_ALLOWED", "allowed")
	command := secureExecTestBinary(t)
	provider := &secretProvider{
		Source:      "exec",
		Command:     command,
		Args:        []string{"-test.run=^TestSecretExecProviderHelper$"},
		TrustedDirs: []string{filepath.Dir(command)},
		PassEnv:     []string{"PSIONIC_TEST_ALLOWED"},
		Env:         map[string]string{"PSIONIC_TEST_SECRET_EXEC": "1"},
	}
	got, err := resolveSecretExec("local", provider, "synthetic/token", os.LookupEnv)
	if err != nil || got != "synthetic-token" {
		t.Fatalf("exec provider failed: %v", err)
	}
}

func TestSecretExecProviderPlainOutputMode(t *testing.T) {
	command := secureExecTestBinary(t)
	provider := &secretProvider{
		Source: "exec", Command: command, Args: []string{"-test.run=^TestSecretExecProviderHelper$"},
		TrustedDirs: []string{filepath.Dir(command)},
		Env:         map[string]string{"PSIONIC_TEST_SECRET_EXEC": "1", "PSIONIC_TEST_SECRET_EXEC_MODE": "plain", "PSIONIC_TEST_ALLOWED": "allowed"},
		JSONOnly:    boolPointer(false),
	}
	got, err := resolveSecretExec("local", provider, "synthetic/token", func(string) (string, bool) { return "", false })
	if err != nil || got != "synthetic-token" {
		t.Fatalf("plain exec output failed: %v", err)
	}
}

func TestSecretExecOptionsDropOwnedReferences(t *testing.T) {
	request := []byte("synthetic request")
	backing := bytes.Repeat([]byte{'x'}, 128)
	copy(backing, request)
	options := secretExecOptions{
		command: "/synthetic/provider", requestedID: "synthetic/token",
		args:    []string{"--secret", "synthetic-value"},
		env:     []string{"TOKEN=synthetic-value"},
		request: backing[:len(request)],
	}

	options.clearReferences()
	if options.command != "" || options.requestedID != "" || options.args[0] != "" || options.args[1] != "" || options.env[0] != "" {
		t.Fatal("exec option references were retained")
	}
	for i, value := range backing {
		if value != 0 {
			t.Fatalf("request allocation byte %d was not wiped", i)
		}
	}
}

func TestParseSecretExecOutputWipesFullCaptureAllocation(t *testing.T) {
	response := []byte(`{"protocolVersion":1,"values":{"id":"synthetic-token"}}`)
	backing := bytes.Repeat([]byte{'x'}, 256)
	copy(backing, response)
	capture := &secretExecCapture{stdout: *bytes.NewBuffer(backing[:len(response)])}

	got, err := parseSecretExecOutput(capture, secretExecOptions{requestedID: "id", jsonOnly: true})
	if err != nil || got != "synthetic-token" {
		t.Fatalf("parse output = %q, %v", got, err)
	}
	for i, value := range backing {
		if value != 0 {
			t.Fatalf("capture allocation byte %d was not wiped", i)
		}
	}
}

func TestSecretExecRejectsInvalidIDsAndProtocol(t *testing.T) {
	for _, id := range []string{"", "../token", "x/./token", "x/../token", "has space", "colon;command"} {
		if validExecSecretID(id) {
			t.Errorf("accepted invalid reference ID %q", id)
		}
	}
	capture := &secretExecCapture{stdout: *bytes.NewBufferString(`{"protocolVersion":2,"values":{"id":"value"}}`)}
	_, err := parseSecretExecOutput(capture, secretExecOptions{requestedID: "id", jsonOnly: true})
	if err == nil {
		t.Fatal("accepted unsupported response protocol")
	}
}

func secureExecTestBinary(t *testing.T) string {
	t.Helper()
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secret-provider.test")
	if err := os.WriteFile(path, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func boolPointer(value bool) *bool { return &value }
