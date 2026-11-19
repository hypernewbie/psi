package marked

// Marked instance, ported from marked/src/Instance.ts.

// Marked compiles markdown to HTML. It holds the defaults of a parser and the
// extensions that were registered on it, like marked's `Marked` class.
type Marked struct {
	// Defaults holds the options of the instance.
	Defaults *MarkedOptions
}

// New returns a marked instance, like marked's `Marked` constructor.
func New(packs ...MarkedExtension) *Marked {
	instance := &Marked{Defaults: DefaultOptions()}
	instance.Use(packs...)
	return instance
}

// MarkedExtension is marked's `MarkedExtension`. It holds the options of the
// pack and the extensions that the pack registers.
type MarkedExtension struct {
	// Options holds the options of the pack, like marked's `MarkedOptions`.
	Options *MarkedOptions
	// Extensions holds the addon extensions.
	Extensions []Extension
	// Tokenizer holds the tokenizer methods that the extension replaces.
	Tokenizer *TokenizerOverrides
	// Renderer holds the renderer methods that the extension replaces.
	Renderer *RendererOverrides
	// Hooks holds the hooks that the extension replaces.
	Hooks *Hooks
	// WalkTokens is called with every token.
	WalkTokens func(token *Token)
}

// Extension is marked's extension object. It holds a tokenizer, a renderer
// and a start function.
type Extension struct {
	// Name is the name of the extension.
	Name string
	// Level is "block" or "inline".
	Level string
	// Tokenizer is the tokenizer of the extension.
	Tokenizer TokenizerExtension
	// Renderer is the renderer of the extension.
	Renderer RendererExtension
	// Start reports where the extension starts in the source.
	Start TokenizerStart
	// ChildTokens holds the names of the child tokens of the extension.
	ChildTokens []string
}

// RendererOverrides holds the renderer methods that an extension replaced.
type RendererOverrides struct {
	// Space renders a space token.
	Space func(r *Renderer, token *Token) string
	// Code renders a code block.
	Code func(r *Renderer, token *Token) string
	// Blockquote renders a block quote.
	Blockquote func(r *Renderer, token *Token) string
	// HTML renders a html token.
	HTML func(r *Renderer, token *Token) string
	// Def renders a link definition.
	Def func(r *Renderer, token *Token) string
	// Heading renders a heading.
	Heading func(r *Renderer, token *Token) string
	// HR renders a horizontal rule.
	HR func(r *Renderer, token *Token) string
	// List renders a list.
	List func(r *Renderer, token *Token) string
	// ListItem renders one item of a list.
	ListItem func(r *Renderer, item *ListItem) string
	// Checkbox renders the checkbox of a task list item.
	Checkbox func(r *Renderer, token *Token) string
	// Paragraph renders a paragraph.
	Paragraph func(r *Renderer, token *Token) string
	// Table renders a table.
	Table func(r *Renderer, token *Token) string
	// TableRow renders one row of a table.
	TableRow func(r *Renderer, text string) string
	// TableCell renders one cell of a table.
	TableCell func(r *Renderer, cell *TableCell) string
	// Strong renders a strong span.
	Strong func(r *Renderer, token *Token) string
	// Em renders an emphasis span.
	Em func(r *Renderer, token *Token) string
	// Codespan renders a code span.
	Codespan func(r *Renderer, token *Token) string
	// BR renders a line break.
	BR func(r *Renderer, token *Token) string
	// Del renders a strikethrough span.
	Del func(r *Renderer, token *Token) string
	// Link renders a link.
	Link func(r *Renderer, token *Token) string
	// Image renders an image.
	Image func(r *Renderer, token *Token) string
	// Text renders a text token.
	Text func(r *Renderer, token *Token) string
}

// SetOptions sets the options of the instance, like marked's `setOptions`.
func (m *Marked) SetOptions(options *MarkedOptions) *Marked {
	m.Defaults = mergeOptions(m.Defaults, options)
	return m
}

