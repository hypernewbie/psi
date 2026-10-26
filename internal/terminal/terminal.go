// Package terminal implements the simple inline terminal (Phase 10).
//
// Main screen only, native scrollback, no alternate screen, no mouse, no
// overlays, no image protocol, no theme engine. Assistant text is plain
// Markdown source; tool status is compact (name + state) with no arguments,
// bodies, or diffs. Reasoning hidden by default. See COMPATIBILITY.md.
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
