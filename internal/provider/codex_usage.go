package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	codexQuotaEndpoint   = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaUserAgent  = "psi-chatgpt-usage"
	codexQuotaAccountMax = 1024
)

// CodexUsageCredential is one read-only snapshot of Pi's Codex OAuth entry.
// It is never refreshed or written back by the quota path.
type CodexUsageCredential struct {
	Access    string
	AccountID string
	Expires   time.Time
	EntryHash [32]byte
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
	if err != nil || !safeQuotaHeaderValue(entry.Access, quotaBodyLimit) {
		return CodexUsageCredential{}, false
	}
	if !safeQuotaHeaderValue(account, codexQuotaAccountMax) {
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
func FetchCodexUsage(ctx context.Context, authPath string, credential CodexUsageCredential, notBefore time.Time) QuotaUsageResult {
	return fetchCodexUsage(ctx, authPath, credential, notBefore, newQuotaHTTPClient(nil))
}

func fetchCodexUsage(ctx context.Context, authPath string, credential CodexUsageCredential, notBefore time.Time, client *http.Client) QuotaUsageResult {
	if !safeQuotaHeaderValue(credential.Access, quotaBodyLimit) || !safeQuotaHeaderValue(credential.AccountID, codexQuotaAccountMax) {
		return QuotaUsageResult{Outcome: QuotaUsageNoCredential}
	}
	now := time.Now()
	if credential.Expires.IsZero() || !now.Before(credential.Expires) {
		return QuotaUsageResult{Outcome: QuotaUsageNoCredential}
	}
	if !notBefore.IsZero() && now.Before(notBefore) {
		return QuotaUsageResult{Outcome: QuotaUsageUnsupported}
	}

	headers := http.Header{
		"Authorization":      {"Bearer " + credential.Access},
		"Chatgpt-Account-Id": {credential.AccountID},
		"Accept":             {"application/json"},
		"User-Agent":         {codexQuotaUserAgent},
	}
	result := fetchQuota(ctx, codexQuotaEndpoint, headers, client, func(body []byte) ([3]float64, bool) {
		used, ok := parseCodexPrimaryUsedPercent(body)
		return [3]float64{used}, ok
	})
	if !result.Sample {
		return result
	}

	// A replacement during the GET makes the sample unusable. This read is
	// local and does not refresh or acquire the auth lease. Transport tests
	// may omit the path; production always supplies it.
	expiresAt := credential.Expires
	if authPath != "" {
		latest, ok := ReadCodexUsageCredential(authPath, time.Now())
		if !ok || latest.EntryHash != credential.EntryHash {
			return QuotaUsageResult{Outcome: QuotaUsageNoCredential, Status: result.Status, StartedAt: result.StartedAt}
		}
		expiresAt = latest.Expires
	}
	result.ExpiresAt = expiresAt
	result.EntryHash = credential.EntryHash
	return result
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
