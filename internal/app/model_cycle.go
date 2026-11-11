package app

import (
	"fmt"
)

// cycleModel follows Pi's Ctrl+P model cycle over the currently usable model
// list. Model changes are session-local and remain in the session timeline.
func (r *runtime) cycleModel(direction int) (string, error) {
	models := r.usableModels()
	current := -1
	for i, model := range models {
		if model.provider == r.ov.Provider && model.option.Model.ID == r.ov.Model {
			current = i
			break
		}
	}
	if len(models) == 0 || (len(models) == 1 && current != -1) {
		return "Only one model available", nil
	}
	if current == -1 {
		if direction < 0 {
			current = len(models) - 1
		} else {
			current = 0
		}
	} else if direction < 0 {
		current = (current - 1 + len(models)) % len(models)
	} else {
		current = (current + 1) % len(models)
	}
	nextModel := models[current]
	next := r.ov
	next.Provider, next.Model = nextModel.provider, nextModel.option.Model.ID
	next.Thinking = r.thinkingForModelSwitch()

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
