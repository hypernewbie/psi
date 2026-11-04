package marked

// MarkedOptions is marked's `MarkedOptions`, ported from
// marked/src/MarkedOptions.ts and marked/src/defaults.ts.
type MarkedOptions struct {
	// Async is marked's `async` option.
	Async bool
	// Breaks enables GFM line breaks. It requires GFM.
	Breaks bool
	// GFM enables GitHub flavored markdown.
	GFM bool
	// Pedantic conforms to obscure parts of markdown.pl.
	Pedantic bool
	// Silent shows an error message when parsing fails instead of panicking.
	Silent bool
	// Extensions holds the tokenizer extensions.
	Extensions *Extensions
	// Hooks holds the parsing hooks.
	Hooks *Hooks
	// WalkTokens is called with every token.
	WalkTokens func(token *Token)
}

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
		Async:    false,
		Breaks:   false,
		GFM:      true,
		Pedantic: false,
		Silent:   false,
	}
}
