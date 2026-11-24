package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/provider"
	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/theme"
)

// usageLookupTimeout bounds all work in one manual slot, including auth reads.
const usageLookupTimeout = 10 * time.Second

// usageProviders lists the supported lookups in Accounts order.
// OpenCode Go has a footer indicator only.
var usageProviders = []string{"zai", "deepseek"}

func usageSupported(providerID string) bool {
	for _, id := range usageProviders {
		if id == providerID {
			return true
		}
	}
	return false
}

// usageProviderName returns the registry display name for a provider ID. It is
// the only provider identity the report renders.
func usageProviderName(providerID string) string {
	if spec, ok := providerregistry.Lookup(providerID); ok && spec.Name != "" {
		return spec.Name
	}
	return providerID
}

// usageReason is a fixed local explanation for an unavailable usage source.
// It never carries provider text, response bodies, or credentials.
type usageReason string

const (
	usageReasonUnsupported   usageReason = "provider is not supported"
	usageReasonInactive      usageReason = "provider is not active"
	usageReasonIneligible    usageReason = "provider is not eligible"
	usageReasonNoCredential  usageReason = "no usable credential"
	usageReasonAuthBusy      usageReason = "credential store is busy"
	usageReasonChangedConfig usageReason = "model configuration changed"
	usageReasonChangedKey    usageReason = "credential changed"
	usageReasonRejectedKey   usageReason = "credential was rejected"
	usageReasonRateLimited   usageReason = "rate limited"
	usageReasonTimeout       usageReason = "request timed out"
	usageReasonTransient     usageReason = "service unavailable"
	usageReasonUnreadable    usageReason = "usage response is unreadable"
)

// usageCapture is the immutable REPL-thread authorization state for one manual
// report. Workers receive only these values and never read the runtime.
type usageCapture struct {
	providerID   string
	authPath     string
	models       *config.ModelsFile // non-active lookups require this catalog to stay current
	keyInput     provider.APIKeyInput
	allowMissing bool
	installed    quotaInstallation
	checkPlan    func(context.Context, string, string, time.Time) provider.QuotaUsageResult
	checkBalance quotaCheck
}

// usageRow is one panel line before width-dependent formatting. Callers
// supply text and style only; the panel never receives provider types.
type usageRow struct {
	text  string
	token terminal.ThemeToken
	bold  bool
}

// usageReport is one sanitized reading. A usable report carries the plan
// windows the panel renders; every other report carries only a fixed local
// reason.
type usageReport struct {
	providerID string
	plan       *provider.PlanWindows
	balance    *provider.DeepSeekBalance
	keySHA     [sha256.Size]byte // proof for reusing detail without another GET
	reason     usageReason
}

// usable reports whether the provider returned any validated account data.
func (report usageReport) usable() bool {
	return report.balance != nil || report.plan != nil && (report.plan.FiveHour != nil || report.plan.Weekly != nil)
}

// panelRows renders one sanitized reading. Only the registry provider name and
// fixed local reasons appear; no provider text reaches the panel.
func (report usageReport) panelRows() []usageRow {
	title := usageRow{text: "Usage · " + usageProviderName(report.providerID), token: terminal.TokenAccent, bold: true}
	if !report.usable() {
		return []usageRow{title, {text: string(report.reason) + ".", token: terminal.TokenError}, usageHintRow()}
	}
	rows := []usageRow{title}
	switch report.providerID {
	case "zai":
		rows = append(rows, usageWindowRow("5 hour", report.plan.FiveHour), usageWindowRow("Weekly", report.plan.Weekly))
	case "deepseek":
		rows = append(rows, deepSeekBalanceRows(report.balance)...)
	default:
		rows = append(rows, usageRow{text: string(usageReasonUnsupported) + ".", token: terminal.TokenError})
	}
	return append(rows, usageHintRow())
}

