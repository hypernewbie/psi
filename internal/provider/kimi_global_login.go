package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/secretmem"
)

// Global Kimi Code device sign-in constants. The client ID, host pair, and
// device grant match the first-party Kimi Code flow. Endpoints are fixed:
// there is deliberately no environment or configuration override.
const (
	kimiGlobalClientID  = "17e5f671-d194-4dfb-9706-5516cb48c098"
	kimiGlobalStartURL  = "https://auth.kimi.ai/api/oauth/device_authorization"
	kimiGlobalTokenURL  = "https://auth.kimi.ai/api/oauth/token"
	kimiGlobalVerifyURL = "https://www.kimi.ai/code/authorize_device"
	kimiGlobalGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// kimiGlobalHTTPTimeout bounds each start or poll request.
	kimiGlobalHTTPTimeout = 30 * time.Second
	// kimiGlobalDefaultLifetime is the fallback device lifetime when the
	// server omits expires_in. The server value is capped at the maximum.
	kimiGlobalDefaultLifetime = 15 * time.Minute
	kimiGlobalMaxLifetime     = 30 * time.Minute
	// One device response carries a short code or a token pair.
	kimiGlobalBodyLimit = 64 * 1024
	// Poll at most once per second, and wait five seconds before the first
	// poll when the server omits an interval.
	kimiGlobalMinInterval     = time.Second
	kimiGlobalDefaultInterval = 5 * time.Second
	// A slow_down response raises the current interval by five seconds.
	kimiGlobalSlowDown = 5 * time.Second
	// Three transient retries in a row are allowed; the fourth fails.
	kimiGlobalTransientRetries = 3
	// A user code is a short token. The limit keeps panel input bounded.
	kimiGlobalUserCodeLimit = 64
)

// Static Kimi Global sign-in failures. None echoes a user code, device code,
// token, response body, OAuth description, or transport error.
var (
	errKimiGlobalDenied    = errors.New("Kimi Code (Global) sign-in was denied; start /login kimi-coding-global again")
	errKimiGlobalExpired   = errors.New("Kimi Code (Global) sign-in expired; start /login kimi-coding-global again")
	errKimiGlobalRejected  = errors.New("Kimi Code (Global) sign-in was rejected; start /login kimi-coding-global again")
	errKimiGlobalTransient = errors.New("Kimi Code (Global) sign-in is temporarily unavailable; start /login kimi-coding-global again")
	errKimiGlobalNetwork   = errors.New("Kimi Code (Global) sign-in request failed; start /login kimi-coding-global again")
	errKimiGlobalInvalid   = errors.New("invalid Kimi Code (Global) sign-in response; start /login kimi-coding-global again")
	errKimiGlobalConflict  = errors.New("Kimi Code (Global) credentials changed during sign-in; refusing overwrite")
	errKimiGlobalLockLost  = errors.New("auth.json lock changed during sign-in; refusing overwrite")
)

// Body failures stay static. A read error is a transient transport failure; an
// oversized body is a terminal invalid response.
var (
	errKimiGlobalBodyRead     = errors.New("kimi global response body read")
	errKimiGlobalBodyTooLarge = errors.New("kimi global response body too large")
)

// PsiUserAgent returns psi's HTTP identity for provider requests: psi/<version>
// for a stamped build and psi for an unstamped internal caller. A version with
// whitespace or non-printable bytes is treated as unstamped so it cannot break
// a header.
func PsiUserAgent(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "psi"
	}
	for i := 0; i < len(version); i++ {
		if version[i] <= 0x20 || version[i] >= 0x7f {
			return "psi"
		}
	}
	return "psi/" + version
}

// KimiGlobalLogin runs one Global Kimi Code device sign-in: it requests a
// device authorization, waits and polls until the user approves it, and saves
// the OAuth credential with the same baseline and commit rules as Codex
// sign-in. The refresh token is stored but this trial never refreshes; an
// expired access token requires another explicit login.
//
// Wait is the polling timer seam. Nil uses a real cancellable timer; tests
// advance a fake clock instead of sleeping.
type KimiGlobalLogin struct {
	Path   string
	HTTP   *http.Client
	Chmod  func(string, os.FileMode) error
	Rename func(string, string) error
	Wait   func(context.Context, time.Duration) error
	// UserAgent is the build version for auth requests. Empty sends "psi".
	UserAgent string
	// now selects the clock for expiry and the poll deadline. Nil means time.Now.
	now func() time.Time
}

func (l *KimiGlobalLogin) nowOrNow() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *KimiGlobalLogin) client() *http.Client {
	if l.HTTP != nil {
		return l.HTTP
	}
	return http.DefaultClient
}

