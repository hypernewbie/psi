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
	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/terminal"
)

const (
	codexQuotaBaseURL       = "https://chatgpt.com/backend-api"
	quotaMinSpacing         = 60 * time.Second
	quotaLocalRetry         = 5 * time.Minute
	quotaSampleTTL          = 5 * time.Minute
	quotaBackoffMaxFailures = 4
	// Each full cell represents ten percentage points of quota left.
	codexQuotaTrackCells = 10
)

type quotaCheck func(context.Context, string, time.Time) provider.QuotaUsageResult

type quotaIdentity struct {
	kind       string
	client     *provider.CodexClient
	planClient *provider.Client
	authPath   string
}

type quotaSchedule struct {
	nextStart time.Time
	failures  int
	disabled  bool
}

type quotaResult struct {
	kind        string
	generation  uint64
	usage       provider.QuotaUsageResult
	canceled    bool
	completedAt time.Time
}

type quotaOwner struct {
	check        quotaCheck
	checkPlan    func(context.Context, string, string, time.Time) provider.QuotaUsageResult
	done         chan quotaResult
	now          func() time.Time
	jitter       func(time.Duration) time.Duration
	successDelay func(float64) time.Duration

	active     quotaIdentity
	eligible   bool
	running    bool
	cancel     context.CancelFunc
	generation uint64

	displayValid bool
	used         float64
	windows      provider.QuotaWindows
	deadline     time.Time

	nextStart time.Time
	nextPoll  time.Time
	stopped   bool
	disabled  bool
	failures  int

	policyKind string
	policies   map[string]quotaSchedule
}

func newQuotaOwner(check quotaCheck) *quotaOwner {
	if check == nil {
		check = checkCodexQuota
	}
	return &quotaOwner{
		check:        check,
		checkPlan:    provider.FetchPlanUsage,
		done:         make(chan quotaResult, 1),
		now:          time.Now,
		jitter:       quotaJitter,
		successDelay: quotaBaseDelay,
		policies:     make(map[string]quotaSchedule),
	}
}

func quotaJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max) + 1))
}

func checkCodexQuota(ctx context.Context, authPath string, notBefore time.Time) provider.QuotaUsageResult {
	credential, ok := provider.ReadCodexUsageCredential(authPath, time.Now())
	if !ok {
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential}
	}
	return provider.FetchCodexUsage(ctx, authPath, credential, notBefore)
}

// quotaIdentityFor restricts requests to built-in models with official
// inference endpoints and the installed credential, never custom auth headers.
func quotaIdentityFor(r *runtime) (quotaIdentity, bool) {
	if r == nil || r.cfg.Model.Custom || r.quotaAuthOverride || r.authErr != nil {
		return quotaIdentity{}, false
	}
	kind := r.ov.Provider
	if kind == "openai-codex" {
		client, ok := r.prov.(*provider.CodexClient)
		if !ok || client == nil || client.Auth == nil || client.Auth.Path == "" || r.cfg.Model.API != "openai-codex-responses" || strings.TrimRight(client.BaseURL, "/") != codexQuotaBaseURL {
			return quotaIdentity{}, false
		}
		return quotaIdentity{kind: kind, client: client, authPath: client.Auth.Path}, true
	}
	if kind != "zai" && kind != "opencode-go" {
		return quotaIdentity{}, false
	}
	var client *provider.Client
	switch c := r.prov.(type) {
	case *provider.CompletionsClient:
		client = c.Client
	case *provider.ResponsesClient:
		client = c.Client
	case *provider.GoogleClient:
		client = c.Client
	case *provider.Client:
		client = c
	}
	if client == nil || client.APIKey == "" {
		return quotaIdentity{}, false
	}
	for name := range client.Headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "x-api-key") || strings.EqualFold(name, "x-goog-api-key") {
			return quotaIdentity{}, false
		}
	}
	base := strings.TrimRight(client.BaseURL, "/")
	if kind == "zai" {
		if r.cfg.Model.API != "openai-completions" || base != providerregistry.DefaultEndpoint(kind, r.cfg.Model.API) {
			return quotaIdentity{}, false
		}
	} else if !providerregistry.TrustedCachedEndpoint(kind, r.cfg.Model.API, base) {
		return quotaIdentity{}, false
	}
	return quotaIdentity{kind: kind, planClient: client}, true
}

