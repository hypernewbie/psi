package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/secretmem"
)

const (
	quotaTimeout     = 10 * time.Second
	quotaBodyLimit   = 64 * 1024
	quotaHeaderLimit = 64 * 1024
)

// QuotaUsageOutcome describes how a best-effort quota read finished. The
// strings are stable test and diagnostic labels; they never contain response
// data or credentials.
type QuotaUsageOutcome string

const (
	QuotaUsageNoCredential           QuotaUsageOutcome = "no usable credential"
	QuotaUsageSupported              QuotaUsageOutcome = "usable quota sample"
	QuotaUsageTemporarilyUnavailable QuotaUsageOutcome = "temporarily unavailable"
	QuotaUsageUnsupported            QuotaUsageOutcome = "unsupported usage response"
)

// QuotaWindows holds server-reported used percentages for OpenCode Go.
type QuotaWindows struct {
	Rolling float64
	Weekly  float64
	Monthly float64
}

// QuotaUsageResult carries used percentages and bounded timing data. A sample
// is usable only when Sample is true. No raw body or error text is retained.
type QuotaUsageResult struct {
	Outcome     QuotaUsageOutcome
	Status      int
	UsedPercent float64
	Windows     QuotaWindows
	Sample      bool
	StartedAt   time.Time
	ExpiresAt   time.Time
	RetryAt     time.Time
	StopPolling bool
	EntryHash   [32]byte
}

// fetchQuota owns the bounded response buffer; the parser borrows it only for
// the duration of the call. HTTP and JSON may retain their own copies.
func fetchQuota(ctx context.Context, endpoint string, headers http.Header, client *http.Client, parse func([]byte) ([3]float64, bool)) QuotaUsageResult {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return QuotaUsageResult{Outcome: QuotaUsageUnsupported, StartedAt: started}
	}
	req.Header = headers
	resp, err := client.Do(req)
	if err != nil {
		return QuotaUsageResult{Outcome: QuotaUsageTemporarilyUnavailable, StartedAt: started}
	}
	defer resp.Body.Close()
	retryAt, stop := quotaRetry(resp.StatusCode, resp.Header.Values("Retry-After"), time.Now())
	result := QuotaUsageResult{Status: resp.StatusCode, StartedAt: started, RetryAt: retryAt, StopPolling: stop}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, quotaBodyLimit+1))
	defer secretmem.Wipe(body[:cap(body)])
	if readErr != nil {
		result.Outcome = QuotaUsageTemporarilyUnavailable
		return result
	}
	if len(body) > quotaBodyLimit {
		result.Outcome = QuotaUsageUnsupported
		return result
	}
	if resp.StatusCode != http.StatusOK {
		result.Outcome = quotaStatusOutcome(resp.StatusCode)
		return result
	}
	values, ok := parse(body)
	if !ok {
		result.Outcome = QuotaUsageUnsupported
		return result
	}
	result.Outcome, result.Sample, result.UsedPercent = QuotaUsageSupported, true, values[0]
	result.Windows = QuotaWindows{Rolling: values[0], Weekly: values[1], Monthly: values[2]}
	return result
}

func newQuotaHTTPClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		transport = newQuotaTransport()
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newQuotaTransport() *http.Transport {
	base, _ := http.DefaultTransport.(*http.Transport)
	if base == nil {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = quotaHeaderLimit
	return transport
}

func safeQuotaHeaderValue(value string, limit int) bool {
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

func quotaRetry(status int, values []string, received time.Time) (time.Time, bool) {
	if status != http.StatusTooManyRequests && status < 500 {
		return time.Time{}, false
	}
	return parseQuotaRetryAfter(values, received)
}

// parseQuotaRetryAfter accepts one Retry-After value for the quota path only.
// Numeric delays are relative to the response-header receipt, not the body
// read, and a valid delay over 24 hours stops polling that provider for the REPL.
func parseQuotaRetryAfter(values []string, received time.Time) (time.Time, bool) {
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

func quotaStatusOutcome(status int) QuotaUsageOutcome {
	switch {
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return QuotaUsageTemporarilyUnavailable
	default:
		return QuotaUsageUnsupported
	}
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
