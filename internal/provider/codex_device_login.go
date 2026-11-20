package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
)

// OpenAI Codex device sign-in endpoints and bounds. The client ID, token
// endpoint, credential type, and account parser are shared with browser sign-in
// and refresh.
const (
	codexDeviceUserCodeURL = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL    = "https://auth.openai.com/api/accounts/deviceauth/token"
	codexDeviceVerifyURL   = "https://auth.openai.com/codex/device"
	codexDeviceRedirectURI = "https://auth.openai.com/deviceauth/callback"
	// codexDevicePollTimeout bounds the whole poll loop, not one request.
	codexDevicePollTimeout = 15 * time.Minute
	// codexDeviceHTTPTimeout bounds each start or poll request.
	codexDeviceHTTPTimeout = 15 * time.Second
	// One device response carries a short user code or authorization code.
	codexDeviceBodyLimit = 64 * 1024
	// Poll at most once per second, even when the server sends interval 0.
	codexDeviceMinInterval = time.Second
	// A slow_down response adds five seconds to the current interval.
	codexDeviceSlowDown = 5 * time.Second
)

// Static device sign-in failures. None echoes a user code, authorization code,
// verifier, response body, or transport error.
var (
	errCodexDeviceUnavailable = errors.New("OpenAI Codex device sign-in is not available for this account; choose browser login")
	errCodexDeviceExpired     = errors.New("OpenAI Codex device sign-in expired; start /login openai-codex again")
	errCodexDeviceRateLimited = errors.New("OpenAI Codex device sign-in is rate limited; wait and start /login openai-codex again")
	errCodexDeviceNetwork     = errors.New("OpenAI Codex device sign-in request failed; start /login openai-codex again")
	errCodexDeviceInvalid     = errors.New("invalid OpenAI Codex device sign-in response; start /login openai-codex again")
)

// CodexDeviceLogin runs one headless Codex sign-in: it requests a user code,
// polls until authorization, exchanges the returned code once, and saves the
// credential with the same baseline and commit rules as browser sign-in.
//
// Wait is the polling timer seam. Nil uses a real cancellable timer; tests
// advance a fake clock instead of sleeping.
type CodexDeviceLogin struct {
	Path   string
	HTTP   *http.Client
	Chmod  func(string, os.FileMode) error
	Rename func(string, string) error
	Wait   func(context.Context, time.Duration) error
	// now selects the clock for expiry and the poll deadline. Nil means time.Now.
	now func() time.Time
}

func (l *CodexDeviceLogin) nowOrNow() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *CodexDeviceLogin) store() *CodexLogin {
	return &CodexLogin{Path: l.Path, HTTP: l.HTTP, Chmod: l.Chmod, Rename: l.Rename, now: l.now}
}