func deepSeekBalanceRows(balance *provider.DeepSeekBalance) []usageRow {
	var rows []usageRow
	if balance.Available != nil {
		if *balance.Available {
			rows = append(rows, usageRow{text: "Account available", token: terminal.TokenText})
		} else {
			rows = append(rows, usageRow{text: "Account unavailable", token: terminal.TokenError})
		}
	}
	for _, amount := range balance.Balances {
		rows = append(rows, usageRow{text: "Balance: " + amount.Currency + " " + amount.Total, token: usageMoneyToken(amount.Total)})
		if amount.Granted != "" {
			rows = append(rows, usageRow{text: "  Granted: " + amount.Currency + " " + amount.Granted, token: terminal.TokenMuted})
		}
		if amount.ToppedUp != "" {
			rows = append(rows, usageRow{text: "  Topped up: " + amount.Currency + " " + amount.ToppedUp, token: terminal.TokenMuted})
		}
	}
	return rows
}

func usageMoneyToken(total string) terminal.ThemeToken {
	if total == "0.00" || strings.HasPrefix(total, "-") {
		return terminal.TokenError
	}
	return terminal.TokenText
}

// usageWindowRow shows one window's remaining quota. A missing window is
// unavailable, never zero.
func usageWindowRow(label string, used *float64) usageRow {
	if used == nil {
		return usageRow{text: fmt.Sprintf("%-8s unavailable", label), token: terminal.TokenMuted}
	}
	return usageRow{text: fmt.Sprintf("%-8s %s left", label, quotaRemaining(*used)), token: terminal.TokenText}
}

func usageHintRow() usageRow {
	return usageRow{text: "Enter accounts · Esc close", token: terminal.TokenMuted}
}

// usageLoadingRows shows the provider being queried before its reply arrives.
func usageLoadingRows(providerID string) []usageRow {
	return []usageRow{
		{text: "Usage · " + usageProviderName(providerID), token: terminal.TokenAccent, bold: true},
		{text: "Loading…", token: terminal.TokenMuted},
		usageHintRow(),
	}
}

// usagePanel is the REPL-owned read-only view behind /usage. It holds display
// rows and a clamped viewport only: provider data and request state stay with
// the caller, so later usage screens reuse it without new terminal chrome.
type usagePanel struct {
	rows  []usageRow
	off   int
	limit int // body rows visible at the last draw
}

// view renders at most budget rows starting at the clamped offset. The
// terminal adds its usual border and footer around them.
func (p *usagePanel) view(width, budget int, noColor bool, palette *theme.Theme) []string {
	if p == nil || len(p.rows) == 0 {
		return nil
	}
	budget = max(1, budget)
	p.limit = budget
	p.off = min(p.off, max(0, len(p.rows)-budget))
	end := min(len(p.rows), p.off+budget)
	rows := make([]string, 0, end-p.off)
	for _, row := range p.rows[p.off:end] {
		rows = append(rows, terminal.FormatStyledRow(terminal.StyledRow{
			Spans: []terminal.StyledSpan{{Text: " " + row.text, Token: row.token, Bold: row.bold}},
		}, width, noColor, palette))
	}
	return rows
}

// move scrolls by delta rows inside the last drawn viewport.
func (p *usagePanel) move(delta int) {
	if p == nil {
		return
	}
	maxOff := max(0, len(p.rows)-max(1, p.limit))
	p.off = min(max(p.off+delta, 0), maxOff)
}

// usageJobResult carries either one lookup or the locally eligible account IDs.
type usageJobResult struct {
	report      usageReport
	accounts    []string
	reason      usageReason
	completedAt time.Time // after the last credential check, before publication
}

// usageRequest is the REPL's single manual slot. A retired request keeps this
// slot until its worker sends one result, which the REPL then discards.
type usageRequest struct {
	capture  usageCapture
	models   *config.ModelsFile // non-nil only for Accounts discovery
	cancel   context.CancelFunc
	done     chan usageJobResult
	timer    *time.Timer
	deadline <-chan time.Time
	cutoff   time.Time
	expired  bool
	retired  bool
}

