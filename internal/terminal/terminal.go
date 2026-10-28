// Package terminal implements psi's inline, Pi-regular-mode subset.
//
// It uses native scrollback, horizontal editor borders, a persistent compact
// footer, and a small internal semantic style table. There is no alternate
// screen, mouse, overlay, image protocol, or user theme engine. Assistant text
// remains plain Markdown source in this implementation slice; tool status
// contains no arguments, bodies, or diffs. Reasoning stays hidden by default.
package terminal

// Mode is always inline in the first release.
const Mode = "inline"

// Key bindings (fixed, no Kitty protocol, no Shift+Enter detection).
const (
	KeyEnter     = '\r'
	KeyCtrlJ     = '\n' // reliable legacy newline binding
	KeyCtrlC     = 3
	KeyCtrlD     = 4
	KeyBackspace = 127
)
