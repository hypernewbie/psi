package app

import (
	"context"
	"crypto/sha256"
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
	accountQuotaPoll        = 5 * time.Minute
	accountQuotaTTL         = 6 * time.Minute
	quotaSampleTTL          = 5 * time.Minute
	quotaBackoffMaxFailures = 4
	// Each full cell represents ten percentage points of quota left.
	codexQuotaTrackCells = 10
)

type quotaCheck func(context.Context, string, time.Time) provider.QuotaUsageResult

// quotaInstallation records the inference client that configure installed: the
// provider, model, API, endpoint, and a one-way fingerprint of the API key it
// received. Account usage sources compare it so they never query an account
// other than the installed client's. No field is rendered, logged, or stored.
type quotaInstallation struct {
	provider         string
	model            string
	api              string
	base             string
	keySHA           [sha256.Size]byte
	configuredKeySHA [sha256.Size]byte
	hasKey           bool
}

type quotaIdentity struct {
	kind       string
	client     *provider.CodexClient
	planClient *provider.Client
	authPath   string
	install    quotaInstallation
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
	accountText  string
	accountToken terminal.ThemeToken
	accountNext  time.Time // shared completion floor for balance sources
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
	if kind != "zai" && kind != "opencode-go" && kind != "deepseek" {
		return quotaIdentity{}, false
	}
	var client *provider.Client
	switch c := r.prov.(type) {
	case *provider.CompletionsClient:
		if c != nil {
			client = c.Client
		}
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
	install := r.quotaInstall
	if kind == "deepseek" {
		if _, _, ok := r.activeUsagePolicy(kind); !ok || r.cfg.Model.API != install.api ||
			r.cfg.Model.ID != install.model || base != install.base || sha256.Sum256([]byte(client.APIKey)) != install.keySHA {
			return quotaIdentity{}, false
		}
		// Do not send the inference client's key to the worker. It binds a
		// fresh credential read to the installed fingerprint instead.
		return quotaIdentity{kind: kind, install: install}, true
	}
	if kind == "zai" {
		if r.cfg.Model.API != "openai-completions" || base != providerregistry.DefaultEndpoint(kind, r.cfg.Model.API) {
			return quotaIdentity{}, false
		}
		// The Z.AI reading belongs to the plan account that the installed
		// client uses. A client built for another route or key cannot read it.
		if install.provider != kind || install.model != r.ov.Model || install.api != r.cfg.Model.API || install.base != base || !install.hasKey {
			return quotaIdentity{}, false
		}
	} else if !providerregistry.TrustedCachedEndpoint(kind, r.cfg.Model.API, base) {
		return quotaIdentity{}, false
	}
	return quotaIdentity{kind: kind, planClient: client, install: install}, true
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
	q.accountText, q.accountToken = "", ""
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
	q.accountText, q.accountToken = "", ""
	q.used = 0
	q.deadline = time.Time{}
}

// pauseAccount cancels account polling without freeing the outstanding slot.
// Legacy sources keep their existing suspend behavior.
func (q *quotaOwner) pauseAccount() {
	if q.active.kind == "deepseek" && q.eligible {
		q.invalidate()
		q.eligible = false
		q.active = quotaIdentity{}
	}
}

func (q *quotaOwner) expireAccount() bool {
	now := q.now()
	if q.active.kind == "deepseek" && q.displayValid && !now.Before(q.deadline) {
		return q.clearDisplay(now)
	}
	return false
}

// due reports the next poll or balance display expiry. Only a state without
// a worker can start a poll, but expiry still wakes an idle terminal.
func (q *quotaOwner) due() (time.Time, bool) {
	if q == nil {
		return time.Time{}, false
	}
	var at time.Time
	if q.check != nil && !q.running && q.eligible && !q.stopped && !q.disabled && !q.nextPoll.IsZero() {
		at = q.pollDue()
	}
	if q.active.kind == "deepseek" && q.displayValid && (at.IsZero() || q.deadline.Before(at)) {
		at = q.deadline
	}
	return at, !at.IsZero()
}

func (q *quotaOwner) pollDue() time.Time {
	at := q.nextPoll
	if q.nextStart.After(at) {
		at = q.nextStart
	}
	if q.active.kind == "deepseek" && q.accountNext.After(at) {
		at = q.accountNext
	}
	return at
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
	if now.Before(q.pollDue()) {
		return
	}
	var capture usageCapture
	notBefore := q.nextStart
	if identity.kind == "deepseek" {
		var ok bool
		capture, _, ok = r.usageActiveCapture(identity.kind)
		if !ok {
			return
		}
		notBefore = q.pollDue()
	}
	q.nextPoll = time.Time{}
	q.running = true
	generation := q.generation
	identity = q.active
	var ctx context.Context
	var cancel context.CancelFunc
	if identity.kind == "deepseek" {
		ctx, cancel = context.WithTimeout(context.Background(), usageLookupTimeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	q.cancel = cancel
	go func() {
		defer cancel()
		var usage provider.QuotaUsageResult
		switch identity.kind {
		case "openai-codex":
			usage = q.check(ctx, identity.authPath, notBefore)
		case "deepseek":
			usage, _ = lookupUsage(ctx, capture, notBefore)
		default:
			usage = q.checkPlan(ctx, identity.kind, identity.planClient.APIKey, notBefore)
		}
		canceled := quotaCanceled(usage, ctx)
		if identity.kind == "deepseek" && ctx.Err() == context.Canceled {
			canceled = !quotaTransientStatus(usage.Status)
		}
		q.done <- quotaResult{
			kind:        identity.kind,
			generation:  generation,
			usage:       usage,
			canceled:    canceled,
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
	if result.kind == "deepseek" {
		completed := result.completedAt
		if completed.IsZero() {
			completed = q.now()
		}
		q.accountNext = completed.Add(quotaMinSpacing)
	}
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
				if q.active.kind == "deepseek" {
					q.pushStart(completed.Add(accountQuotaPoll))
					q.nextPoll = q.nextStart
				} else {
					q.scheduleSuccess(completed, used)
				}
			} else {
				q.nextPoll = time.Time{}
			}
			return changed
		}
		if !q.disabled {
			q.pushStart(q.retiredSuccessDue(result.kind, completed))
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
		due = q.retiredSuccessDue(result.kind, completed)
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

func (q *quotaOwner) retiredSuccessDue(kind string, completed time.Time) time.Time {
	if kind == "deepseek" {
		return completed.Add(accountQuotaPoll)
	}
	return q.ordinaryDue(completed)
}

func (q *quotaOwner) publish(now time.Time, usage provider.QuotaUsageResult) bool {
	deadline := usage.StartedAt.Add(quotaSampleTTL)
	var text string
	var token terminal.ThemeToken
	if q.active.kind == "deepseek" {
		deadline = usage.StartedAt.Add(accountQuotaTTL)
		text, token = deepSeekQuotaSuffix(usage.Balance)
		if text == "" {
			return q.clearDisplay(now)
		}
	}
	if !usage.ExpiresAt.IsZero() && usage.ExpiresAt.Before(deadline) {
		deadline = usage.ExpiresAt
	}
	if !deadline.After(now) {
		return q.clearDisplay(now)
	}
	changed := !q.displayValid || !now.Before(q.deadline) || q.used != usage.UsedPercent || q.windows != usage.Windows ||
		q.accountText != text || q.accountToken != token || (text != "" && q.deadline != deadline)
	q.accountText, q.accountToken = text, token
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
	visible := q.active.kind == "deepseek" || now.Before(q.deadline)
	q.accountText, q.accountToken = "", ""
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
	case 0, http.StatusOK:
		return true
	}
	return quotaTransientStatus(usage.Status)
}

func quotaTransientStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
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
	case "deepseek":
		return q.accountText, q.accountToken, q.deadline
	case "zai":
		return zaiQuotaSuffix(q.used), terminal.TokenMuted, q.deadline
	case "opencode-go":
		return "Go R" + quotaRemaining(q.windows.Rolling) + " W" + quotaRemaining(q.windows.Weekly) + " M" + quotaRemaining(q.windows.Monthly), terminal.TokenMuted, q.deadline
	default:
		return codexQuotaSuffix(q.used), terminal.TokenMuted, q.deadline
	}
}

func deepSeekQuotaSuffix(balance *provider.DeepSeekBalance) (string, terminal.ThemeToken) {
	if balance == nil {
		return "", ""
	}
	if balance.Available != nil && !*balance.Available {
		return "DeepSeek unavailable", terminal.TokenError
	}
	if len(balance.Balances) == 0 {
		return "", ""
	}
	amount := balance.Balances[0]
	for _, candidate := range balance.Balances {
		if candidate.Currency == "USD" {
			amount = candidate
			break
		}
	}
	return "DeepSeek " + amount.Currency + " " + amount.Total, usageMoneyToken(amount.Total)
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

// zaiQuotaSuffix matches the Codex track direction: the bar starts full and
// drains as the five-hour plan quota is used, while the caption states what is
// left. Z.AI reports used percentage, so the track receives the remainder.
func zaiQuotaSuffix(used float64) string {
	remaining := 100 - used
	filled, empty := terminal.FooterBarCells(terminal.FooterBarEighths(remaining, codexQuotaTrackCells), codexQuotaTrackCells)
	return "[" + filled + empty + "] GLM 5h " + quotaRemaining(used) + " Left"
}
