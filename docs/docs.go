// Package docs embeds psi's documentation so the binary can print it without a
// checkout and the system prompt can list its topics.
package docs

import (
	"embed"
	"strings"
)

//go:embed *.md
var files embed.FS

// Topic is one document served by `psi --docs`.
type Topic struct {
	Name    string
	Summary string
}

// Topics is the ordered list of served documents. Markdown files in this
// directory that are not listed here (QA reports, acceptance suites) stay out
// of `--docs`.
var Topics = []Topic{
	{Name: "index", Summary: "Map of the psi documentation"},
	{Name: "quickstart", Summary: "First run: provider setup and a first prompt"},
	{Name: "usage", Summary: "Use psi in the terminal: prompts, steering, commands"},
	{Name: "cli", Summary: "Every command-line flag and what psi rejects"},
	{Name: "config", Summary: "settings.json, models.json, and precedence"},
	{Name: "providers", Summary: "Supported providers, credentials, and models"},
	{Name: "sessions", Summary: "Session files, resume, clone, and storage"},
	{Name: "compaction", Summary: "Automatic and manual context compaction"},
	{Name: "skills", Summary: "On-demand instruction files and /skill commands"},
	{Name: "security", Summary: "Project trust, permissions, and what leaves the process"},
	{Name: "env", Summary: "Provider keys and directory or shell overrides"},
	{Name: "how-psi-works", Summary: "Agent loop, context assembly, and sessions"},
	{Name: "keyboard", Summary: "Keys, prefixes, and slash commands"},
	{Name: "limitations", Summary: "Unsupported Pi features and known gaps"},
	{Name: "recovery", Summary: "Recovery after an interruption or crash"},
	{Name: "bridge", Summary: "Phi bridge protocol and capabilities"},
}

// Get returns one topic body. Only names listed in Topics resolve.
func Get(name string) (string, bool) {
	for _, topic := range Topics {
		if topic.Name != name {
			continue
		}
		body, err := files.ReadFile(name + ".md")
		if err != nil {
			return "", false
		}
		return string(body), true
	}
	return "", false
}

// List returns one "<name> - <summary>" line per topic, newline terminated.
func List() string {
	var sb strings.Builder
	for _, topic := range Topics {
		sb.WriteString(topic.Name + " - " + topic.Summary + "\n")
	}
	return sb.String()
}
