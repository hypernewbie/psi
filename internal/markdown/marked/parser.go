package marked

import (
	"strings"
)

// Parser, ported from marked/src/Parser.ts.

// Parser renders the tokens of a document with a renderer.
type Parser struct {
	options      *MarkedOptions
	renderer     *Renderer
	textRenderer *TextRenderer
}

// NewParser returns a parser for the given options.
func NewParser(options *MarkedOptions) *Parser {
	if options == nil {
		options = DefaultOptions()
	}
	parser := &Parser{options: options}
	parser.renderer = NewRenderer(options)
	parser.renderer.parser = parser
	parser.textRenderer = NewTextRenderer(options)
	parser.textRenderer.parser = parser
	return parser
}

// Renderer returns the renderer of the parser.
func (p *Parser) Renderer() *Renderer {
	return p.renderer
}

// TextRenderer returns the text renderer of the parser.
func (p *Parser) TextRenderer() *TextRenderer {
	return p.textRenderer
}

// Parse renders a list of block tokens.
func (p *Parser) Parse(tokens []Token) string {
	p.renderer.parser = p
	var out strings.Builder

	for i := range tokens {
		token := &tokens[i]

		// Run any renderer extensions
		if p.options.Extensions != nil {
			if extension, ok := p.options.Extensions.Renderers[token.Type]; ok {
				ret, handled := extension(token)
				if !handled || !blockRenderableTypes[token.Type] {
					if ret != "" {
						out.WriteString(ret)
					}
					continue
				}
			}
		}

		switch token.Type {
		case TypeSpace:
			out.WriteString(p.renderer.Space(token))
		case TypeHR:
			out.WriteString(p.renderer.HR(token))
		case TypeHeading:
			out.WriteString(p.renderer.Heading(token))
		case TypeCode:
			out.WriteString(p.renderer.Code(token))
		case TypeTable:
			out.WriteString(p.renderer.Table(token))
		case TypeBlockquote:
			out.WriteString(p.renderer.Blockquote(token))
		case TypeList:
			out.WriteString(p.renderer.List(token))
		case TypeCheckbox:
			out.WriteString(p.renderer.Checkbox(token))
		case TypeHTML:
			out.WriteString(p.renderer.HTML(token))
		case TypeDef:
			out.WriteString(p.renderer.Def(token))
		case TypeParagraph:
			out.WriteString(p.renderer.Paragraph(token))
		case TypeText:
			out.WriteString(p.renderer.Text(token))
		default:
			errMsg := "Token with \"" + token.Type + "\" type was not found."
			if p.options.Silent {
				return out.String()
			}
			panic(errMsg)
		}
	}

	return out.String()
}

// ParseInline renders a list of inline tokens.
func (p *Parser) ParseInline(tokens []Token) string {
	return p.parseInline(tokens, false)
}

// ParseInlineText renders a list of inline tokens with the text renderer.
func (p *Parser) ParseInlineText(tokens []Token) string {
	return p.parseInline(tokens, true)
}

// parseInline renders a list of inline tokens.
func (p *Parser) parseInline(tokens []Token, text bool) string {
	p.renderer.parser = p
	var out strings.Builder

	for i := range tokens {
		token := &tokens[i]

		// Run any renderer extensions
		if p.options.Extensions != nil {
			if extension, ok := p.options.Extensions.Renderers[token.Type]; ok {
				ret, handled := extension(token)
				if !handled || !inlineRenderableTypes[token.Type] {
					if ret != "" {
						out.WriteString(ret)
					}
					continue
				}
			}
		}

		switch token.Type {
		case TypeEscape:
			out.WriteString(p.text(token, text))
		case TypeHTML:
			out.WriteString(p.html(token, text))
		case TypeLink:
			out.WriteString(p.link(token, text))
		case TypeImage:
			out.WriteString(p.image(token, text))
		case TypeCheckbox:
			out.WriteString(p.checkbox(token, text))
		case TypeStrong:
			out.WriteString(p.strong(token, text))
		case TypeEm:
			out.WriteString(p.em(token, text))
		case TypeCodespan:
			out.WriteString(p.codespan(token, text))
		case TypeBr:
			out.WriteString(p.br(token, text))
		case TypeDel:
			out.WriteString(p.del(token, text))
		case TypeText:
			out.WriteString(p.text(token, text))
		default:
			errMsg := "Token with \"" + token.Type + "\" type was not found."
			if p.options.Silent {
				return out.String()
			}
			panic(errMsg)
		}
	}

	return out.String()
}