// Use registers extensions with the instance, like marked's `use`.
func (m *Marked) Use(packs ...MarkedExtension) *Marked {
	for _, pack := range packs {
		opts := pack.Options
		if opts == nil {
			opts = &MarkedOptions{}
		}

		// ==-- Parse "addon" extensions --== //
		if len(pack.Extensions) > 0 {
			extensions := m.Defaults.Extensions
			if extensions == nil {
				extensions = &Extensions{
					Renderers:   map[string]RendererExtension{},
					ChildTokens: map[string][]string{},
				}
			}
			if extensions.Renderers == nil {
				extensions.Renderers = map[string]RendererExtension{}
			}
			if extensions.ChildTokens == nil {
				extensions.ChildTokens = map[string][]string{}
			}
			for _, ext := range pack.Extensions {
				if ext.Renderer != nil {
					previous := extensions.Renderers[ext.Name]
					if previous != nil {
						// Replace extension with func to run new extension but
						// fall back if false
						current := ext.Renderer
						extensions.Renderers[ext.Name] = func(token *Token) (string, bool) {
							ret, handled := current(token)
							if handled {
								return ret, true
							}
							return previous(token)
						}
					} else {
						extensions.Renderers[ext.Name] = ext.Renderer
					}
				}
				if ext.Tokenizer != nil {
					switch ext.Level {
					case "block":
						extensions.Block = append([]TokenizerExtension{ext.Tokenizer}, extensions.Block...)
					case "inline":
						extensions.Inline = append([]TokenizerExtension{ext.Tokenizer}, extensions.Inline...)
					}
					if ext.Start != nil {
						if ext.Level == "block" {
							extensions.StartBlock = append(extensions.StartBlock, ext.Start)
						} else {
							extensions.StartInline = append(extensions.StartInline, ext.Start)
						}
					}
				}
				if ext.ChildTokens != nil {
					extensions.ChildTokens[ext.Name] = ext.ChildTokens
				}
			}
			opts = mergeOptions(opts, &MarkedOptions{Extensions: extensions})
		}
		// ==-- Parse "overwrite" extensions --== //
		if pack.Tokenizer != nil {
			tokenizer := m.Defaults.Tokenizer
			if tokenizer == nil {
				tokenizer = newTokenizer(m.Defaults)
			}
			if tokenizer.Overrides == nil {
				tokenizer.Overrides = &TokenizerOverrides{}
			}
			if pack.Tokenizer.EmStrong != nil {
				previous := tokenizer.Overrides.EmStrong
				current := pack.Tokenizer.EmStrong
				tokenizer.Overrides.EmStrong = func(t *Tokenizer, src, maskedSrc, prevChar string) (*Token, bool) {
					token, handled := current(t, src, maskedSrc, prevChar)
					if handled {
						return token, true
					}
					if previous != nil {
						return previous(t, src, maskedSrc, prevChar)
					}
					return nil, false
				}
			}
			if pack.Tokenizer.Del != nil {
				previous := tokenizer.Overrides.Del
				current := pack.Tokenizer.Del
				tokenizer.Overrides.Del = func(t *Tokenizer, src, maskedSrc, prevChar string) (*Token, bool) {
					token, handled := current(t, src, maskedSrc, prevChar)
					if handled {
						return token, true
					}
					if previous != nil {
						return previous(t, src, maskedSrc, prevChar)
					}
					return nil, false
				}
			}
			opts = mergeOptions(opts, &MarkedOptions{Tokenizer: tokenizer})
		}

		// ==-- Parse Hooks extensions --== //
		if pack.Hooks != nil {
			hooks := m.Defaults.Hooks
			if hooks == nil {
				hooks = &Hooks{}
			}
			if pack.Hooks.EmStrongMask != nil {
				previous := hooks.EmStrongMask
				current := pack.Hooks.EmStrongMask
				hooks.EmStrongMask = func(src string) string {
					masked := current(src)
					if previous != nil {
						return previous(masked)
					}
					return masked
				}
			}
			opts = mergeOptions(opts, &MarkedOptions{Hooks: hooks})
		}

		// ==-- Parse WalkTokens extensions --== //
		if pack.WalkTokens != nil {
			previous := m.Defaults.WalkTokens
			current := pack.WalkTokens
			opts = mergeOptions(opts, &MarkedOptions{WalkTokens: func(token *Token) {
				current(token)
				if previous != nil {
					previous(token)
				}
			}})
		}

		m.Defaults = mergeOptions(m.Defaults, opts)
	}

	return m
}

