package event

// ToolSummary is the bounded, presentation-only portion of a tool call. It
// never contains arguments, tool output, or an unredacted command.
type ToolSummary struct {
	Kind      string
	Path      string
	LineRange string
	Command   string
	Note      string
	Truncated bool
	Available bool
	ExitCode  *int
	// Web optionally carries the bounded card data for a web tool. It stays
	// nil for other tools and for web rows restored from sessions that have no
	// display metadata.
	Web *WebDisplay
}