// blockRenderableTypes holds the block token types that a renderer extension
// may fall back to, like marked's list in the parser.
var blockRenderableTypes = map[string]bool{
	TypeSpace:      true,
	TypeHR:         true,
	TypeHeading:    true,
	TypeCode:       true,
	TypeTable:      true,
	TypeBlockquote: true,
	TypeList:       true,
	TypeCheckbox:   true,
	TypeHTML:       true,
	TypeDef:        true,
	TypeParagraph:  true,
	TypeText:       true,
}

// inlineRenderableTypes holds the inline token types that a renderer
// extension may fall back to, like marked's list in the parser.
var inlineRenderableTypes = map[string]bool{
	TypeEscape:   true,
	TypeHTML:     true,
	TypeLink:     true,
	TypeImage:    true,
	TypeCheckbox: true,
	TypeStrong:   true,
	TypeEm:       true,
	TypeCodespan: true,
	TypeBr:       true,
	TypeDel:      true,
	TypeText:     true,
}

// text renders a text token with the html renderer or the text renderer.
func (p *Parser) text(token *Token, text bool) string {
	if text {
		return p.textRenderer.Text(token)
	}
	return p.renderer.Text(token)
}

// html renders a html token with the html renderer or the text renderer.
func (p *Parser) html(token *Token, text bool) string {
	if text {
		return p.textRenderer.HTML(token)
	}
	return p.renderer.HTML(token)
}

// link renders a link with the html renderer or the text renderer.
func (p *Parser) link(token *Token, text bool) string {
	if text {
		return p.textRenderer.Link(token)
	}
	return p.renderer.Link(token)
}

// image renders an image with the html renderer or the text renderer.
func (p *Parser) image(token *Token, text bool) string {
	if text {
		return p.textRenderer.Image(token)
	}
	return p.renderer.Image(token)
}

// checkbox renders a checkbox with the html renderer or the text renderer.
func (p *Parser) checkbox(token *Token, text bool) string {
	if text {
		return p.textRenderer.Checkbox(token)
	}
	return p.renderer.Checkbox(token)
}

// strong renders a strong span with the html renderer or the text renderer.
func (p *Parser) strong(token *Token, text bool) string {
	if text {
		return p.textRenderer.Strong(token)
	}
	return p.renderer.Strong(token)
}

// em renders an emphasis span with the html renderer or the text renderer.
func (p *Parser) em(token *Token, text bool) string {
	if text {
		return p.textRenderer.Em(token)
	}
	return p.renderer.Em(token)
}

// codespan renders a code span with the html renderer or the text renderer.
func (p *Parser) codespan(token *Token, text bool) string {
	if text {
		return p.textRenderer.Codespan(token)
	}
	return p.renderer.Codespan(token)
}

// br renders a line break with the html renderer or the text renderer.
func (p *Parser) br(token *Token, text bool) string {
	if text {
		return p.textRenderer.BR(token)
	}
	return p.renderer.BR(token)
}

// del renders a strikethrough span with the html renderer or the text
// renderer.
func (p *Parser) del(token *Token, text bool) string {
	if text {
		return p.textRenderer.Del(token)
	}
	return p.renderer.Del(token)
}

// ParseTokens renders a list of block tokens to HTML, like marked's
// `marked.parser`.
func ParseTokens(tokens []Token, options *MarkedOptions) string {
	return NewParser(options).Parse(tokens)
}

// ParseInlineTokens renders a list of inline tokens to HTML, like marked's
// `marked.parseInline`.
func ParseInlineTokens(tokens []Token, options *MarkedOptions) string {
	return NewParser(options).ParseInline(tokens)
}
