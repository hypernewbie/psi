package app

import (
	"context"
	"path/filepath"
	"slices"
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

// usageView keeps the report and account choices in the same editor region.
type usageView struct {
	usagePanel
	providerID       string
	picker           *terminal.ChoicePicker
	accounts         []string
	accountRow       usageRow
	oneRowDetail     bool // Give detail the row after paging or confirming a choice.
	selectionVisible bool // Only a choice rendered in the last frame can be confirmed.
}

func (v *usageView) setDetail(id string, rows []usageRow) {
	v.providerID = id
	v.usagePanel = usagePanel{rows: rows}
	if v.picker != nil {
		for i, account := range v.accounts {
			v.picker.Options[i].Current = account == id
		}
	}
}

func (v *usageView) showReport(report usageReport) {
	v.setDetail(report.providerID, report.panelRows())
}

func (v *usageView) showAccountStatus(text string, token terminal.ThemeToken) {
	v.accountRow = usageRow{text: "Accounts: " + text, token: token}
}

func (v *usageView) showAccounts(ids []string) {
	v.accounts, v.picker = ids, nil
	if len(ids) == 0 {
		v.showAccountStatus("no eligible accounts · Esc close", terminal.TokenMuted)
		return
	}
	v.picker = &terminal.ChoicePicker{Title: "Accounts", Hint: "↑↓ highlight · Enter load · PgUp/PgDn scroll · Esc close"}
	for i, id := range ids {
		current := id == v.providerID
		if current {
			v.picker.Selected = i
		}
		v.picker.Options = append(v.picker.Options, terminal.ChoiceOption{Label: usageProviderName(id), Current: current})
	}
}

func (v *usageView) removeAccount(id string) {
	if i := slices.Index(v.accounts, id); i >= 0 {
		v.showAccounts(slices.Delete(v.accounts, i, i+1))
	}
}

func (v *usageView) selectedAccount() string {
	if !v.selectionVisible {
		return ""
	}
	if _, ok := v.picker.SelectedOption(); ok {
		return v.accounts[v.picker.Selected]
	}
	return ""
}

func (v *usageView) view(width, budget int, noColor bool, palette *theme.Theme) []string {
	v.selectionVisible = false
	if v.picker != nil {
		if budget == 1 && v.oneRowDetail {
			return v.usagePanel.view(width, 1, noColor, palette)
		}
		v.selectionVisible = true
		// Keep up to two detail rows before investing in more choice rows.
		// BoundedRows always keeps the selection in the remaining space.
		choices := v.picker.BoundedRows(width, max(1, budget-2), noColor, palette)
		if len(choices) >= budget {
			return choices
		}
		return append(v.usagePanel.view(width, budget-len(choices), noColor, palette), choices...)
	}
	if v.accountRow.text != "" && budget > 1 {
		detail := v.usagePanel.view(width, budget-1, noColor, palette)
		return append(detail, formatUsageRow(v.accountRow, width, noColor, palette))
	}
	return v.usagePanel.view(width, budget, noColor, palette)
}

func (v *usageView) moveAccount(delta int) {
	v.oneRowDetail = false
	v.picker.Move(delta)
}

func (v *usageView) pageDetail(direction int) {
	v.oneRowDetail = true
	v.usagePanel.move(direction * max(1, v.limit))
}
