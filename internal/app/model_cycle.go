package app

import (
	"fmt"

	"psi/internal/provider"
)

// cycleModel follows Pi's Ctrl+P model cycle over the currently usable model
// list. Model changes are session-local and remain in the session timeline.
func (r *runtime) cycleModel(direction int) (string, error) {
	models := r.usableModels()
	if len(models) <= 1 {
		return "Only one model available", nil
	}

	current := -1
	for i, model := range models {
		if model.provider == r.ov.Provider && model.option.Model.ID == r.ov.Model {
			current = i
			break
		}
	}
	if current < 0 {
		current = 0
	}
	if direction < 0 {
		current = (current - 1 + len(models)) % len(models)
	} else {
		current = (current + 1) % len(models)
	}
	nextModel := models[current]
	next := r.ov
	next.Provider, next.Model = nextModel.provider, nextModel.option.Model.ID
	if !provider.SupportsThinking(providerModel(next.Provider, nextModel.option.Model), next.Thinking) {
		next.Thinking = "off"
	}

	candidate := *r
	candidate.prov = nil
	if err := candidate.configure(next); err != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return "", err
	}
	if _, err := r.store.AppendModelChange(candidate.ov.Provider, candidate.ov.Model); err != nil {
		if candidate.prov != nil {
			candidate.prov.CloseIdle()
		}
		return "", err
	}
	r.installConfiguration(&candidate)
	if r.ac != nil {
		r.ac.ResetGrowth()
	}

	name := nextModel.option.Model.Name
	if name == "" {
		name = nextModel.option.Model.ID
	}
	status := "Switched to " + name
	if r.cfg.Model.Reasoning && r.ov.Thinking != "off" {
		status += fmt.Sprintf(" (thinking: %s)", r.ov.Thinking)
	}
	return status, nil
}