func (r *runtime) startUsageJob(capture usageCapture, discover bool, work func(context.Context) usageJobResult) *usageRequest {
	ctx, cancel := context.WithTimeout(context.Background(), usageLookupTimeout)
	cutoff, _ := ctx.Deadline()
	request := &usageRequest{capture: capture, cancel: cancel, done: make(chan usageJobResult, 1), cutoff: cutoff}
	if discover {
		request.models = r.models
	}
	if r.usageDeadline != nil {
		request.deadline = r.usageDeadline()
	} else {
		request.timer = time.NewTimer(time.Until(cutoff))
		request.deadline = request.timer.C
	}
	go func() {
		defer cancel()
		result := work(ctx)
		result.completedAt = time.Now()
		request.done <- result
	}()
	return request
}

// usageCapture resolves the requested provider and snapshots the authorization
// state for it. It performs no credential or network I/O.
func (r *runtime) usageCapture(arg string) (usageCapture, string) {
	if r == nil || r.models == nil {
		return usageCapture{}, "Usage unavailable: no model configuration."
	}
	requested := strings.TrimSpace(arg)
	if len(strings.Fields(requested)) > 1 {
		return usageCapture{}, "Usage: use /usage [provider]."
	}
	providerID := r.ov.Provider
	if requested != "" {
		resolved, refusal := r.usageProviderFromArgument(requested)
		if refusal != "" {
			return usageCapture{}, refusal
		}
		providerID = resolved
	}
	if !usageSupported(providerID) {
		return usageCapture{}, usageRefusal(providerID, usageReasonUnsupported)
	}
	var capture usageCapture
	var reason usageReason
	var ok bool
	if providerID == r.ov.Provider {
		capture, reason, ok = r.usageActiveCapture(providerID)
	} else {
		capture, ok = r.usageOtherCapture(providerID)
		reason = usageReasonIneligible
	}
	if !ok {
		return usageCapture{}, usageRefusal(providerID, reason)
	}
	return capture, ""
}

// usageProviderFromArgument resolves one argument to a provider ID. An exact ID
// wins; otherwise a unique case-insensitive match across built-in and
// configured IDs is accepted. The raw argument is never echoed.
func (r *runtime) usageProviderFromArgument(arg string) (string, string) {
	ids := r.providerIDs()
	for _, id := range ids {
		if id == arg {
			return id, ""
		}
	}
	lower := strings.ToLower(arg)
	match := ""
	for _, id := range ids {
		if strings.ToLower(id) != lower {
			continue
		}
		if match != "" && match != id {
			return "", "Usage: provider name is ambiguous."
		}
		match = id
	}
	if match == "" {
		return "", "Usage: unknown provider."
	}
	return match, ""
}

func usageRefusal(providerID string, reason usageReason) string {
	name := usageProviderName(providerID)
	if name == "" {
		return "Usage unavailable: " + string(reason) + "."
	}
	return "Usage unavailable for " + name + ": " + string(reason) + "."
}

// usageActiveCapture snapshots the active provider's direct route, apiKeyPolicy
// input, auth path, environment, and installed client identity. A route or
// credential the installed client was not built from is refused.
func (r *runtime) usageActiveCapture(providerID string) (usageCapture, usageReason, bool) {
	if r.ov.Provider != providerID {
		return usageCapture{}, usageReasonInactive, false
	}
	keyInput, allowMissing, ok := r.activeUsagePolicy(providerID)
	if !ok {
		return usageCapture{}, usageReasonIneligible, false
	}
	identity, eligible := quotaIdentityFor(r)
	if !eligible || identity.kind != providerID {
		return usageCapture{}, usageReasonIneligible, false
	}
	return usageCapture{
		providerID: providerID, authPath: filepath.Join(r.agentDir, "auth.json"),
		keyInput: keyInput, allowMissing: allowMissing, installed: r.quotaInstall,
		checkPlan: r.manualUsagePlanCheck(), checkBalance: r.usageBalanceCheck,
	}, "", true
}

func (r *runtime) manualUsagePlanCheck() func(context.Context, string, string, time.Time) provider.QuotaUsageResult {
	if r.usagePlanCheck != nil {
		return r.usagePlanCheck
	}
	return r.planQuotaCheck
}

