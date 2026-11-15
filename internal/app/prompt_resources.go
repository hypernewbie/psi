package app

import (
	"psi/internal/config"
	"psi/internal/skills"
)

func effectivePromptLanguage(o Options, st config.Settings) string {
	if o.Language != "" {
		return o.Language
	}
	if st.Language != "" {
		return st.Language
	}
	return config.LanguageEnglish
}

// preparedPromptResources is the outcome of one prompt discovery and assembly
// pass. Nothing here mutates runtime state, so callers can install it only
// after every required read succeeds.
type preparedPromptResources struct {
	prompt      string
	sections    []config.PromptSection
	skills      []skills.Skill
	diagnostics []skills.Diagnostic
}

func preparePromptResources(o Options, cwd, agentDir string, st config.Settings, consent bool) (preparedPromptResources, error) {
	var instructions []config.InstructionFile
	var err error
	if !o.NoContextFiles {
		instructions, err = config.DiscoverInstructions(cwd, agentDir, consent)
		if err != nil {
			return preparedPromptResources{}, err
		}
	}
	var sk []skills.Skill
	var diagnostics []skills.Diagnostic
	if !o.NoSkills {
		sk, diagnostics = skills.Discover(cwd, agentDir, st.Skills, consent)
	}
	var appendSystemPrompts []string
	for _, source := range o.AppendSystemPrompts {
		appendSystemPrompts = append(appendSystemPrompts, resolveCLIPrompt(source))
	}
	language := effectivePromptLanguage(o, st)
	sections := config.BuildPromptSections(cwd, instructions, []string{skills.FormatForPrompt(sk, language)}, st.DefaultTools, resolveCLIPrompt(o.SystemPrompt), o.SystemPrompt != "", appendSystemPrompts, language)
	return preparedPromptResources{
		prompt:      config.RenderSections(sections),
		sections:    sections,
		skills:      sk,
		diagnostics: diagnostics,
	}, nil
}