// Lexer parses a markdown document into tokens, like marked's `lexer`.
func (m *Marked) Lexer(src string, options *MarkedOptions) []Token {
	opt := mergeOptions(m.Defaults, options)
	lexer := NewLexer(opt)
	return lexer.Lex(src)
}

// Parser renders tokens as HTML, like marked's `parser`.
func (m *Marked) Parser(tokens []Token, options *MarkedOptions) string {
	return ParseTokens(tokens, mergeOptions(m.Defaults, options))
}

// Parse compiles markdown to HTML, like marked's `parse`.
func (m *Marked) Parse(src string, options *MarkedOptions) string {
	opt := mergeOptions(m.Defaults, options)
	tokens := m.Lexer(src, opt)
	if opt.WalkTokens != nil {
		WalkTokens(tokens, opt.WalkTokens)
	}
	return ParseTokens(tokens, opt)
}

// ParseInline compiles inline markdown to HTML, like marked's `parseInline`.
func (m *Marked) ParseInline(src string, options *MarkedOptions) string {
	opt := mergeOptions(m.Defaults, options)
	lexer := NewLexer(opt)
	tokens := lexer.inlineTokens(src, nil)
	if opt.WalkTokens != nil {
		WalkTokens(tokens, opt.WalkTokens)
	}
	return ParseInlineTokens(tokens, opt)
}

// WalkTokens calls the walk function with every token, like marked's
// `walkTokens`.
func WalkTokens(tokens []Token, walk func(*Token)) {
	for i := range tokens {
		walk(&tokens[i])
		switch tokens[i].Type {
		case TypeTable:
			for j := range tokens[i].Header {
				WalkTokens(tokens[i].Header[j].Tokens, walk)
			}
			for j := range tokens[i].Rows {
				for k := range tokens[i].Rows[j] {
					WalkTokens(tokens[i].Rows[j][k].Tokens, walk)
				}
			}
		case TypeList:
			for j := range tokens[i].Items {
				WalkTokens(tokens[i].Items[j].Tokens, walk)
			}
		default:
			WalkTokens(tokens[i].Tokens, walk)
		}
	}
}

// mergeOptions merges the given options into the defaults, like marked's
// `{ ...this.defaults, ...options }`. An option that is not given keeps the
// value of the defaults.
func mergeOptions(defaults *MarkedOptions, options *MarkedOptions) *MarkedOptions {
	if defaults == nil {
		defaults = DefaultOptions()
	}
	if options == nil {
		return defaults
	}
	merged := *defaults
	if options.Async != nil {
		merged.Async = options.Async
	}
	if options.Breaks != nil {
		merged.Breaks = options.Breaks
	}
	if options.GFM != nil {
		merged.GFM = options.GFM
	}
	if options.Pedantic != nil {
		merged.Pedantic = options.Pedantic
	}
	if options.Silent != nil {
		merged.Silent = options.Silent
	}
	if options.Extensions != nil {
		merged.Extensions = options.Extensions
	}
	if options.Hooks != nil {
		merged.Hooks = options.Hooks
	}
	if options.Tokenizer != nil {
		merged.Tokenizer = options.Tokenizer
	}
	if options.WalkTokens != nil {
		merged.WalkTokens = options.WalkTokens
	}
	return &merged
}
