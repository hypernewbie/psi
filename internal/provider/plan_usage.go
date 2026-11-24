package provider

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"time"
)

const (
	zaiUsageEndpoint        = "https://api.z.ai/api/monitor/usage/quota/limit"
	openCodeGoUsageEndpoint = "https://opencode.ai/zen/go/v1/usage"
	// Z.AI reports the five-hour plan window as a plan limit of five units
	// and the weekly window as one unit of six. Unit 3 is hours; unit 6 is
	// days of the week.
	zaiFiveHourUnit   = 3
	zaiFiveHourLength = 5
	zaiWeeklyUnit     = 6
	zaiWeeklyLength   = 1
	zaiCreditLimit    = "CREDIT_LIMIT"
	zaiTokensLimit    = "TOKENS_LIMIT"
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

// FetchZaiUsage makes a one-shot plan lookup for the account panel. It uses
// the HTTP/1-only usage transport so a refused HTTP/2 stream cannot retry it.
func FetchZaiUsage(ctx context.Context, key string, notBefore time.Time) QuotaUsageResult {
	return fetchPlanUsage(ctx, "zai", key, notBefore, newUsageHTTPClient(nil))
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
	if !UsageKeyHeaderSafe(key) {
		return QuotaUsageResult{Outcome: QuotaUsageNoCredential}
	}
	started := time.Now()
	if !notBefore.IsZero() && started.Before(notBefore) {
		return QuotaUsageResult{Outcome: QuotaUsageUnsupported}
	}
	headers := http.Header{"Authorization": {"Bearer " + key}, "Accept": {"application/json"}, "User-Agent": {agent}}
	return fetchQuotaWith(ctx, endpoint, headers, client, func(body []byte) quotaSample {
		return parsePlanSample(providerID, body)
	})
}

// parsePlanUsage adapts the typed plan sample to the legacy percent triple so
// the quota owner keeps its footer rules. It reports ok only for a usable
// five-hour reading; a weekly-only reading is panel data, not a footer sample.
func parsePlanUsage(providerID string, body []byte) ([3]float64, bool) {
	sample := parsePlanSample(providerID, body)
	return sample.values, sample.ok
}

func parsePlanSample(providerID string, body []byte) quotaSample {
	switch providerID {
	case "zai":
		return parseZaiPlanWindows(body)
	case "opencode-go":
		values, ok := parseOpenCodeGoWindows(body)
		return quotaSample{values: values, ok: ok}
	}
	return quotaSample{}
}

// parseZaiPlanWindows selects each window on its own so an ambiguous or
// unusable weekly row cannot hide a valid five-hour reading, and a missing
// five-hour row cannot hide a valid weekly reading.
func parseZaiPlanWindows(body []byte) quotaSample {
	if !quotaBodyIsOneJSONValue(body) {
		return quotaSample{}
	}
	var payload struct {
		Success *bool `json:"success"`
		Data    *struct {
			Limits []json.RawMessage `json:"limits"`
		} `json:"data"`
	}
	defer func() {
		if payload.Data != nil {
			wipeQuotaRows(payload.Data.Limits)
		}
	}()
	if json.Unmarshal(body, &payload) != nil || payload.Data == nil || (payload.Success != nil && !*payload.Success) {
		return quotaSample{}
	}
	plan := &PlanWindows{}
	if raw, ok := zaiPlanLimit(payload.Data.Limits, zaiFiveHourUnit, zaiFiveHourLength, zaiCreditLimit, zaiTokensLimit); ok {
		plan.FiveHour = zaiPlanPercent(raw)
	}
	if raw, ok := zaiPlanLimit(payload.Data.Limits, zaiWeeklyUnit, zaiWeeklyLength, zaiCreditLimit); ok {
		plan.Weekly = zaiPlanPercent(raw)
	}
	sample := quotaSample{plan: plan}
	if plan.FiveHour != nil {
		sample.values[0] = *plan.FiveHour
		sample.ok = true
	}
	return sample
}

// zaiPlanLimit returns the one row that declares the window. Each row is
// decoded on its own so a wrong-typed field in an unrelated window cannot hide
// a match. Two matches are ambiguous even when one lacks a percentage.
func zaiPlanLimit(limits []json.RawMessage, unit, number int, types ...string) (json.RawMessage, bool) {
	var selected json.RawMessage
	matches := 0
	for _, raw := range limits {
		var window struct {
			Type   string `json:"type"`
			Unit   *int   `json:"unit"`
			Number *int   `json:"number"`
		}
		if json.Unmarshal(raw, &window) != nil {
			continue
		}
		if window.Unit == nil || *window.Unit != unit || window.Number == nil || *window.Number != number {
			continue
		}
		matched := false
		for _, want := range types {
			if window.Type == want {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		matches++
		selected = raw
	}
	return selected, matches == 1
}

// zaiPlanPercent decodes the selected row's used percentage. Nil means the
// value is missing or invalid; it is never zero.
func zaiPlanPercent(raw json.RawMessage) *float64 {
	var row struct {
		Percentage *float64 `json:"percentage"`
	}
	if json.Unmarshal(raw, &row) != nil || row.Percentage == nil || !validUsagePercent(*row.Percentage) {
		return nil
	}
	used := *row.Percentage
	return &used
}

func parseOpenCodeGoWindows(body []byte) ([3]float64, bool) {
	var values [3]float64
	if !quotaBodyIsOneJSONValue(body) {
		return values, false
	}
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

func validUsagePercent(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 100
}
