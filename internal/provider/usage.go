package provider

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hypernewbie/psi/internal/secretmem"
)

const (
	deepSeekBalanceEndpoint = "https://api.deepseek.com/user/balance"
	usageMoneyWidth         = 24 // formatted amount only; the currency is separate
)

// DeepSeekBalance holds validated account data; nil availability and empty
// optional amounts mean unknown, not false or zero.
type DeepSeekBalance struct {
	Available *bool
	Balances  []DeepSeekCurrencyBalance
}

type DeepSeekCurrencyBalance struct {
	Currency string
	Total    string
	Granted  string
	ToppedUp string
}

// UsageKeyHeaderSafe reports whether the API-key usage fetchers can put key
// in a bearer header. Account discovery uses the same checks without a GET.
func UsageKeyHeaderSafe(key string) bool {
	return safeQuotaHeaderValue(key, quotaHeaderLimit)
}

// FetchDeepSeekBalance borrows key for one read; the required Authorization
// string and HTTP internals may keep their own copies. It never refreshes auth.
func FetchDeepSeekBalance(ctx context.Context, key string, notBefore time.Time) QuotaUsageResult {
	return fetchDeepSeekBalance(ctx, key, notBefore, newUsageHTTPClient(nil))
}

func newUsageHTTPClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		policy := newQuotaTransport()
		policy.TLSClientConfig = nil // verified TLS, with standard roots
		policy.DialTLSContext = nil
		policy.DialTLS = nil
		policy.TLSNextProto = nil
		policy.ForceAttemptHTTP2 = false
		policy.Protocols = new(http.Protocols)
		policy.Protocols.SetHTTP1(true) // no HTTP/2 retries
		transport = policy
	}
	return newQuotaHTTPClient(transport)
}

func fetchDeepSeekBalance(ctx context.Context, key string, notBefore time.Time, client *http.Client) QuotaUsageResult {
	if !UsageKeyHeaderSafe(key) {
		return QuotaUsageResult{Outcome: QuotaUsageNoCredential}
	}
	started := time.Now()
	if ctx.Err() != nil || started.Before(notBefore) {
		return QuotaUsageResult{Outcome: QuotaUsageTemporarilyUnavailable}
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deepSeekBalanceEndpoint, nil)
	if err != nil {
		return QuotaUsageResult{Outcome: QuotaUsageUnsupported}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "psi-quota")
	result := QuotaUsageResult{StartedAt: started, Outcome: QuotaUsageTemporarilyUnavailable}
	resp, err := client.Do(req)
	if err != nil {
		return result
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	result.RetryAt, result.StopPolling = quotaRetry(result.Status, resp.Header.Values("Retry-After"), time.Now())
	if result.Status != http.StatusOK {
		result.Outcome = quotaStatusOutcome(result.Status)
		return result
	}
	result.Outcome = QuotaUsageUnsupported
	contentTypes := resp.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return result
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		return result
	}
	body, err := readQuotaBody(resp.Body)
	defer secretmem.Wipe(body[:cap(body)])
	if err != nil || ctx.Err() != nil {
		result.Outcome = QuotaUsageTemporarilyUnavailable
		return result
	}
	if len(body) > quotaBodyLimit {
		return result
	}
	balance := parseDeepSeekBalance(body)
	if balance != nil && ctx.Err() == nil {
		result.Outcome, result.Sample, result.Balance = QuotaUsageSupported, true, balance
	}
	return result
}

// parseDeepSeekBalance accepts exact keys and validates each optional field on
// its own. Decoder strings may outlive the call; owned raw messages are wiped.
func parseDeepSeekBalance(body []byte) *DeepSeekBalance {
	if !utf8.Valid(body) || !quotaBodyIsOneJSONValue(body) {
		return nil
	}
	var fields map[string]json.RawMessage
	defer func() { releaseRawEntries(fields) }()
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil
	}
	balance := &DeepSeekBalance{}
	var available *bool
	if json.Unmarshal(fields["is_available"], &available) == nil {
		balance.Available = available
	}
	var rows []json.RawMessage
	defer func() { wipeQuotaRows(rows) }()
	if json.Unmarshal(fields["balance_infos"], &rows) == nil {
		for _, row := range rows {
			if parsed, ok := parseDeepSeekCurrency(row); ok {
				balance.Balances = append(balance.Balances, parsed)
			}
		}
	}
	if len(balance.Balances) == 0 && (balance.Available == nil || *balance.Available) {
		return nil
	}
	return balance
}

func parseDeepSeekCurrency(raw json.RawMessage) (DeepSeekCurrencyBalance, bool) {
	var fields map[string]json.RawMessage
	defer func() { releaseRawEntries(fields) }()
	if json.Unmarshal(raw, &fields) != nil {
		return DeepSeekCurrencyBalance{}, false
	}
	var currency string
	if json.Unmarshal(fields["currency"], &currency) != nil || len(currency) != 3 {
		return DeepSeekCurrencyBalance{}, false
	}
	for i := range len(currency) {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return DeepSeekCurrencyBalance{}, false
		}
	}
	money := func(name string) string {
		var value string
		if json.Unmarshal(fields[name], &value) != nil {
			return ""
		}
		return formatUsageMoney(value)
	}
	total := money("total_balance")
	if total == "" {
		return DeepSeekCurrencyBalance{}, false
	}
	return DeepSeekCurrencyBalance{Currency: currency, Total: total, Granted: money("granted_balance"), ToppedUp: money("topped_up_balance")}, true
}

// formatUsageMoney rounds in decimal (half away from zero) without losing
// cents to binary floating-point conversion. Empty means invalid or too wide.
func formatUsageMoney(value string) string {
	magnitude, negative := strings.CutPrefix(value, "-")
	whole, fraction, hasFraction := strings.Cut(magnitude, ".")
	if !isAllDigits(whole) || (hasFraction && !isAllDigits(fraction)) {
		return ""
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	const decimalWidth = 3 // decimal point and two cents digits
	if len(whole)+decimalWidth > usageMoneyWidth {
		return ""
	}
	cents := []byte(whole + "00")
	copy(cents[len(whole):], fraction)
	if len(fraction) > 2 && fraction[2] >= '5' {
		// Carry through the cents and whole digits, adding a leading digit
		// only when every digit was nine.
		i := len(cents) - 1
		for ; i >= 0 && cents[i] == '9'; i-- {
			cents[i] = '0'
		}
		if i < 0 {
			cents = append([]byte{'1'}, cents...)
		} else {
			cents[i]++
		}
	}
	point := len(cents) - 2
	formatted := string(cents[:point]) + "." + string(cents[point:])
	if negative && formatted != "0.00" {
		formatted = "-" + formatted
	}
	if len(formatted) > usageMoneyWidth {
		return ""
	}
	return formatted
}
