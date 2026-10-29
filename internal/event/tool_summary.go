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
}
