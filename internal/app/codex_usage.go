package app

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/terminal"
)

const (
	codexQuotaBaseURL            = "https://chatgpt.com/backend-api"
	codexQuotaMinSpacing         = 60 * time.Second
	codexQuotaLocalRetry         = 5 * time.Minute
	codexQuotaSampleTTL          = 5 * time.Minute
	codexQuotaBackoffMaxFailures = 4
	// One filled cell is ten percent of the remaining-quota track.
	codexQuotaTrackCells = 10
)

type codexQuotaCheck func(context.Context, string, time.Time) provider.CodexUsageResult

type codexQuotaIdentity struct {
	client   *provider.CodexClient
	authPath string
}

type codexQuotaResult struct {
	generation  uint64
	usage       provider.CodexUsageResult
	canceled    bool
	completedAt time.Time
}

type codexQuotaOwner struct {
	check        codexQuotaCheck
	done         chan codexQuotaResult
	now          func() time.Time
	jitter       func(time.Duration) time.Duration
	successDelay func(float64) time.Duration

	active     codexQuotaIdentity
	eligible   bool
	running    bool
	cancel     context.CancelFunc
	generation uint64

	displayValid bool
	used         float64
	deadline     time.Time

	nextStart time.Time
	nextPoll  time.Time
	stopped   bool
	disabled  bool
	failures  int
}

func newCodexQuotaOwner(check codexQuotaCheck) *codexQuotaOwner {
	if check == nil {
		check = checkCodexQuota
	}
	return &codexQuotaOwner{
		check:        check,
		done:         make(chan codexQuotaResult, 1),
		now:          time.Now,
		jitter:       codexQuotaJitter,
		successDelay: codexQuotaBaseDelay,
	}
}

func codexQuotaJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max) + 1))
}

func checkCodexQuota(ctx context.Context, authPath string, notBefore time.Time) provider.CodexUsageResult {
	credential, ok := provider.ReadCodexUsageCredential(authPath, time.Now())
	if !ok {
		return provider.CodexUsageResult{Outcome: provider.CodexUsageNoCredential}
	}
	return provider.FetchCodexUsage(ctx, authPath, credential, notBefore)
}

// codexQuotaIdentityFor applies the quota-only activation gate. Direct-use
// OpenAI credentials, custom models, off-origin bases, and any configured
// Authorization override never reach the Codex usage endpoint.
func codexQuotaIdentityFor(r *runtime) (codexQuotaIdentity, bool) {
	if r == nil || r.cfg.Model.Custom || r.codexQuotaAuthOverride || r.ov.Provider != "openai-codex" || r.cfg.Model.API != "openai-codex-responses" {
		return codexQuotaIdentity{}, false
	}
	client, ok := r.prov.(*provider.CodexClient)
	if !ok || client == nil || client.Auth == nil || client.Auth.Path == "" {
		return codexQuotaIdentity{}, false
	}
	if strings.TrimRight(client.BaseURL, "/") != codexQuotaBaseURL {
		return codexQuotaIdentity{}, false
	}
	return codexQuotaIdentity{client: client, authPath: client.Auth.Path}, true
}

// observe reacts only to an activation change. The global start floor, failure
// count, and permanent stop survive the switch; the current poll and display
// do not.
func (q *codexQuotaOwner) observe(r *runtime) bool {
	if q == nil {
		return false
	}
	identity, eligible := codexQuotaIdentityFor(r)
	if identity == q.active && eligible == q.eligible {
		return false
	}
	q.invalidate()
	q.active, q.eligible = identity, eligible
	if eligible && !q.disabled {
		q.stopped = false
		q.nextPoll = q.now()
		if q.nextPoll.Before(q.nextStart) {
			q.nextPoll = q.nextStart
		}
	} else {
		q.stopped = true
		q.nextPoll = time.Time{}
	}
	return true
}

func (q *codexQuotaOwner) invalidate() {
	q.generation++
	if q.cancel != nil {
		q.cancel()
		q.cancel = nil
	}
	q.nextPoll = time.Time{}
	q.stopped = false
	q.displayValid = false
	q.used = 0
	q.deadline = time.Time{}
}

func (q *codexQuotaOwner) close() {
	if q == nil {
		return
	}
	q.generation++
	if q.cancel != nil {
		q.cancel()
		q.cancel = nil
	}
	q.running = false
	q.nextPoll = time.Time{}
	q.displayValid = false
	q.used = 0
	q.deadline = time.Time{}
}