func (l *CodexDeviceLogin) wait(ctx context.Context, delay time.Duration) error {
	if l.Wait != nil {
		return l.Wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run executes one complete attempt. ready receives the user code and fixed
// verification URL before polling starts. A nil error means the credential was
// saved. A context error means the caller cancelled before commit.
func (l *CodexDeviceLogin) Run(ctx context.Context, ready func(userCode, verificationURL string)) error {
	if l.Path == "" {
		return errors.New("cannot save the OpenAI Codex login without an auth path")
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return errors.New("cannot prepare the OpenAI Codex login")
	}
	store := l.store()
	baseline, err := store.captureBaseline(ctx)
	if err != nil {
		return err
	}
	defer secretmem.Wipe(baseline.raw[:cap(baseline.raw)])
	start, err := l.start(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready != nil {
		ready(start.userCode, codexDeviceVerifyURL)
	}
	code, verifier, err := l.poll(ctx, start)
	if err != nil {
		return err
	}
	// The poll success arrived before the deadline; the exchange itself is
	// bounded by its own short context, not by the poll deadline.
	if err := ctx.Err(); err != nil {
		return err
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, codexLoginExchangeTimeout)
	defer cancel()
	token, err := store.exchangeCode(exchangeCtx, code, verifier, codexDeviceRedirectURI)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return store.commit(ctx, baseline, token)
}

type codexDeviceStart struct {
	userCode string
	deviceID string
	interval time.Duration
}

func (l *CodexDeviceLogin) start(ctx context.Context) (codexDeviceStart, error) {
	requestCtx, cancel := context.WithTimeout(ctx, codexDeviceHTTPTimeout)
	defer cancel()
	status, data, err := doCodexDeviceJSON(requestCtx, l.HTTP, codexDeviceUserCodeURL, map[string]string{"client_id": codexClientID})
	defer secretmem.Wipe(data[:cap(data)])
	if err != nil {
		if ctx.Err() != nil {
			return codexDeviceStart{}, ctx.Err()
		}
		return codexDeviceStart{}, errCodexDeviceNetwork
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return codexDeviceStart{}, errCodexDeviceUnavailable
	case http.StatusTooManyRequests:
		return codexDeviceStart{}, errCodexDeviceRateLimited
	default:
		return codexDeviceStart{}, errCodexDeviceNetwork
	}
	var response struct {
		UserCode     string `json:"user_code"`
		DeviceAuthID string `json:"device_auth_id"`
		Interval     any    `json:"interval"`
	}
	if json.Unmarshal(data, &response) != nil {
		return codexDeviceStart{}, errCodexDeviceInvalid
	}
	userCode := strings.TrimSpace(response.UserCode)
	deviceID := strings.TrimSpace(response.DeviceAuthID)
	interval, ok := codexDeviceInterval(response.Interval)
	if userCode == "" || deviceID == "" || !ok {
		return codexDeviceStart{}, errCodexDeviceInvalid
	}
	return codexDeviceStart{userCode: userCode, deviceID: deviceID, interval: interval}, nil
}

// codexDeviceInterval accepts Pi's finite nonnegative number or numeric string.
// Clamp finite intervals to the poll window before converting to a duration.
func codexDeviceInterval(value any) (time.Duration, bool) {
	var seconds float64
	switch v := value.(type) {
	case float64:
		seconds = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		seconds = parsed
	default:
		return 0, false
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0, false
	}
	return time.Duration(min(seconds, codexDevicePollTimeout.Seconds()) * float64(time.Second)), true
}

// poll runs the immediate first poll, then waits between responses until
// authorization, cancellation, expiry, or a static failure. It never retries an
// ambiguous start or poll response.
func (l *CodexDeviceLogin) poll(ctx context.Context, start codexDeviceStart) (string, string, error) {
	deadline := l.nowOrNow().Add(codexDevicePollTimeout)
	interval := max(start.interval, codexDeviceMinInterval)
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if !l.nowOrNow().Before(deadline) {
			return "", "", errCodexDeviceExpired
		}
		requestCtx, cancel := context.WithDeadline(ctx, minTime(l.nowOrNow().Add(codexDeviceHTTPTimeout), deadline))
		status, data, err := doCodexDeviceJSON(requestCtx, l.HTTP, codexDeviceTokenURL, map[string]string{
			"device_auth_id": start.deviceID,
			"user_code":      start.userCode,
		})
		cancel()
		if err := ctx.Err(); err != nil {
			secretmem.Wipe(data[:cap(data)])
			return "", "", err
		}
		if !l.nowOrNow().Before(deadline) {
			secretmem.Wipe(data[:cap(data)])
			return "", "", errCodexDeviceExpired
		}
		if err != nil {
			return "", "", errCodexDeviceNetwork
		}
		if status == http.StatusOK {
			var response struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeVerifier      string `json:"code_verifier"`
			}
			parseErr := json.Unmarshal(data, &response)
			secretmem.Wipe(data[:cap(data)])
			if parseErr != nil {
				return "", "", errCodexDeviceInvalid
			}
			code := strings.TrimSpace(response.AuthorizationCode)
			verifier := strings.TrimSpace(response.CodeVerifier)
			if code == "" || verifier == "" {
				return "", "", errCodexDeviceInvalid
			}
			return code, verifier, nil
		}
		pending, slow := false, false
		switch {
		case status == http.StatusForbidden || status == http.StatusNotFound:
			pending = true
		case status >= http.StatusBadRequest && status < 600:
			pending, slow = parseCodexDeviceError(data)
		}
		secretmem.Wipe(data[:cap(data)])
		switch {
		case pending:
		case slow:
			interval += codexDeviceSlowDown
		case status == http.StatusTooManyRequests:
			return "", "", errCodexDeviceRateLimited
		default:
			return "", "", errCodexDeviceInvalid
		}
		remaining := deadline.Sub(l.nowOrNow())
		if remaining <= 0 {
			return "", "", errCodexDeviceExpired
		}
		// Add up to 20% jitter without polling sooner than the server allows.
		delay := interval + time.Duration(1+rand.Int64N(int64(interval/5)))
		if err := l.wait(ctx, min(delay, remaining)); err != nil {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			return "", "", errCodexDeviceNetwork
		}
	}
}

// parseCodexDeviceError recognizes pending and slow-down codes in either the
// string error field or a nested error.code object.
func parseCodexDeviceError(data []byte) (pending, slow bool) {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &payload) != nil || len(payload.Error) == 0 {
		return false, false
	}
	var code string
	if json.Unmarshal(payload.Error, &code) != nil {
		var nested struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(payload.Error, &nested) != nil {
			return false, false
		}
		code = nested.Code
	}
	switch strings.TrimSpace(code) {
	case "deviceauth_authorization_pending":
		return true, false
	case "slow_down":
		return false, true
	default:
		return false, false
	}
}

// doCodexDeviceJSON sends one bounded JSON POST on a client copy that refuses
// redirects. A start failure, or a forbidden or not-found poll, needs only
// its status. Other bodies are bounded; errors never expose response details.
func doCodexDeviceJSON(ctx context.Context, client *http.Client, endpoint string, payload any) (int, []byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, errCodexDeviceInvalid
	}
	// A RoundTripper can still own the reader after returning an error.
	// Give it an immutable copy so the JSON buffer can be wiped now.
	reader := strings.NewReader(string(body))
	secretmem.Wipe(body[:cap(body)])
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return 0, nil, errCodexDeviceInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	single := *client
	single.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := single.Do(req)
	if err != nil {
		return 0, nil, errCodexDeviceNetwork
	}
	defer resp.Body.Close()
	status := resp.StatusCode
	startFailure := endpoint == codexDeviceUserCodeURL && status != http.StatusOK
	if startFailure || status == http.StatusForbidden || status == http.StatusNotFound {
		return status, nil, nil
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, codexDeviceBodyLimit+1))
	if readErr != nil || len(data) > codexDeviceBodyLimit {
		secretmem.Wipe(data[:cap(data)])
		return 0, nil, errCodexDeviceNetwork
	}
	return status, data, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
