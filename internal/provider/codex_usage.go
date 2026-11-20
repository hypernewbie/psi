package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
)

const (
	codexQuotaEndpoint    = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaUserAgent   = "psi-chatgpt-usage"
	codexQuotaTimeout     = 10 * time.Second
	codexQuotaBodyLimit   = 64 * 1024
	codexQuotaHeaderLimit = 64 * 1024
	codexQuotaAccountMax  = 1024
)

// CodexUsageOutcome describes how a local-only quota read finished. The
// strings are stable test and diagnostic labels; they never contain response
// data or credentials.
type CodexUsageOutcome string

const (
	CodexUsageNoCredential           CodexUsageOutcome = "no usable credential"
	CodexUsageSupported              CodexUsageOutcome = "usable primary quota window"
	CodexUsageTemporarilyUnavailable CodexUsageOutcome = "temporarily unavailable"
	CodexUsageUnsupported            CodexUsageOutcome = "unsupported usage response"
)

// CodexUsageCredential is one read-only snapshot of Pi's Codex OAuth entry.
// It is never refreshed or written back by the quota path.
type CodexUsageCredential struct {
	Access    string
	AccountID string
	Expires   time.Time
	EntryHash [32]byte
}

// CodexUsageResult carries a primary-window reading and bounded timing data.
// A sample is usable only when Sample is true. No raw body or error text is
// retained.
type CodexUsageResult struct {
	Outcome     CodexUsageOutcome
	Status      int
	UsedPercent float64
	Sample      bool
	StartedAt   time.Time
	ExpiresAt   time.Time
	RetryAt     time.Time
	StopPolling bool
	EntryHash   [32]byte
}

// ReadCodexUsageCredential reads the selected openai-codex OAuth entry without
// taking the shared auth lease, refreshing, or writing. A symlink to a regular
// target is followed; missing, expired, unsafe, and non-OAuth entries fail
// closed.
func ReadCodexUsageCredential(authPath string, now time.Time) (CodexUsageCredential, bool) {
	auth := &CodexAuth{Path: authPath}
	target, err := resolveAuthTarget(auth.Path)
	if err != nil {
		return CodexUsageCredential{}, false
	}
	all, entry, err := auth.loadTarget(target)
	if err != nil {
		return CodexUsageCredential{}, false
	}
	defer ReleaseAuthEntries(all)

	if entry.Access == "" || entry.Expires <= now.UnixMilli() {
		return CodexUsageCredential{}, false
	}
	account, err := codexAccount(entry)
	if err != nil || !safeCodexQuotaValue(entry.Access, codexQuotaBodyLimit) {
		return CodexUsageCredential{}, false
	}
	if !safeCodexQuotaValue(account, codexQuotaAccountMax) {
		return CodexUsageCredential{}, false
	}
	return CodexUsageCredential{
		Access:    entry.Access,
		AccountID: account,
		Expires:   time.UnixMilli(entry.Expires),
		EntryHash: sha256.Sum256(all["openai-codex"]),
	}, true
}

// FetchCodexUsage performs at most one GET against the fixed ChatGPT usage
// endpoint. The caller's context is bounded here as well. It never retries,
// redirects, refreshes credentials, or consults environment/API-key fallbacks.
// A start at or before notBefore is skipped so a caller can keep starts apart.
// authPath is re-read after a usable response so a credential replacement
// during the request discards the sample.
func FetchCodexUsage(ctx context.Context, authPath string, credential CodexUsageCredential, notBefore time.Time) CodexUsageResult {
	return fetchCodexUsage(ctx, authPath, credential, notBefore, newCodexQuotaHTTPClient(nil))
}

