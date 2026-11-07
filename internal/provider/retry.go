package provider

import (
	"errors"
	"regexp"
)

// Retry classification for one-off calls such as compaction summaries, ported
// from Pi's isRetryableAssistantError (packages/ai/src/utils/retry.ts). This
// classifies failures only; callers own the retry budget, backoff, reporting,
// and cancellation handling.

var zhipuUsageLimitPattern = regexp.MustCompile(`(?i)"code"\s*:\s*"?1308(?:[^0-9]|$)`)

// nonRetryableLimitPatterns match quota, budget, and billing exhaustion. These
// are subscription/account limits that reset after hours, not transient
// throttles, so retrying immediately cannot help.
var nonRetryableLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)GoUsageLimitError`),
	regexp.MustCompile(`(?i)FreeUsageLimitError`),
	regexp.MustCompile(`(?i)Monthly usage limit reached`),
	regexp.MustCompile(`(?i)available balance`),
	regexp.MustCompile(`(?i)insufficient_quota`),
	regexp.MustCompile(`(?i)out of budget`),
	regexp.MustCompile(`(?i)quota exceeded`),
	regexp.MustCompile(`(?i)billing`),
	regexp.MustCompile(`(?i)subscription_sharing_usage_limit_exceeded`),
	regexp.MustCompile(`(?i)subscription_sharing_workplace_individual_usage_limit_exceeded`),
	// Zhipu/Z.ai error code 1308: the plan's usage limit is reached, sent as
	// HTTP 429. It resets after hours, not seconds.
	zhipuUsageLimitPattern,
}

// retryableTransientPatterns match provider load, server-side failures, and
// network or stream transport drops. psi's own stream-interruption wording is
// included alongside Pi's provider and gateway text; Node-only spellings stay
// because gateways relay upstream messages verbatim.
var retryableTransientPatterns = []*regexp.Regexp{
	// Provider load, HTTP status, and server-side transient failures.
	regexp.MustCompile(`(?i)overloaded`),
	regexp.MustCompile(`(?i)server_busy`),
	regexp.MustCompile(`(?i)servers are currently busy`),
	regexp.MustCompile(`(?i)currently experiencing high demand`),
	regexp.MustCompile(`(?i)model is at capacity`),
	regexp.MustCompile(`(?i)rate.?limit`),
	regexp.MustCompile(`(?i)too many requests`),
	// Status codes must not be part of a longer word or number, e.g. a request
	// id or byte count.
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:429|500|502|503|504|520|524)(?:[^a-z0-9]|$)`),
	regexp.MustCompile(`(?i)service.?(?:is.?)?(?:temporarily.?)?unavailable`),
	regexp.MustCompile(`(?i)server.error`),
	regexp.MustCompile(`(?i)internal.error`),

	// Wrapper and gateway text for transient upstream failures.
	regexp.MustCompile(`(?i)provider.?returned.?(?:an.?)?error`),
	regexp.MustCompile(`(?i)exceeded request buffer limit while retrying upstream`),
	regexp.MustCompile(`(?i)unable to forward upstream`),
	regexp.MustCompile(`(?i)upstream.request.failed`),
	regexp.MustCompile(`(?i)upstream_stream_read_error`),
	regexp.MustCompile(`(?i)stream was interrupted`),
	regexp.MustCompile(`(?i)http/?2 stream failed`),

	// Network, proxy, and transport failures, including Go's net errors and
	// relayed Node fetch text.
	regexp.MustCompile(`(?i)network.error`),
	regexp.MustCompile(`(?i)connection.error`),
	regexp.MustCompile(`(?i)connection.refused`),
	regexp.MustCompile(`(?i)connection.reset`),
	regexp.MustCompile(`(?i)connection.lost`),
	regexp.MustCompile(`(?i)connection closed`),
	regexp.MustCompile(`(?i)other side closed`),
	regexp.MustCompile(`(?i)unexpected EOF`),
	regexp.MustCompile(`(?i)broken pipe`),
	regexp.MustCompile(`(?i)no such host`),
	regexp.MustCompile(`(?i)fetch failed`),
	regexp.MustCompile(`(?i)request to \S+ failed, reason:`),
	regexp.MustCompile(`(?i)getaddrinfo`),
	regexp.MustCompile(`(?i)ENOTFOUND`),
	regexp.MustCompile(`(?i)EAI_AGAIN`),
	regexp.MustCompile(`(?i)upstream.connect`),
	regexp.MustCompile(`(?i)reset before headers`),
	regexp.MustCompile(`(?i)socket hang up`),
	regexp.MustCompile(`(?i)socket connection was closed`),
	regexp.MustCompile(`(?i)timed.?out`),
	regexp.MustCompile(`(?i)timeout`),
	regexp.MustCompile(`(?i)terminated`),
	regexp.MustCompile(`(?i)websocket.closed`),
	regexp.MustCompile(`(?i)websocket.error`),

	// Premature stream endings from adapters and gateways.
	regexp.MustCompile(`(?i)stream interrupted`),
	regexp.MustCompile(`(?i)ended without`),
	regexp.MustCompile(`(?i)stream ended before message_stop`),
	regexp.MustCompile(`(?i)stream ended before a terminal response event`),
	regexp.MustCompile(`(?i)http2 request did not get a response`),
	regexp.MustCompile(`(?i)stream disconnected before completion`),
	regexp.MustCompile(`(?i)pending stream has been canceled`),

	// Provider-requested retry delays and explicit retry guidance.
	regexp.MustCompile(`(?i)retry delay`),
	regexp.MustCompile(`(?i)you can retry your request`),
	regexp.MustCompile(`(?i)try your request again`),
	regexp.MustCompile(`(?i)please retry your request`),

	// gRPC based providers (e.g. NVIDIA NIM).
	regexp.MustCompile(`(?i)ResourceExhausted`),
	regexp.MustCompile(`(?i)subscription_sharing_usage_unavailable`),
	regexp.MustCompile(`(?i)subscription_sharing_user_unavailable`),
}

// IsRetryableError reports whether a failed provider call looks like a
// transient provider or transport failure worth retrying. Typed rate-limit
// and transport errors are retryable, and mid-stream interruptions always
// are. Quota, budget, and billing exhaustion is never retryable; auth,
// validation, and context-limit failures fail fast. The caller still handles
// cancellation and context overflow separately.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var se *StreamInterrupt
	if errors.As(err, &se) {
		return matchesRetryPatterns(se.Error())
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return matchesRetryPatterns(pe.Error() + "\n" + pe.Detail)
	}
	return matchesRetryPatterns(err.Error())
}

func isNonRetryableProviderLimit(message string) bool {
	for _, pattern := range nonRetryableLimitPatterns {
		if pattern.MatchString(message) {
			return true
		}
	}
	return false
}

func matchesRetryPatterns(message string) bool {
	if isNonRetryableProviderLimit(message) {
		return false
	}
	for _, pattern := range retryableTransientPatterns {
		if pattern.MatchString(message) {
			return true
		}
	}
	return false
}
