package app

import (
	"path/filepath"

	"github.com/hypernewbie/psi/internal/config"
)

// reloadResources refreshes the prompt inputs, skill catalog, and queue modes
// without changing session identity, model selection, or project trust. Prepare
// the new values before installing them so a read failure leaves the old prompt
// and the old queue modes.
func (r *runtime) reloadResources() error {
	cwd := r.store.Cwd()
	consent := r.projectTrust.allowsResources()
	loaded, err := config.LoadSettings(filepath.Join(r.agentDir, "settings.json"), filepath.Join(cwd, ".pi", "settings.json"), consent)
	if err != nil {
		return err
	}
	candidate := loaded.Settings
	// The active run keeps its tool loadout; reload only refreshes files and
	// the settings that new loops read.
	candidate.DefaultTools = r.settings.DefaultTools
	prepared, err := preparePromptResources(r.options, cwd, r.agentDir, candidate, promptToolNames(candidate.DefaultTools, r.web), r.projectTrust.allowsResources())
	if err != nil {
		return err
	}
	r.skills, r.prompt, r.sections = prepared.skills, prepared.prompt, prepared.sections
	r.settings.Language = candidate.Language
	r.settings.Skills = candidate.Skills
	// newLoop applies these to every loop built after the reload.
	r.settings.SteeringMode = candidate.SteeringMode
	r.settings.FollowUpMode = candidate.FollowUpMode
	r.settings.MCPAllowProjectCommands = candidate.MCPAllowProjectCommands
	r.cfg.System = prepared.prompt
	r.cfg.Sections = prepared.sections
	r.syncBridge()
	r.reloadMCP()
	return nil
}

// reloadModels re-reads models.json and installs the result. A malformed file
// keeps the current model configuration, so the session stays usable and the
// caller reports the error.
func (r *runtime) reloadModels() error {
	models, err := config.LoadModels(filepath.Join(r.agentDir, "models.json"))
	if err != nil {
		return err
	}
	r.models = models
	if r.live != nil {
		r.live.publishModels(models)
	}
	return nil
}