func fetchCodexUsage(ctx context.Context, authPath string, credential CodexUsageCredential, notBefore time.Time, client *http.Client) CodexUsageResult {
	if !safeCodexQuotaValue(credential.Access, codexQuotaBodyLimit) || !safeCodexQuotaValue(credential.AccountID, codexQuotaAccountMax) {
		return CodexUsageResult{Outcome: CodexUsageNoCredential}
	}
	now := time.Now()
	if credential.Expires.IsZero() || !now.Before(credential.Expires) {
		return CodexUsageResult{Outcome: CodexUsageNoCredential}
	}
	if !notBefore.IsZero() && now.Before(notBefore) {
		return CodexUsageResult{Outcome: CodexUsageUnsupported}
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, codexQuotaTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, codexQuotaEndpoint, nil)
	if err != nil {
		return CodexUsageResult{Outcome: CodexUsageUnsupported, StartedAt: started}
	}
	request.Header = http.Header{
		"Authorization":      {"Bearer " + credential.Access},
		"Chatgpt-Account-Id": {credential.AccountID},
		"Accept":             {"application/json"},
		"User-Agent":         {codexQuotaUserAgent},
	}

	response, err := client.Do(request)
	if err != nil {
		return CodexUsageResult{Outcome: CodexUsageTemporarilyUnavailable, StartedAt: started}
	}
	received := time.Now()
	defer response.Body.Close()

	retryAt, stopPolling := codexQuotaRetry(response.StatusCode, response.Header.Values("Retry-After"), received)
	body, readErr := io.ReadAll(io.LimitReader(response.Body, codexQuotaBodyLimit+1))
	if readErr != nil {
		secretmem.Wipe(body[:cap(body)])
		return CodexUsageResult{Outcome: CodexUsageTemporarilyUnavailable, Status: response.StatusCode, StartedAt: started, RetryAt: retryAt, StopPolling: stopPolling}
	}
	if len(body) > codexQuotaBodyLimit {
		secretmem.Wipe(body[:cap(body)])
		return CodexUsageResult{Outcome: CodexUsageUnsupported, Status: response.StatusCode, StartedAt: started, RetryAt: retryAt, StopPolling: stopPolling}
	}
	used, parsed := parseCodexPrimaryUsedPercent(body)
	secretmem.Wipe(body[:cap(body)])

	if response.StatusCode != http.StatusOK {
		return CodexUsageResult{Outcome: codexQuotaStatusOutcome(response.StatusCode), Status: response.StatusCode, StartedAt: started, RetryAt: retryAt, StopPolling: stopPolling}
	}
	if !parsed {
		return CodexUsageResult{Outcome: CodexUsageUnsupported, Status: response.StatusCode, StartedAt: started}
	}

	// A replacement during the GET makes the sample unusable. This read is
	// local and does not refresh or acquire the auth lease. Transport tests
	// may omit the path; production always supplies it.
	expiresAt := credential.Expires
	if authPath != "" {
		latest, ok := ReadCodexUsageCredential(authPath, time.Now())
		if !ok || latest.EntryHash != credential.EntryHash {
			return CodexUsageResult{Outcome: CodexUsageNoCredential, Status: response.StatusCode, StartedAt: started}
		}
		expiresAt = latest.Expires
	}
	return CodexUsageResult{
		Outcome:     CodexUsageSupported,
		Status:      response.StatusCode,
		UsedPercent: used,
		Sample:      true,
		StartedAt:   started,
		ExpiresAt:   expiresAt,
		EntryHash:   credential.EntryHash,
	}
}

func newCodexQuotaHTTPClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = newCodexQuotaTransport()
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newCodexQuotaTransport() *http.Transport {
	base, _ := http.DefaultTransport.(*http.Transport)
	if base == nil {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = codexQuotaHeaderLimit
	return transport
}

func safeCodexQuotaValue(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] <= ' ' || value[i] >= 0x7f {
			return false
		}
	}
	return true
}

func codexQuotaRetry(status int, values []string, received time.Time) (time.Time, bool) {
	if status != http.StatusTooManyRequests && status < 500 {
		return time.Time{}, false
	}
	return parseCodexRetryAfter(values, received)
}

// parseCodexRetryAfter accepts one Retry-After value for the quota path only.
// Numeric delays are relative to the response-header receipt, not the body
// read, and a valid delay over 24 hours stops polling for the rest of the REPL.
func parseCodexRetryAfter(values []string, received time.Time) (time.Time, bool) {
	if len(values) != 1 {
		return time.Time{}, false
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return time.Time{}, false
	}
	if isAllDigits(value) {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return time.Time{}, true
		}
		const day = uint64(24 * 60 * 60)
		if seconds > day {
			return time.Time{}, true
		}
		return received.Add(time.Duration(seconds) * time.Second), false
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return time.Time{}, false
	}
	wait := when.Sub(received)
	if wait < 0 {
		return time.Time{}, false
	}
	if wait > 24*time.Hour {
		return time.Time{}, true
	}
	return when, false
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func codexQuotaStatusOutcome(status int) CodexUsageOutcome {
	switch {
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return CodexUsageTemporarilyUnavailable
	default:
		return CodexUsageUnsupported
	}
}

func parseCodexPrimaryUsedPercent(data []byte) (float64, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	duplicate, err := duplicateJSONObjectKeys(decoder)
	if err != nil || duplicate {
		return 0, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return 0, false
	}
	var payload struct {
		RateLimit *struct {
			PrimaryWindow *struct {
				UsedPercent json.RawMessage `json:"used_percent"`
			} `json:"primary_window"`
		} `json:"rate_limit"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.RateLimit == nil || payload.RateLimit.PrimaryWindow == nil {
		return 0, false
	}
	raw := bytes.TrimSpace(payload.RateLimit.PrimaryWindow.UsedPercent)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	var used float64
	if json.Unmarshal(raw, &used) != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
		return 0, false
	}
	return used, true
}

// duplicateJSONObjectKeys walks one JSON value and rejects a repeated key in
// any object. encoding/json otherwise accepts the last duplicate silently,
// which would make a generated usage document ambiguous.
func duplicateJSONObjectKeys(decoder *json.Decoder) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return false, nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return false, fmt.Errorf("object key is not a string")
			}
			if seen[key] {
				return true, nil
			}
			seen[key] = true
			duplicate, err := duplicateJSONObjectKeys(decoder)
			if err != nil || duplicate {
				return duplicate, err
			}
		}
		_, err := decoder.Token()
		return false, err
	case '[':
		for decoder.More() {
			duplicate, err := duplicateJSONObjectKeys(decoder)
			if err != nil || duplicate {
				return duplicate, err
			}
		}
		_, err := decoder.Token()
		return false, err
	default:
		return false, fmt.Errorf("unexpected JSON delimiter")
	}
}