// activeUsagePolicy compares the current catalog route against the installed
// client, including when the model picker has reloaded the catalog alone.
func (r *runtime) activeUsagePolicy(providerID string) (provider.APIKeyInput, bool, bool) {
	if r.models == nil {
		return provider.APIKeyInput{}, false, false
	}
	meta, err := r.models.SelectModel(providerID, r.ov.Model)
	if err != nil {
		return provider.APIKeyInput{}, false, false
	}
	configured := r.models.Providers[providerID]
	// SelectModel merges request headers but not the provider-level authHeader.
	if meta.Custom || meta.AuthHeader || configured.AuthHeader || usageAuthHeader(meta.Headers) {
		return provider.APIKeyInput{}, false, false
	}
	base := strings.TrimSuffix(meta.BaseURL, "/")
	installed := r.quotaInstall
	if !usageDirectRoute(providerID, meta.API, meta.BaseURL) ||
		!installed.hasKey || installed.provider != providerID || installed.model != r.ov.Model ||
		installed.api != meta.API || installed.base != base {
		return provider.APIKeyInput{}, false, false
	}
	keyInput, allowMissing := apiKeyPolicy(providerID, meta, configured)
	if providerID == "deepseek" && installed.configuredKeySHA != sha256.Sum256([]byte(keyInput.Value)) {
		return provider.APIKeyInput{}, false, false
	}
	return keyInput, allowMissing, true
}

// usageStillCurrent rechecks the catalog or installed route before displaying
// a reading. The worker already rechecked the credential itself.
func (r *runtime) usageStillCurrent(capture usageCapture) bool {
	if capture.models != nil {
		return r.models == capture.models && r.ov.Provider != capture.providerID &&
			capture.authPath == filepath.Join(r.agentDir, "auth.json")
	}
	current, _, ok := r.usageActiveCapture(capture.providerID)
	if !ok {
		return false
	}
	return current.installed == capture.installed &&
		current.authPath == capture.authPath &&
		current.keyInput == capture.keyInput &&
		current.allowMissing == capture.allowMissing
}

// usageDirectRoute only accepts the registered API and endpoint, allowing a
// single trailing slash where clients join the request path.
func usageDirectRoute(providerID, api, base string) bool {
	endpoint := providerregistry.DefaultEndpoint(providerID, api)
	return api == providerregistry.DefaultAPI(providerID) && endpoint != "" && strings.TrimSuffix(base, "/") == endpoint
}

// usageAuthHeader reports any case-insensitive authentication header entry,
// including an empty one. Such an entry can redirect billing away from the
// installed API key.
func usageAuthHeader(headers map[string]string) bool {
	for name := range headers {
		switch {
		case strings.EqualFold(name, "Authorization"),
			strings.EqualFold(name, "X-API-Key"),
			strings.EqualFold(name, "X-Goog-API-Key"):
			return true
		}
	}
	return false
}

// fetchUsageReport resolves the credential, performs one fixed request and
// rechecks the key before returning a usable sample. Active lookups also bind
// the key to the installed client's fingerprint.
func fetchUsageReport(ctx context.Context, capture usageCapture) usageReport {
	result, keySHA, reason := lookupUsage(ctx, capture, time.Time{})
	if reason != "" {
		return usageReport{providerID: capture.providerID, reason: reason}
	}
	if capture.providerID == "deepseek" {
		return usageReport{providerID: capture.providerID, balance: result.Balance, keySHA: keySHA}
	}
	sample := result.Plan
	if sample == nil && result.Sample {
		// A legacy injection only carries the footer percentage; the panel
		// shows it as the five-hour reading.
		used := result.UsedPercent
		sample = &provider.PlanWindows{FiveHour: &used}
	}
	return usageReport{providerID: capture.providerID, plan: sample, keySHA: keySHA}
}