// observe reacts only to an activation change. Each provider keeps its own
// start floor, failure count, and permanent stop; polls and displays do not.
func (q *quotaOwner) observe(r *runtime) bool {
	if q == nil {
		return false
	}
	identity, eligible := quotaIdentityFor(r)
	if identity == q.active && eligible == q.eligible {
		return false
	}
	q.invalidate()
	if eligible && identity.kind != q.policyKind {
		q.savePolicy()
		q.policyKind = identity.kind
		q.loadPolicy()
	}
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

func (q *quotaOwner) savePolicy() {
	if q.policyKind != "" {
		q.policies[q.policyKind] = quotaSchedule{q.nextStart, q.failures, q.disabled}
	}
}

func (q *quotaOwner) loadPolicy() {
	state := q.policies[q.policyKind]
	q.nextStart, q.failures, q.disabled = state.nextStart, state.failures, state.disabled
}

func (q *quotaOwner) invalidate() {
	q.generation++
	if q.cancel != nil {
		q.cancel()
		q.cancel = nil
	}
	q.nextPoll = time.Time{}
	q.stopped = false
	q.displayValid = false
	q.used = 0
	q.windows = provider.QuotaWindows{}
	q.deadline = time.Time{}
}

func (q *quotaOwner) close() {
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
func (q *quotaOwner) due() (time.Time, bool) {
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
// eligibility, the provider's start floor, the current poll deadline, and shutdown.
// The REPL calls it only after its exit, suspend, and console-error checks.
func (q *quotaOwner) pump(r *runtime) {
	identity, eligible := quotaIdentityFor(r)
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
	identity = q.active
	notBefore := q.nextStart
	ctx, cancel := context.WithCancel(context.Background())
	q.cancel = cancel
	go func() {
		var usage provider.QuotaUsageResult
		if identity.kind == "openai-codex" {
			usage = q.check(ctx, identity.authPath, notBefore)
		} else {
			usage = q.checkPlan(ctx, identity.kind, identity.planClient.APIKey, notBefore)
		}
		q.done <- quotaResult{
			kind:        identity.kind,
			generation:  generation,
			usage:       usage,
			canceled:    quotaCanceled(usage, ctx),
			completedAt: q.now(),
		}
	}()
}

// handleResult records GET timing and schedules from completion. Stale results
// can affect their provider's floor or stop state, but never publish a reading
// or change another provider's schedule.
func (q *quotaOwner) handleResult(result quotaResult) bool {
	if q == nil {
		return false
	}
	q.running = false
	q.cancel = nil
	if result.kind != "" && result.kind != q.policyKind {
		q.recordOtherProviderResult(result)
		return false
	}
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
				used := usage.UsedPercent
				if q.active.kind == "opencode-go" {
					used = max(used, usage.Windows.Weekly, usage.Windows.Monthly)
				}
				q.scheduleSuccess(completed, used)
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
	if quotaGETFailure(usage) {
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
	if usage.Outcome == provider.QuotaUsageNoCredential {
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

// recordOtherProviderResult accounts for an in-flight request after a provider
// switch without touching the selected provider's state.
func (q *quotaOwner) recordOtherProviderResult(result quotaResult) {
	state := q.policies[result.kind]
	usage := result.usage
	completed := result.completedAt
	if completed.IsZero() {
		completed = q.now()
	}
	if usage.StopPolling {
		state.disabled = true
	}
	var due time.Time
	switch {
	case state.disabled:
	case result.canceled:
		if !usage.StartedAt.IsZero() {
			due = q.ordinaryDue(completed)
		}
	case usage.Sample:
		due = q.ordinaryDue(completed)
	case quotaGETFailure(usage):
		state.failures = min(state.failures+1, quotaBackoffMaxFailures)
		due = q.failureDueFor(completed, usage.RetryAt, state.failures)
	case !usage.StartedAt.IsZero():
		due = q.ordinaryDue(completed)
	}
	if due.After(state.nextStart) {
		state.nextStart = due
	}
	q.policies[result.kind] = state
}

func (q *quotaOwner) publish(now time.Time, usage provider.QuotaUsageResult) bool {
	deadline := usage.StartedAt.Add(quotaSampleTTL)
	if !usage.ExpiresAt.IsZero() && usage.ExpiresAt.Before(deadline) {
		deadline = usage.ExpiresAt
	}
	if !deadline.After(now) {
		return q.clearDisplay(now)
	}
	changed := !q.displayValid || !now.Before(q.deadline) || q.used != usage.UsedPercent || q.windows != usage.Windows
	q.displayValid = true
	q.used = usage.UsedPercent
	q.windows = usage.Windows
	q.deadline = deadline
	return changed
}

func (q *quotaOwner) clearDisplay(now time.Time) bool {
	if !q.displayValid {
		return false
	}
	visible := now.Before(q.deadline)
	q.displayValid = false
	q.used = 0
	q.deadline = time.Time{}
	return visible
}

func (q *quotaOwner) scheduleSuccess(completed time.Time, used float64) {
	due := completed.Add(q.withJitter(q.successDelay(used), false))
	q.pushStart(due)
	q.nextPoll = q.nextStart
}

func (q *quotaOwner) scheduleLocal(completed time.Time) {
	due := completed.Add(q.withJitter(quotaLocalRetry, false))
	q.nextPoll = due
	if q.nextStart.After(q.nextPoll) {
		q.nextPoll = q.nextStart
	}
}

func (q *quotaOwner) ordinaryDue(completed time.Time) time.Time {
	return completed.Add(q.withJitter(quotaMinSpacing, false))
}

func (q *quotaOwner) failureDue(completed time.Time, retryAt time.Time) time.Time {
	return q.failureDueFor(completed, retryAt, q.failures)
}

func (q *quotaOwner) failureDueFor(completed time.Time, retryAt time.Time, failures int) time.Time {
	wait := quotaBackoff(failures)
	positive := false
	if !retryAt.IsZero() && retryAt.After(completed.Add(wait)) {
		wait = retryAt.Sub(completed)
		positive = true
	}
	return completed.Add(q.withJitter(wait, positive))
}

func (q *quotaOwner) withJitter(wait time.Duration, positive bool) time.Duration {
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

func (q *quotaOwner) pushStart(due time.Time) {
	if due.After(q.nextStart) {
		q.nextStart = due
	}
}

func (q *quotaOwner) incrementFailure() {
	if q.failures < quotaBackoffMaxFailures {
		q.failures++
	}
}

// quotaCanceled distinguishes an owner-canceled attempt from an observed
// HTTP response. A 200 read interrupted by cancellation is not a server
// failure; a 429 or 5xx that arrived before cancellation still counts.
func quotaCanceled(usage provider.QuotaUsageResult, ctx context.Context) bool {
	if usage.Sample || ctx.Err() != context.Canceled {
		return false
	}
	return usage.Status == 0 || usage.Status == http.StatusOK
}

func quotaGETFailure(usage provider.QuotaUsageResult) bool {
	if usage.Sample || usage.Outcome == provider.QuotaUsageNoCredential || usage.StartedAt.IsZero() {
		return false
	}
	switch usage.Status {
	case 0, http.StatusOK, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return usage.Status >= 500
}

func quotaBaseDelay(used float64) time.Duration {
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

func quotaBackoff(failures int) time.Duration {
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

func (q *quotaOwner) presentation(r *runtime) (string, terminal.ThemeToken, time.Time) {
	if q == nil || !q.displayValid {
		return "", "", time.Time{}
	}
	identity, eligible := quotaIdentityFor(r)
	if !eligible || identity != q.active || !q.now().Before(q.deadline) {
		return "", "", time.Time{}
	}
	switch identity.kind {
	case "zai":
		return "GLM " + quotaRemaining(q.used), terminal.TokenMuted, q.deadline
	case "opencode-go":
		return "Go R" + quotaRemaining(q.windows.Rolling) + " W" + quotaRemaining(q.windows.Weekly) + " M" + quotaRemaining(q.windows.Monthly), terminal.TokenMuted, q.deadline
	default:
		return codexQuotaSuffix(q.used), terminal.TokenMuted, q.deadline
	}
}

func quotaRemaining(used float64) string {
	remaining := 100 - used
	if remaining > 0 && remaining < 1 {
		return "<1%"
	}
	return fmt.Sprintf("%d%%", int(math.Floor(remaining)))
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
	filled, empty := terminal.FooterBarCells(terminal.FooterBarEighths(remaining, codexQuotaTrackCells), codexQuotaTrackCells)
	return "[" + filled + empty + "] Codex: " + label
}
