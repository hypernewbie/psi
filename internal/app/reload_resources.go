package app

import (
	"path/filepath"

	"psi/internal/config"
	"psi/internal/skills"
)

// reloadResources refreshes the prompt inputs and skill catalog without
// changing session identity, model selection, or project trust. Prepare the
// new values before installing them so a read failure leaves the old prompt.
func (r *runtime) reloadResources() error {
	cwd := r.store.Cwd()
	loaded, err := config.LoadSettings(filepath.Join(r.agentDir, "settings.json"), filepath.Join(cwd, ".pi", "settings.json"), r.projectTrusted)
	if err != nil {
		return err
	}
	var instructions []config.InstructionFile
	if !r.options.NoContextFiles {
		var err error
		instructions, err = config.DiscoverInstructions(cwd, r.agentDir, r.projectTrusted)
		if err != nil {
			return err
		}
	}
	var sk []skills.Skill
	if !r.options.NoSkills {
		sk, _ = skills.Discover(cwd, r.agentDir, loaded.Settings.Skills, r.projectTrusted)
	}
	var appendPrompts []string
	for _, source := range r.options.AppendSystemPrompts {
		appendPrompts = append(appendPrompts, resolveCLIPrompt(source))
	}
	sections := config.BuildPromptSections(cwd, instructions, []string{skills.FormatForPrompt(sk)}, r.settings.DefaultTools, resolveCLIPrompt(r.options.SystemPrompt), r.options.SystemPrompt != "", appendPrompts)
	prompt := config.RenderSections(sections)
	r.skills, r.prompt, r.sections = sk, prompt, sections
	r.settings.Skills = loaded.Settings.Skills
	r.cfg.System = prompt
	r.cfg.Sections = sections
	r.syncBridge()
	return nil
}