func (l *KimiGlobalLogin) wait(ctx context.Context, delay time.Duration) error {
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
func (l *KimiGlobalLogin) Run(ctx context.Context, ready func(userCode, verificationURL string)) error {
	if l.Path == "" {
		return errors.New("cannot save the Kimi Code (Global) login without an auth path")
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return errors.New("cannot prepare the Kimi Code (Global) login")
	}
	baseline, err := l.captureBaseline(ctx)
	if err != nil {
		return err
	}
	defer secretmem.Wipe(baseline.raw[:cap(baseline.raw)])
	started := l.nowOrNow()
	start, err := l.start(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready != nil {
		ready(start.userCode, kimiGlobalVerifyURL)
	}
	token, err := l.poll(ctx, start, started)
	if err != nil {
		return err
	}
	// The token response arrived before the deadline. Saving is bounded by
	// the same cancellation contract as Codex sign-in.
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.commit(ctx, baseline, token)
}

type kimiGlobalStart struct {
	deviceCode string
	userCode   string
	interval   time.Duration
	lifetime   time.Duration
}

// start requests one device authorization. It never retries; a fresh /login
// starts a fresh grant.
func (l *KimiGlobalLogin) start(ctx context.Context) (kimiGlobalStart, error) {
	requestCtx, cancel := context.WithTimeout(ctx, kimiGlobalHTTPTimeout)
	defer cancel()
	status, data, _, err := doKimiGlobalForm(requestCtx, l.client(), kimiGlobalStartURL, url.Values{"client_id": {kimiGlobalClientID}}, PsiUserAgent(l.UserAgent))
	defer secretmem.Wipe(data[:cap(data)])
	if err != nil {
		if ctx.Err() != nil {
			return kimiGlobalStart{}, ctx.Err()
		}
		if errors.Is(err, errKimiGlobalBodyRead) || errors.Is(err, errKimiGlobalBodyTooLarge) {
			return kimiGlobalStart{}, errKimiGlobalInvalid
		}
		return kimiGlobalStart{}, errKimiGlobalNetwork
	}
	if status != http.StatusOK {
		return kimiGlobalStart{}, errKimiGlobalNetwork
	}
	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        any    `json:"interval"`
		ExpiresIn       any    `json:"expires_in"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&response) != nil {
		return kimiGlobalStart{}, errKimiGlobalInvalid
	}
	if !kimiGlobalPrintable(response.DeviceCode, 0) ||
		!kimiGlobalPrintable(response.UserCode, kimiGlobalUserCodeLimit) ||
		response.VerificationURI != kimiGlobalVerifyURL {
		return kimiGlobalStart{}, errKimiGlobalInvalid
	}
	interval, ok := kimiGlobalDuration(response.Interval, kimiGlobalDefaultInterval, false)
	if !ok {
		return kimiGlobalStart{}, errKimiGlobalInvalid
	}
	if interval == 0 {
		interval = kimiGlobalDefaultInterval
	}
	interval = max(interval, kimiGlobalMinInterval)
	lifetime, ok := kimiGlobalDuration(response.ExpiresIn, kimiGlobalDefaultLifetime, true)
	if !ok || lifetime <= 0 {
		return kimiGlobalStart{}, errKimiGlobalInvalid
	}
	return kimiGlobalStart{deviceCode: response.DeviceCode, userCode: response.UserCode, interval: interval, lifetime: lifetime}, nil
}

// kimiGlobalDuration accepts an integral JSON number of seconds. The default
// applies only when the field is absent or null. Values above the poll
// maximum are capped before conversion so a huge server value cannot overflow.
// Negative and non-integral values are invalid.
func kimiGlobalDuration(value any, fallback time.Duration, positive bool) (time.Duration, bool) {
	if value == nil {
		return fallback, true
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	seconds, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || math.Trunc(seconds) != seconds {
		return 0, false
	}
	if positive && seconds == 0 {
		return 0, false
	}
	seconds = min(seconds, kimiGlobalMaxLifetime.Seconds())
	return time.Duration(seconds * float64(time.Second)), true
}

// poll waits before each request and handles the RFC 8628 pending, slow_down,
// transient, and terminal responses. The deadline starts before the device
// authorization request, so time in the panel and network waits are included.
func (l *KimiGlobalLogin) poll(ctx context.Context, start kimiGlobalStart, started time.Time) (kimiGlobalToken, error) {
	deadline := started.Add(start.lifetime)
	interval := start.interval
	transient, retryAfter := 0, time.Duration(0)
	for {
		if err := ctx.Err(); err != nil {
			return kimiGlobalToken{}, err
		}
		if !l.nowOrNow().Before(deadline) {
			return kimiGlobalToken{}, errKimiGlobalExpired
		}
		remaining := deadline.Sub(l.nowOrNow())
		delay := interval
		if transient > 0 {
			delay = max(delay, kimiGlobalBackoff(transient), retryAfter)
		}
		if delay >= remaining {
			// Waiting would use up the device lifetime; do not poll again.
			return kimiGlobalToken{}, errKimiGlobalExpired
		}
		if err := l.wait(ctx, delay); err != nil {
			if ctx.Err() != nil {
				return kimiGlobalToken{}, ctx.Err()
			}
			transient++
			if transient > kimiGlobalTransientRetries {
				return kimiGlobalToken{}, errKimiGlobalTransient
			}
			continue
		}
		if !l.nowOrNow().Before(deadline) {
			return kimiGlobalToken{}, errKimiGlobalExpired
		}
		requestCtx, cancel := context.WithDeadline(ctx, minTime(l.nowOrNow().Add(kimiGlobalHTTPTimeout), deadline))
		status, data, header, err := doKimiGlobalForm(requestCtx, l.client(), kimiGlobalTokenURL, url.Values{
			"client_id":   {kimiGlobalClientID},
			"device_code": {start.deviceCode},
			"grant_type":  {kimiGlobalGrantType},
		}, PsiUserAgent(l.UserAgent))
		cancel()
		if err := ctx.Err(); err != nil {
			secretmem.Wipe(data[:cap(data)])
			return kimiGlobalToken{}, err
		}
		if !l.nowOrNow().Before(deadline) {
			secretmem.Wipe(data[:cap(data)])
			return kimiGlobalToken{}, errKimiGlobalExpired
		}
		if err != nil {
			secretmem.Wipe(data[:cap(data)])
			if errors.Is(err, errKimiGlobalBodyTooLarge) {
				return kimiGlobalToken{}, errKimiGlobalInvalid
			}
			// A body read error is a transport failure and uses the same
			// bounded retry budget as a timeout or connection error.
			transient++
			if transient > kimiGlobalTransientRetries {
				return kimiGlobalToken{}, errKimiGlobalTransient
			}
			retryAfter = kimiGlobalRetryAfter(header.Get("Retry-After"))
			continue
		}
		if status == http.StatusTooManyRequests || status >= 500 && status < 600 {
			secretmem.Wipe(data[:cap(data)])
			transient++
			if transient > kimiGlobalTransientRetries {
				return kimiGlobalToken{}, errKimiGlobalTransient
			}
			retryAfter = kimiGlobalRetryAfter(header.Get("Retry-After"))
			continue
		}
		if status == http.StatusOK {
			token, tokenErr := decodeKimiGlobalToken(data, l.nowOrNow())
			secretmem.Wipe(data[:cap(data)])
			if tokenErr != nil {
				return kimiGlobalToken{}, tokenErr
			}
			return token, nil
		}
		code, serverInterval, parsed := decodeKimiGlobalError(data)
		secretmem.Wipe(data[:cap(data)])
		switch {
		case parsed && code == "authorization_pending":
			transient, retryAfter = 0, 0
		case parsed && code == "slow_down":
			transient, retryAfter = 0, 0
			interval = max(interval+kimiGlobalSlowDown, serverInterval)
		case parsed && code == "access_denied":
			return kimiGlobalToken{}, errKimiGlobalDenied
		case parsed && code == "expired_token":
			return kimiGlobalToken{}, errKimiGlobalExpired
		case status >= 300 && status < 400:
			return kimiGlobalToken{}, errKimiGlobalRejected
		default:
			// Unknown grant rejections stop immediately. A lost success
			// response can leave the grant consumed, so the message asks
			// for a new explicit login.
			return kimiGlobalToken{}, errKimiGlobalRejected
		}
	}
}

func kimiGlobalBackoff(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt-1)) * time.Second
}

// kimiGlobalRetryAfter reuses parseRetryAfter for its supported numeric and
// HTTP-date range. A numeric delay beyond that range is longer than the device
// lifetime, not an absent delay.
func kimiGlobalRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		switch {
		case math.IsNaN(seconds), seconds < 0:
			// A malformed or negative delay is not a usable Retry-After.
			return 0
		case math.IsInf(seconds, 0), seconds > 86400:
			// This delay is longer than the device lifetime, not absent.
			return kimiGlobalMaxLifetime
		}
	} else if errors.Is(err, strconv.ErrRange) {
		if strings.HasPrefix(value, "-") {
			return 0
		}
		return kimiGlobalMaxLifetime
	}
	return parseRetryAfter(value)
}

// decodeKimiGlobalError reads the RFC 8628 error code and an optional
// slow_down interval. Descriptions are never read or returned.
func decodeKimiGlobalError(data []byte) (code string, interval time.Duration, ok bool) {
	var payload struct {
		Error    string `json:"error"`
		Interval any    `json:"interval"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return "", 0, false
	}
	code = strings.TrimSpace(payload.Error)
	if code == "" {
		return "", 0, false
	}
	if seconds, valid := kimiGlobalDuration(payload.Interval, 0, false); valid && seconds > 0 {
		interval = seconds
	}
	return code, interval, true
}

type kimiGlobalToken struct {
	access  string
	refresh string
	expires int64
}

// decodeKimiGlobalToken validates the stored credential fields: header-safe
// access and refresh tokens and a positive integral lifetime that converts to
// a future Unix millisecond expiry. The returned token fields are forced string
// copies at the JSON boundary; Go strings cannot be wiped by the caller.
func decodeKimiGlobalToken(data []byte, now time.Time) (kimiGlobalToken, error) {
	var payload struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		ExpiresIn any    `json:"expires_in"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return kimiGlobalToken{}, errKimiGlobalInvalid
	}
	if !kimiGlobalPrintable(payload.Access, 0) || !kimiGlobalPrintable(payload.Refresh, 0) {
		return kimiGlobalToken{}, errKimiGlobalInvalid
	}
	number, ok := payload.ExpiresIn.(json.Number)
	if !ok {
		return kimiGlobalToken{}, errKimiGlobalInvalid
	}
	seconds, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || math.Trunc(seconds) != seconds {
		return kimiGlobalToken{}, errKimiGlobalInvalid
	}
	// Compare before conversion so an overflowing lifetime is rejected
	// instead of wrapping into a past expiry.
	const int64Limit = float64(1 << 63)
	expiry := float64(now.UnixMilli()) + seconds*1000
	if math.IsNaN(expiry) || math.IsInf(expiry, 0) || expiry >= int64Limit || int64(expiry) <= now.UnixMilli() {
		return kimiGlobalToken{}, errKimiGlobalInvalid
	}
	return kimiGlobalToken{access: payload.Access, refresh: payload.Refresh, expires: int64(expiry)}, nil
}

// kimiGlobalPrintable accepts nonempty printable ASCII without spaces,
// controls, DEL, or non-ASCII bytes. limit zero means no length limit.
func kimiGlobalPrintable(value string, limit int) bool {
	if value == "" || limit > 0 && len(value) > limit {
		return false
	}
	for i := 0; i < len(value); i++ {
		// 0x20 is space; 0x7f is DEL; bytes above it are non-ASCII.
		if value[i] <= 0x20 || value[i] >= 0x7f {
			return false
		}
	}
	return true
}

// doKimiGlobalForm sends one bounded form POST on a client copy that refuses
// redirects. It returns the status and a caller-owned response body. Errors are
// static sentinels: no transport message or response body is returned as an
// error. The encoded form is an unavoidable net/url string copy; Go strings are
// not erasable by the caller.
func doKimiGlobalForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, userAgent string) (int, []byte, http.Header, error) {
	if client == nil {
		client = http.DefaultClient
	}
	reader := strings.NewReader(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return 0, nil, nil, errKimiGlobalInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	single := *client
	single.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := single.Do(req)
	if err != nil {
		return 0, nil, nil, errKimiGlobalNetwork
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, kimiGlobalBodyLimit+1))
	if readErr != nil {
		secretmem.Wipe(data[:cap(data)])
		return 0, nil, resp.Header, errKimiGlobalBodyRead
	}
	if len(data) > kimiGlobalBodyLimit {
		secretmem.Wipe(data[:cap(data)])
		return 0, nil, resp.Header, errKimiGlobalBodyTooLarge
	}
	return resp.StatusCode, data, resp.Header, nil
}

// kimiGlobalBaseline pins the stored Global entry and the resolved destination
// across the device wait. raw is a compacted, owned copy so a reindented but
// otherwise identical entry is not a conflict.
type kimiGlobalBaseline struct {
	target     authTarget
	parentInfo os.FileInfo
	file       bool
	present    bool
	raw        json.RawMessage
}

// captureBaseline reads the stored document under a short shared lease and
// releases the lease before the panel or polling. A missing file is a valid
// first-login baseline; unsafe or nonregular targets fail closed.
func (l *KimiGlobalLogin) captureBaseline(ctx context.Context) (kimiGlobalBaseline, error) {
	var baseline kimiGlobalBaseline
	unlock, _, err := acquireAuthLock(ctx, l.Path+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		if ctx.Err() != nil {
			return baseline, ctx.Err()
		}
		return baseline, errors.New("auth.json is busy; retry /login kimi-coding-global")
	}
	defer unlock()
	target, err := resolveAuthTarget(l.Path)
	if err != nil {
		return baseline, err
	}
	parentInfo, err := statAuthIdentity(filepath.Dir(target.path))
	if err != nil {
		return baseline, errors.New("cannot resolve auth.json directory")
	}
	entries, _, exists, err := readAuthDocument(target)
	if err != nil {
		return baseline, err
	}
	defer releaseRawEntries(entries)
	baseline = kimiGlobalBaseline{target: target, parentInfo: parentInfo, file: exists}
	raw, present := entries[providerregistry.KimiGlobalProviderID]
	baseline.present = present
	if present {
		compact, err := compactAuthEntry(raw)
		if err != nil {
			return baseline, errors.New("auth.json credential entry is malformed")
		}
		baseline.raw = compact
	}
	return baseline, nil
}

// commit saves the OAuth credential under a fresh shared lease. The
// destination path, parent identity, and Global entry must still match the
// baseline. An unrelated provider change, or a new or renamed file at the same
// destination with an unchanged Global entry, is merged.
func (l *KimiGlobalLogin) commit(ctx context.Context, baseline kimiGlobalBaseline, token kimiGlobalToken) error {
	unlock, leaseValid, err := acquireAuthLock(ctx, l.Path+".lock", 0, 0, apiKeyAuthLockWait)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("auth.json is busy; retry /login kimi-coding-global")
	}
	defer unlock()
	currentTarget, err := resolveAuthTarget(l.Path)
	if err != nil {
		return errKimiGlobalConflict
	}
	currentParent, err := statAuthIdentity(filepath.Dir(currentTarget.path))
	if err != nil || baseline.parentInfo == nil || !os.SameFile(baseline.parentInfo, currentParent) {
		return errKimiGlobalConflict
	}
	if currentTarget.path != baseline.target.path || (baseline.file && !currentTarget.exists) {
		return errKimiGlobalConflict
	}
	entries, mode, exists, err := readAuthDocument(currentTarget)
	if err != nil {
		return errKimiGlobalConflict
	}
	defer releaseRawEntries(entries)
	if exists != currentTarget.exists {
		return errKimiGlobalConflict
	}
	raw, present := entries[providerregistry.KimiGlobalProviderID]
	if present != baseline.present || (present && !sameAuthEntryJSON(raw, baseline.raw)) {
		return errKimiGlobalConflict
	}
	// An explicit login replaces the identity with exactly the four Pi
	// OAuth fields. The device and user codes are never stored.
	newEntry, err := json.Marshal(map[string]any{
		"type":    "oauth",
		"access":  token.access,
		"refresh": token.refresh,
		"expires": token.expires,
	})
	if err != nil {
		return errors.New("cannot save the Kimi Code (Global) login")
	}
	next := make(map[string]json.RawMessage, len(entries)+1)
	for key, value := range entries {
		next[key] = value
	}
	next[providerregistry.KimiGlobalProviderID] = newEntry
	defer releaseRawEntries(next)
	validate := func() error {
		if !leaseValid() {
			return errKimiGlobalLockLost
		}
		latest, _, latestExists, err := readAuthDocument(currentTarget)
		if err != nil {
			return errKimiGlobalConflict
		}
		defer releaseRawEntries(latest)
		if latestExists != currentTarget.exists || !equalAuthDocuments(entries, latest) {
			return errKimiGlobalConflict
		}
		return nil
	}
	// Cancellation before admission writes nothing. Once admitted, the save
	// finishes under the lease and reports its actual outcome.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAuthDocumentWithHooks(currentTarget, next, mode, validate, authFileHooks{Chmod: l.Chmod, Rename: l.Rename}); err != nil {
		switch {
		case errors.Is(err, errKimiGlobalConflict), errors.Is(err, errKimiGlobalLockLost):
			return err
		case errors.Is(err, errAuthDocumentTooLarge):
			return errors.New("cannot save the Kimi Code (Global) login: auth.json would exceed its size limit")
		default:
			return errors.New("cannot save the Kimi Code (Global) login")
		}
	}
	return nil
}
