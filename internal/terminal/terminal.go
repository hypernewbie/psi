// Package terminal implements psi's inline, Pi-regular-mode subset.
//
// It uses native scrollback, horizontal editor borders, a persistent compact
// footer, and semantic styles from the selected Pi-compatible theme. There is
// no alternate screen, mouse, overlay, or image protocol. Messages use a narrow
// CommonMark subset; tool status contains no arguments, bodies, or diffs.
// Reasoning stays hidden by default.
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