// due reports the next time the REPL should wake for a check. Only a state
// without an outstanding worker can be armed; a worker owns its result.
func (q *codexQuotaOwner) due() (time.Time, bool) {
	if q == nil || q.check == nil || q.running || !q.eligible || q.stopped || q.disabled || q.nextPoll.IsZero() {
		return time.Time{}, false
	}
	at := q.nextPoll
	if q.nextStart.After(at) {
		at = q.nextStart
	}
	return at, true
}

// pump starts at most one worker after rechecking the installed identity,
// eligibility, the global start floor, the current poll deadline, and shutdown.
// The REPL calls it only after its exit, suspend, and console-error checks.
func (q *codexQuotaOwner) pump(r *runtime) {
	identity, eligible := codexQuotaIdentityFor(r)
	if q == nil || q.check == nil || !eligible || identity != q.active || q.running || q.stopped || q.disabled || q.nextPoll.IsZero() {
		return
	}
	now := q.now()
	if now.Before(q.nextStart) || now.Before(q.nextPoll) {
		return
	}
	q.nextPoll = time.Time{}
	q.running = true
	generation := q.generation
	authPath := q.active.authPath
	notBefore := q.nextStart
	ctx, cancel := context.WithCancel(context.Background())
	q.cancel = cancel
	go func() {
		usage := q.check(ctx, authPath, notBefore)
		q.done <- codexQuotaResult{
			generation:  generation,
			usage:       usage,
			canceled:    codexQuotaCanceled(usage, ctx),
			completedAt: q.now(),
		}
	}()
}

// handleResult records GET timing and schedules from completion. A stale
// completion can move the global floor or stop all polling, but it cannot
// publish a reading, stop the new activation, or make the new activation skip
// its own schedule.
func (q *codexQuotaOwner) handleResult(result codexQuotaResult) bool {
	if q == nil {
		return false
	}
	q.running = false
	q.cancel = nil
	now := q.now()
	completed := result.completedAt
	if completed.IsZero() {
		completed = now
	}
	usage := result.usage
	current := result.generation == q.generation
	if usage.StopPolling {
		q.disabled = true
		q.nextPoll = time.Time{}
	}
	if result.canceled {
		if !usage.StartedAt.IsZero() && !q.disabled {
			q.pushStart(q.ordinaryDue(completed))
		}
		if current {
			return q.clearDisplay(now)
		}
		return false
	}
	if usage.Sample {
		if current {
			q.failures = 0
			changed := q.publish(now, usage)
			if !q.disabled && q.eligible {
				q.scheduleSuccess(completed, usage.UsedPercent)
			} else {
				q.nextPoll = time.Time{}
			}
			return changed
		}
		if !q.disabled {
			q.pushStart(q.ordinaryDue(completed))
		}
		return false
	}
	if q.disabled {
		if current {
			return q.clearDisplay(now)
		}
		return false
	}
	if codexQuotaGETFailure(usage) {
		q.incrementFailure()
		if current {
			q.pushStart(q.failureDue(completed, usage.RetryAt))
			q.nextPoll = q.nextStart
			return q.clearDisplay(now)
		}
		q.pushStart(q.failureDue(completed, usage.RetryAt))
		return false
	}
	if usage.StartedAt.IsZero() {
		if current {
			q.scheduleLocal(completed)
			return q.clearDisplay(now)
		}
		return false
	}
	if usage.Outcome == provider.CodexUsageNoCredential {
		q.pushStart(q.ordinaryDue(completed))
		if current {
			q.scheduleLocal(completed)
			return q.clearDisplay(now)
		}
		return false
	}

	// A non-200 status other than 408, 429, or 5xx stops this activation.
	q.pushStart(q.ordinaryDue(completed))
	if current {
		q.stopped = true
		q.nextPoll = time.Time{}
		return q.clearDisplay(now)
	}
	return false
}

func (q *codexQuotaOwner) publish(now time.Time, usage provider.CodexUsageResult) bool {
	deadline := usage.StartedAt.Add(codexQuotaSampleTTL)
	if !usage.ExpiresAt.IsZero() && usage.ExpiresAt.Before(deadline) {
		deadline = usage.ExpiresAt
	}
	if !deadline.After(now) {
		return q.clearDisplay(now)
	}
	changed := !q.displayValid || !now.Before(q.deadline) || q.used != usage.UsedPercent
	q.displayValid = true
	q.used = usage.UsedPercent
	q.deadline = deadline
	return changed
}

