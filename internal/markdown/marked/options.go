package marked

// MarkedOptions is marked's `MarkedOptions`, ported from
// marked/src/MarkedOptions.ts and marked/src/defaults.ts.
//
// The boolean options are pointers, because marked merges options with a
// spread: an option that is not given keeps the value of the defaults, and
// an option that is given as false turns the default off.
type MarkedOptions struct {
	// Async is marked's `async` option.
	Async *bool
	// Breaks enables GFM line breaks. It requires GFM.
	Breaks *bool
	// GFM enables GitHub flavored markdown.
	GFM *bool
	// Pedantic conforms to obscure parts of markdown.pl.
	Pedantic *bool
	// Silent shows an error message when parsing fails instead of panicking.
	Silent *bool
	// Extensions holds the tokenizer extensions.
	Extensions *Extensions
	// Hooks holds the parsing hooks.
	Hooks *Hooks
	// Tokenizer holds the tokenizer of the parser.
	Tokenizer *Tokenizer
	// WalkTokens is called with every token.
	WalkTokens func(token *Token)
}

// Bool returns a pointer to a boolean, for the options of marked.
func Bool(value bool) *bool {
	return &value
}

// AsyncEnabled reports whether the async option is on.
func (o *MarkedOptions) AsyncEnabled() bool { return o.Async != nil && *o.Async }

// BreaksEnabled reports whether the breaks option is on.
func (o *MarkedOptions) BreaksEnabled() bool { return o.Breaks != nil && *o.Breaks }

// GFMEnabled reports whether the gfm option is on.
func (o *MarkedOptions) GFMEnabled() bool { return o.GFM != nil && *o.GFM }

// PedanticEnabled reports whether the pedantic option is on.
func (o *MarkedOptions) PedanticEnabled() bool { return o.Pedantic != nil && *o.Pedantic }

// SilentEnabled reports whether the silent option is on.
func (o *MarkedOptions) SilentEnabled() bool { return o.Silent != nil && *o.Silent }

// Extensions is marked's `extensions` option.
type Extensions struct {
	// Renderers holds the renderer extensions.
	Renderers map[string]RendererExtension
	// ChildTokens holds the child token names of the extensions.
	ChildTokens map[string][]string
	// Inline holds the inline tokenizer extensions.
	Inline []TokenizerExtension
	// Block holds the block tokenizer extensions.
	Block []TokenizerExtension
	// StartInline holds the functions that report where an inline extension
	// starts.
	StartInline []TokenizerStart
	// StartBlock holds the functions that report where a block extension
	// starts.
	StartBlock []TokenizerStart
}

// TokenizerExtension is marked's `TokenizerExtensionFunction`.
type TokenizerExtension func(lexer *Lexer, src string, tokens []Token) *Token

// TokenizerStart is marked's `TokenizerStartFunction`. It returns the offset
// of the extension in the source, or -1 when the extension does not start
// there.
type TokenizerStart func(lexer *Lexer, src string) int

// RendererExtension is marked's `RendererExtensionFunction`. It returns the
// rendered token and reports whether it rendered it. A renderer that reports
// false lets the built in renderer handle the token.
type RendererExtension func(token *Token) (string, bool)

// Hooks is marked's `Hooks`. Only the hooks the port needs are kept.
type Hooks struct {
	// EmStrongMask is called with the masked source before the em and strong
	// tokens are scanned.
	EmStrongMask func(src string) string
}

// DefaultOptions returns marked's default options.
func DefaultOptions() *MarkedOptions {
	return &MarkedOptions{
		Async:    Bool(false),
		Breaks:   Bool(false),
		GFM:      Bool(true),
		Pedantic: Bool(false),
		Silent:   Bool(false),
	}
}