// lookupUsage shares the credential checks between manual reports and
// account balance polls. Rejected samples keep bounded HTTP metadata.
func lookupUsage(ctx context.Context, capture usageCapture, notBefore time.Time) (provider.QuotaUsageResult, [sha256.Size]byte, usageReason) {
	key, keySHA, reason := checkedUsageKey(ctx, capture)
	if reason != "" {
		return provider.QuotaUsageResult{Outcome: provider.QuotaUsageNoCredential}, keySHA, reason
	}
	var result provider.QuotaUsageResult
	if capture.providerID == "deepseek" {
		check := capture.checkBalance
		if check == nil {
			check = provider.FetchDeepSeekBalance
		}
		result = check(ctx, key, notBefore)
	} else if capture.checkPlan != nil {
		result = capture.checkPlan(ctx, capture.providerID, key, notBefore)
	} else {
		result = provider.FetchZaiUsage(ctx, key, notBefore)
	}
	if !result.Sample && result.Balance == nil && (result.Plan == nil || result.Plan.FiveHour == nil && result.Plan.Weekly == nil) {
		return result, keySHA, usageFailureReason(result, ctx)
	}
	_, currentSHA, reason := checkedUsageKey(ctx, capture)
	if reason == "" && currentSHA != keySHA {
		reason = usageReasonChangedKey
	}
	if reason != "" {
		result.Sample, result.Plan, result.Balance = false, nil, nil
		result.Outcome = provider.QuotaUsageNoCredential
		return result, keySHA, reason
	}
	return result, keySHA, ""
}

// checkedUsageKey resolves current auth without storing the key in the job.
// For the active provider it also verifies the installed client's credential.
func checkedUsageKey(ctx context.Context, capture usageCapture) (string, [sha256.Size]byte, usageReason) {
	var keySHA [sha256.Size]byte
	if ctx.Err() != nil {
		return "", keySHA, usageReasonTimeout
	}
	key, _, err := provider.SelectProviderAPIKeyInputContext(ctx, envMap(), capture.authPath, capture.providerID, capture.keyInput)
	if ctx.Err() != nil {
		return "", keySHA, usageReasonTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "", keySHA, usageReasonAuthBusy
	}
	if err != nil || !provider.UsageKeyHeaderSafe(key) {
		return "", keySHA, usageReasonNoCredential
	}
	keySHA = sha256.Sum256([]byte(key))
	if capture.installed.hasKey && keySHA != capture.installed.keySHA {
		return "", keySHA, usageReasonChangedKey
	}
	return key, keySHA, ""
}

// usageFailureReason maps a bounded provider result to a fixed local reason.
func usageFailureReason(result provider.QuotaUsageResult, ctx context.Context) usageReason {
	switch result.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return usageReasonRejectedKey
	case http.StatusTooManyRequests:
		return usageReasonRateLimited
	}
	if result.Outcome == provider.QuotaUsageNoCredential {
		return usageReasonNoCredential
	}
	if result.Status == 0 || result.Status == http.StatusRequestTimeout || result.Status >= 500 {
		if ctx.Err() != nil && result.Status == 0 {
			return usageReasonTimeout
		}
		return usageReasonTransient
	}
	return usageReasonUnreadable
}

// stopDeadline stops the pending deadline wake. A fired timer leaves no value
// to drain on this path, so the non-blocking receive cannot block the REPL.
func (u *usageRequest) stopDeadline() {
	if u.timer != nil {
		if !u.timer.Stop() {
			select {
			case <-u.timer.C:
			default:
			}
		}
	}
	u.deadline = nil
}

// retire abandons the request on suspend, terminal release, or shutdown. Its
// workers keep their slot until they return, but their results never print.
func (u *usageRequest) retire() {
	if u == nil || u.retired {
		return
	}
	u.retired = true
	if u.cancel != nil {
		u.cancel()
	}
	u.stopDeadline()
}

// completedInTime uses the worker's timestamp, not the order in which the
// REPL sees the timer and the result. A busy REPL may see both at once.
func (u *usageRequest) completedInTime(result usageJobResult) bool {
	return !result.completedAt.After(u.cutoff)
}

// expire marks the deadline as passed. Buffered completions are accepted only
// if their timestamps beat the cutoff. Later ones only free the slot.
func (u *usageRequest) expire() {
	if u == nil || u.expired || u.retired {
		return
	}
	u.expired = true
	u.cancel()
	u.stopDeadline()
}