func (q *codexQuotaOwner) clearDisplay(now time.Time) bool {
	if !q.displayValid {
		return false
	}
	visible := now.Before(q.deadline)
	q.displayValid = false
	q.used = 0
	q.deadline = time.Time{}
	return visible
}

func (q *codexQuotaOwner) scheduleSuccess(completed time.Time, used float64) {
	due := completed.Add(q.withJitter(q.successDelay(used), false))
	q.pushStart(due)
	q.nextPoll = q.nextStart
}

func (q *codexQuotaOwner) scheduleLocal(completed time.Time) {
	due := completed.Add(q.withJitter(codexQuotaLocalRetry, false))
	q.nextPoll = due
	if q.nextStart.After(q.nextPoll) {
		q.nextPoll = q.nextStart
	}
}

func (q *codexQuotaOwner) ordinaryDue(completed time.Time) time.Time {
	return completed.Add(q.withJitter(codexQuotaMinSpacing, false))
}

func (q *codexQuotaOwner) failureDue(completed time.Time, retryAt time.Time) time.Time {
	wait := codexQuotaBackoff(q.failures)
	positive := false
	if !retryAt.IsZero() && retryAt.After(completed.Add(wait)) {
		wait = retryAt.Sub(completed)
		positive = true
	}
	return completed.Add(q.withJitter(wait, positive))
}

func (q *codexQuotaOwner) withJitter(wait time.Duration, positive bool) time.Duration {
	maxJitter := wait / 5
	if maxJitter <= 0 {
		return wait
	}
	jitter := q.jitter(maxJitter)
	if jitter < 0 {
		jitter = 0
	}
	if jitter > maxJitter {
		jitter = maxJitter
	}
	if positive && jitter == 0 {
		jitter = 1
	}
	return wait + jitter
}

func (q *codexQuotaOwner) pushStart(due time.Time) {
	if due.After(q.nextStart) {
		q.nextStart = due
	}
}

func (q *codexQuotaOwner) incrementFailure() {
	if q.failures < codexQuotaBackoffMaxFailures {
		q.failures++
	}
}

// codexQuotaCanceled distinguishes an owner-canceled attempt from an observed
// HTTP response. A 200 read interrupted by cancellation is not a server
// failure; a 429 or 5xx that arrived before cancellation still counts.
func codexQuotaCanceled(usage provider.CodexUsageResult, ctx context.Context) bool {
	if usage.Sample || ctx.Err() != context.Canceled {
		return false
	}
	return usage.Status == 0 || usage.Status == http.StatusOK
}

func codexQuotaGETFailure(usage provider.CodexUsageResult) bool {
	if usage.Sample || usage.Outcome == provider.CodexUsageNoCredential || usage.StartedAt.IsZero() {
		return false
	}
	switch usage.Status {
	case 0, http.StatusOK, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return usage.Status >= 500
}

func codexQuotaBaseDelay(used float64) time.Duration {
	switch {
	case used >= 99:
		return 5 * time.Second
	case used >= 90:
		return 15 * time.Second
	case used >= 75:
		return 30 * time.Second
	default:
		return 60 * time.Second
	}
}

func codexQuotaBackoff(failures int) time.Duration {
	switch failures {
	case 0, 1:
		return 5 * time.Minute
	case 2:
		return 10 * time.Minute
	case 3:
		return 20 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func (q *codexQuotaOwner) presentation(r *runtime) (string, terminal.ThemeToken, time.Time) {
	if q == nil || !q.displayValid {
		return "", "", time.Time{}
	}
	identity, eligible := codexQuotaIdentityFor(r)
	if !eligible || identity != q.active || !q.now().Before(q.deadline) {
		return "", "", time.Time{}
	}
	return codexQuotaSuffix(q.used), terminal.TokenMuted, q.deadline
}

func codexQuotaSuffix(used float64) string {
	remaining := 100 - used
	label := "100% Left"
	if used > 0 {
		floor := int(math.Floor(remaining))
		switch {
		case floor >= 100:
			label = "99% Left"
		case floor >= 1:
			label = fmt.Sprintf("%d%% Left", floor)
		case remaining > 0:
			label = "<1% Left"
		default:
			label = "0% Left"
		}
	}
	filled, empty := terminal.FooterBarCells(terminal.FooterBarEighths(used, codexQuotaTrackCells), codexQuotaTrackCells)
	return "[" + filled + empty + "] Codex: " + label
}
