package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/hypernewbie/psi/internal/config"
	"github.com/hypernewbie/psi/internal/providerregistry"
	"github.com/hypernewbie/psi/internal/terminal"
	"github.com/hypernewbie/psi/internal/theme"
)

// usageOtherCapture admits a provider only when its route is unambiguous
// without choosing a model. The catalog is immutable once published.
func (r *runtime) usageOtherCapture(id string) (usageCapture, bool) {
	if r.models == nil || !usageSupported(id) || id == r.ov.Provider {
		return usageCapture{}, false
	}
	api := providerregistry.DefaultAPI(id)
	base := providerregistry.DefaultEndpoint(id, api)
	configured := r.models.Providers[id]
	if base == "" || (configured.API != "" && configured.API != api) ||
		(configured.BaseURL != "" && !usageDirectRoute(id, api, configured.BaseURL)) ||
		configured.AuthHeader || usageAuthHeader(configured.Headers) {
		return usageCapture{}, false
	}
	isDirect := func(modelID string) bool {
		if strings.TrimSpace(modelID) == "" {
			return false
		}
		meta, err := r.models.SelectModel(id, modelID)
		return err == nil && !meta.Custom && usageDirectRoute(id, meta.API, meta.BaseURL) &&
			!meta.AuthHeader && !usageAuthHeader(meta.Headers)
	}
	declared := make(map[string]bool, len(configured.Models))
	for _, model := range configured.Models {
		if declared[model.ID] || !isDirect(model.ID) {
			return usageCapture{}, false
		}
		declared[model.ID] = true
	}
	for modelID := range configured.ModelOverrides {
		if !declared[modelID] && !isDirect(modelID) {
			return usageCapture{}, false
		}
	}
	// The direct route excludes keyless/custom policies, and built-in model
	// metadata uses the provider's API key rather than a model-specific one.
	keyInput, allowMissing := apiKeyPolicy(id, config.ModelMeta{}, configured)
	return usageCapture{
		providerID: id, models: r.models, authPath: filepath.Join(r.agentDir, "auth.json"),
		keyInput: keyInput, allowMissing: allowMissing, checkPlan: r.manualUsagePlanCheck(), checkBalance: r.usageBalanceCheck,
	}, true
}

func (r *runtime) usageAccountCandidates(shown string) []usageCapture {
	var candidates []usageCapture
	add := func(id string) {
		if !usageSupported(id) {
			return
		}
		if id == r.ov.Provider {
			if capture, _, ok := r.usageActiveCapture(id); ok {
				candidates = append(candidates, capture)
			}
		} else if capture, ok := r.usageOtherCapture(id); ok {
			candidates = append(candidates, capture)
		}
	}
	add(shown)
	for _, id := range usageProviders {
		if id != shown {
			add(id)
		}
	}
	return candidates
}

// discoverUsageAccounts only checks local credentials. Its result does not
// carry key material, fingerprints, or credential sources to the editor.
func discoverUsageAccounts(ctx context.Context, candidates []usageCapture) ([]string, usageReason) {
	var accounts []string
	for _, capture := range candidates {
		_, _, reason := checkedUsageKey(ctx, capture)
		switch reason {
		case "":
			accounts = append(accounts, capture.providerID)
		case usageReasonTimeout, usageReasonAuthBusy:
			return nil, reason
		}
	}
	if ctx.Err() != nil {
		return nil, usageReasonTimeout
	}
	return accounts, ""
}

// usageView holds the last detail while Accounts discovery owns the slot.
// Both the detail rows and choices reuse the same editor region.
type usageView struct {
	usagePanel
	providerID string
	detail     []usageRow
	report     usageReport
	capture    usageCapture
	picker     *terminal.ChoicePicker
	accounts   []string
}

func (v *usageView) setDetail(id string, rows []usageRow) {
	*v = usageView{providerID: id, detail: rows, usagePanel: usagePanel{rows: rows}}
}

func (v *usageView) showReport(capture usageCapture, report usageReport) {
	v.setDetail(capture.providerID, report.panelRows())
	if report.usable() {
		v.report, v.capture = report, capture
	}
}

func (v *usageView) showAccountStatus(text string, token terminal.ThemeToken) {
	v.accounts, v.picker = nil, nil
	// Insert the status below the title so it is visible even in a short
	// editor, while leaving the previous detail available to scroll.
	v.rows = append([]usageRow{v.detail[0], {text: text, token: token}}, v.detail[1:]...)
	v.off = 0
}

func (v *usageView) showAccounts(ids []string) {
	if len(ids) == 0 || (len(ids) == 1 && ids[0] == v.providerID) {
		v.showAccountStatus("No other accounts available.", terminal.TokenMuted)
		return
	}
	v.accounts = ids
	v.picker = &terminal.ChoicePicker{Title: "Accounts", Hint: "↑↓ navigate · Enter select · Esc close"}
	for _, id := range ids {
		v.picker.Options = append(v.picker.Options, terminal.ChoiceOption{Label: usageProviderName(id), Current: id == v.providerID})
	}
}

func (v *usageView) view(width, budget int, noColor bool, palette *theme.Theme) []string {
	if v.picker != nil {
		return v.picker.BoundedRows(width, budget, noColor, palette)
	}
	return v.usagePanel.view(width, budget, noColor, palette)
}

func (v *usageView) move(delta int) {
	if v.picker != nil {
		v.picker.Move(delta)
	} else {
		v.usagePanel.move(delta)
	}
}

func recheckUsageDetail(ctx context.Context, capture usageCapture, report usageReport) usageReport {
	_, keySHA, reason := checkedUsageKey(ctx, capture)
	if reason == "" && keySHA != report.keySHA {
		reason = usageReasonChangedKey
	}
	if reason != "" {
		return usageReport{providerID: capture.providerID, reason: reason}
	}
	return report
}
