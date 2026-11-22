package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	zaiUsageEndpoint        = "https://api.z.ai/api/monitor/usage/quota/limit"
	openCodeGoUsageEndpoint = "https://opencode.ai/zen/go/v1/usage"
	// The OpenCode Go usage service can reject Python's default User-Agent.
	// This is the client identity used by the reference quota monitor.
	openCodeGoUsageAgent = "opencode-copilot-chat/1.0 (+https://github.com/ltmoerdani/opencode-copilot-chat)"
)

// FetchPlanUsage borrows the active provider client's key. The HTTP header
// requires a string copy, which the Go HTTP stack may retain. No credentials
// are written or refreshed and no response details are retained.
func FetchPlanUsage(ctx context.Context, providerID, key string, notBefore time.Time) QuotaUsageResult {
	return fetchPlanUsage(ctx, providerID, key, notBefore, newQuotaHTTPClient(nil))
}

func fetchPlanUsage(ctx context.Context, providerID, key string, notBefore time.Time, client *http.Client) QuotaUsageResult {
	endpoint, agent := "", "psi-quota"
	switch providerID {
	case "zai":
		endpoint = zaiUsageEndpoint
	case "opencode-go":
		endpoint, agent = openCodeGoUsageEndpoint, openCodeGoUsageAgent
	default:
		return QuotaUsageResult{Outcome: QuotaUsageUnsupported}
	}
	if !safeQuotaHeaderValue(key, quotaBodyLimit) {
		return QuotaUsageResult{Outcome: QuotaUsageNoCredential}
	}
	started := time.Now()
	if !notBefore.IsZero() && started.Before(notBefore) {
		return QuotaUsageResult{Outcome: QuotaUsageUnsupported}
	}
	headers := http.Header{"Authorization": {"Bearer " + key}, "Accept": {"application/json"}, "User-Agent": {agent}}
	return fetchQuota(ctx, endpoint, headers, client, func(body []byte) ([3]float64, bool) {
		return parsePlanUsage(providerID, body)
	})
}

func parsePlanUsage(providerID string, body []byte) ([3]float64, bool) {
	var values [3]float64
	decoder := json.NewDecoder(bytes.NewReader(body))
	duplicate, err := duplicateJSONObjectKeys(decoder)
	if err != nil || duplicate {
		return values, false
	}
	if _, err = decoder.Token(); err != io.EOF {
		return values, false
	}
	switch providerID {
	case "zai":
		var payload struct {
			Success *bool `json:"success"`
			Data    *struct {
				Limits []struct {
					Type       string   `json:"type"`
					Percentage *float64 `json:"percentage"`
				} `json:"limits"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &payload) != nil || payload.Data == nil || (payload.Success != nil && !*payload.Success) {
			return values, false
		}
		found := false
		for _, limit := range payload.Data.Limits {
			if limit.Type != "TOKENS_LIMIT" {
				continue
			}
			if found || limit.Percentage == nil || !validUsagePercent(*limit.Percentage) {
				return values, false
			}
			values[0], found = *limit.Percentage, true
		}
		return values, found
	case "opencode-go":
		type window struct {
			Percent  *float64 `json:"percent"`
			Status   string   `json:"status"`
			ResetsAt string   `json:"resetsAt"`
		}
		var payload struct {
			Usage *struct {
				Rolling *window `json:"rolling"`
				Weekly  *window `json:"weekly"`
				Monthly *window `json:"monthly"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &payload) != nil || payload.Usage == nil {
			return values, false
		}
		for i, w := range []*window{payload.Usage.Rolling, payload.Usage.Weekly, payload.Usage.Monthly} {
			if w == nil || w.Percent == nil || !validUsagePercent(*w.Percent) || (w.Status != "ok" && w.Status != "rate-limited") {
				return values, false
			}
			if _, err := time.Parse(time.RFC3339Nano, w.ResetsAt); err != nil {
				return values, false
			}
			values[i] = *w.Percent
		}
		return values, true
	}
	return values, false
}

func validUsagePercent(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 100
}
