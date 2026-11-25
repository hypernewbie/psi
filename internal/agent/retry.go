package agent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/hypernewbie/psi/internal/event"
	"github.com/hypernewbie/psi/internal/provider"
)

// errRetryCancelled is Pi's finalError for a retry the user aborted.
var errRetryCancelled = errors.New("Retry cancelled")

// retryDelayMs is Pi's retryDelayMs: the wait before retry number attempt
// (1 for the first retry) doubles from baseDelayMs and never exceeds
// maxDelayMs. A negative setting waits nothing.
func retryDelayMs(baseDelayMs, attempt, maxDelayMs int) time.Duration {
	// The settings are not range-checked, so the wait is computed as int64
	// milliseconds and clamped to what a time.Duration can hold.
	largestMs := int64(math.MaxInt64 / time.Millisecond)
	delayMs := int64(baseDelayMs)
	for i := 1; i < attempt && delayMs > 0; i++ {
		if delayMs > largestMs/2 {
			delayMs = largestMs
			break
		}
		delayMs *= 2
	}
	if delayMs > int64(maxDelayMs) {
		delayMs = int64(maxDelayMs)
	}
	if delayMs < 0 {
		delayMs = 0
	}
	if delayMs > largestMs {
		delayMs = largestMs
	}
	return time.Duration(delayMs) * time.Millisecond
}

// scheduleAutoRetry is Pi's _prepareRetry: an assistant response that failed
// with a transient provider error is retried after an exponential backoff.
// The failed attempt stays in raw session history, but a context edit omits
// it from model projection before the wait, so the retried request carries
// the same model context as the failed one. Pi announces the retry before it
// writes that edit; the announcement follows the edit here, so a storage
// failure never leaves an announced retry without its end event.
//
// It reports whether the turn should run again. A requested cancellation, a
// context overflow (which belongs to compaction recovery), a non-transient
// error, and an exhausted budget all end the run without a retry. A storage
// failure is returned and stops the run.
func (l *Loop) scheduleAutoRetry(ctx context.Context, cfg Config, failedEntryID string, cause error) (bool, error) {
	if ctx.Err() != nil {
		return false, nil
	}
	var pe *provider.ProviderError
	if errors.As(cause, &pe) && pe.Kind == provider.ErrContextLimit {
		return false, nil
	}
	if !provider.IsRetryableError(cause) {
		return false, nil
	}
	l.retryAttempt++
	if l.retryAttempt > cfg.RetryAttempts {
		// Keep the completed retry count so endAutoRetry can report it.
		l.retryAttempt--
		return false, nil
	}
	delay := retryDelayMs(cfg.RetryBaseDelayMs, l.retryAttempt, cfg.RetryMaxAgentDelayMs)
	omittedID, err := l.omitFailedAttempt(cfg, failedEntryID, cause)
	if err != nil {
		// No retry was announced, so leave no retry count to report.
		l.retryAttempt--
		return false, err
	}
	l.sink(event.Event{Type: event.AutoRetryStart, RetryAttempt: l.retryAttempt, RetryMaximum: cfg.RetryAttempts,
		RetryDelayMs: delay.Milliseconds(), Text: cause.Error(), CommittedID: omittedID})
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, nil
	case <-timer.C:
	}
	return true, nil
}

// omitFailedAttempt is the context edit of Pi's _omitRecoveryAttempt. An
// attempt without visible output has no record yet, so Pi's failure marker is
// written first: the response with one empty text block. The entry then stays
// in raw session history, with any visible text in the transcript, while the
// null edit removes it from model projection. The attempt never carries tool
// calls, because tools run only after a response settles successfully, so
// there is no tool result to omit with it. It returns the ID of the omitted
// entry.
func (l *Loop) omitFailedAttempt(cfg Config, entryID string, cause error) (string, error) {
	if entryID == "" {
		id, err := l.store.AppendMessage(toFailureAssistant(&provider.Response{StopReason: "error"}, cfg, cause.Error()))
		if err != nil {
			return "", fmt.Errorf("storage failure, stopping: %w", err)
		}
		entryID = id
	}
	if _, err := l.store.AppendContextEdit(entryID, nil); err != nil {
		return "", fmt.Errorf("storage failure, stopping: %w", err)
	}
	return entryID, nil
}

// endAutoRetry is Pi's auto_retry_end: it reports the outcome of the retries
// attempted so far and resets the counter. A nil error is success; a retry
// the user aborted reports errRetryCancelled. Nothing is emitted when the
// failed response was not retried.
func (l *Loop) endAutoRetry(finalErr error) {
	attempt := l.retryAttempt
	l.retryAttempt = 0
	if attempt == 0 {
		return
	}
	l.sink(event.Event{Type: event.AutoRetryEnd, RetryAttempt: attempt, Err: finalErr})
}
