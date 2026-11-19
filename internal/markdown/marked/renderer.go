package marked

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Renderer, ported from marked/src/Renderer.ts.
//
// marked renders the tokens to HTML. psi renders them in the terminal, but the
// HTML renderer is the reference for the behaviour of the tokens, so the port
// keeps it.

// Renderer renders the tokens of a document to HTML.
type Renderer struct {
	options *MarkedOptions
	parser  *Parser
}

// NewRenderer returns a renderer for the given options.
func NewRenderer(options *MarkedOptions) *Renderer {
	if options == nil {
		options = DefaultOptions()
	}
	return &Renderer{options: options}
}

// Space renders a space token.
func (r *Renderer) Space(token *Token) string {
	return ""
}

// Code renders a code block.
func (r *Renderer) Code(token *Token) string {
	langString := ""
	if found := exec(other.NotSpaceStart, token.Lang); found != nil {
		langString = found.whole()
	}

	code := strings.TrimSuffix(token.Text, "\n") + "\n"

	if langString == "" {
		return "<pre><code>" + escapeCode(code, token.Escaped) + "</code></pre>\n"
	}

	return "<pre><code class=\"language-" + EscapeHtmlEntities(langString, false) + "\">" +
		escapeCode(code, token.Escaped) + "</code></pre>\n"
}

// escapeCode escapes the code of a code block.
func escapeCode(code string, escaped bool) string {
	if escaped {
		return code
	}
	return EscapeHtmlEntities(code, true)
}

// Blockquote renders a block quote.
func (r *Renderer) Blockquote(token *Token) string {
	body := r.parser.Parse(token.Tokens)
	return "<blockquote>\n" + body + "</blockquote>\n"
}

// HTML renders a html token.
func (r *Renderer) HTML(token *Token) string {
	return token.Text
}

// Def renders a link definition.
func (r *Renderer) Def(token *Token) string {
	return ""
}

// Heading renders a heading.
func (r *Renderer) Heading(token *Token) string {
	depth := itoa(token.Depth)
	return "<h" + depth + ">" + r.parser.ParseInline(token.Tokens) + "</h" + depth + ">\n"
}

// HR renders a horizontal rule.
func (r *Renderer) HR(token *Token) string {
	return "<hr>\n"
}

// List renders a list.
func (r *Renderer) List(token *Token) string {
	var body strings.Builder
	for i := range token.Items {
		body.WriteString(r.ListItem(&token.Items[i]))
	}

	listType := "ul"
	startAttr := ""
	if token.Ordered {
		listType = "ol"
		if token.Start != 1 {
			startAttr = " start=\"" + itoa(token.Start) + "\""
		}
	}
	return "<" + listType + startAttr + ">\n" + body.String() + "</" + listType + ">\n"
}

// ListItem renders one item of a list.
func (r *Renderer) ListItem(item *ListItem) string {
	return "<li>" + r.parser.Parse(item.Tokens) + "</li>\n"
}

// Checkbox renders the checkbox of a task list item.
func (r *Renderer) Checkbox(token *Token) string {
	out := "<input "
	if token.Checked {
		out += "checked=\"\" "
	}
	return out + "disabled=\"\" type=\"checkbox\"> "
}

// Paragraph renders a paragraph.
func (r *Renderer) Paragraph(token *Token) string {
	return "<p>" + r.parser.ParseInline(token.Tokens) + "</p>\n"
}

// Table renders a table.
func (r *Renderer) Table(token *Token) string {
	var header strings.Builder
	for i := range token.Header {
		header.WriteString(r.TableCell(&token.Header[i]))
	}

	var body strings.Builder
	for i := range token.Rows {
		var cell strings.Builder
		for j := range token.Rows[i] {
			cell.WriteString(r.TableCell(&token.Rows[i][j]))
		}
		body.WriteString(r.TableRow(cell.String()))
	}
	rows := ""
	if body.Len() > 0 {
		rows = "<tbody>" + body.String() + "</tbody>"
	}

	return "<table>\n" +
		"<thead>\n" +
		r.TableRow(header.String()) +
		"</thead>\n" +
		rows +
		"</table>\n"
}

// TableRow renders one row of a table.
func (r *Renderer) TableRow(text string) string {
	return "<tr>\n" + text + "</tr>\n"
}

// TableCell renders one cell of a table.
func (r *Renderer) TableCell(token *TableCell) string {
	content := r.parser.ParseInline(token.Tokens)
	cellType := "td"
	if token.Header {
		cellType = "th"
	}
	tag := "<" + cellType + ">"
	if token.Align != "" {
		tag = "<" + cellType + " align=\"" + token.Align + "\">"
	}
	return tag + content + "</" + cellType + ">\n"
}

// Strong renders a strong span.
func (r *Renderer) Strong(token *Token) string {
	return "<strong>" + r.parser.ParseInline(token.Tokens) + "</strong>"
}

// Em renders an emphasis span.
func (r *Renderer) Em(token *Token) string {
	return "<em>" + r.parser.ParseInline(token.Tokens) + "</em>"
}

// Codespan renders a code span.
func (r *Renderer) Codespan(token *Token) string {
	return "<code>" + EscapeHtmlEntities(token.Text, true) + "</code>"
}

// BR renders a line break.
func (r *Renderer) BR(token *Token) string {
	return "<br>"
}

// Del renders a strikethrough span.
func (r *Renderer) Del(token *Token) string {
	return "<del>" + r.parser.ParseInline(token.Tokens) + "</del>"
}

// Link renders a link.
func (r *Renderer) Link(token *Token) string {
	text := r.parser.ParseInline(token.Tokens)
	cleanHref, ok := CleanUrl(token.Href)
	if !ok {
		return text
	}
	out := "<a href=\"" + cleanHref + "\""
	if token.Title != nil && *token.Title != "" {
		out += " title=\"" + EscapeHtmlEntities(*token.Title, false) + "\""
	}
	return out + ">" + text + "</a>"
}

// Image renders an image.
func (r *Renderer) Image(token *Token) string {
	text := token.Text
	if len(token.Tokens) > 0 {
		text = r.parser.ParseInlineText(token.Tokens)
	}
	cleanHref, ok := CleanUrl(token.Href)
	if !ok {
		return EscapeHtmlEntities(text, false)
	}
	out := "<img src=\"" + cleanHref + "\" alt=\"" + EscapeHtmlEntities(text, false) + "\""
	if token.Title != nil && *token.Title != "" {
		out += " title=\"" + EscapeHtmlEntities(*token.Title, false) + "\""
	}
	return out + ">"
}

// Text renders a text token.
func (r *Renderer) Text(token *Token) string {
	if len(token.Tokens) > 0 {
		return r.parser.ParseInline(token.Tokens)
	}
	if token.Escaped {
		return token.Text
	}
	return EscapeHtmlEntities(token.Text, false)
}

// TextRenderer returns only the textual part of a token, like marked's
// `TextRenderer`.
type TextRenderer struct {
	options *MarkedOptions
	parser  *Parser
}

// NewTextRenderer returns a text renderer for the given options.
func NewTextRenderer(options *MarkedOptions) *TextRenderer {
	if options == nil {
		options = DefaultOptions()
	}
	return &TextRenderer{options: options}
}

// Strong returns the text of a strong span.
func (r *TextRenderer) Strong(token *Token) string { return token.Text }

// Em returns the text of an emphasis span.
func (r *TextRenderer) Em(token *Token) string { return token.Text }

// Codespan returns the text of a code span.
func (r *TextRenderer) Codespan(token *Token) string { return token.Text }

// Del returns the text of a strikethrough span.
func (r *TextRenderer) Del(token *Token) string { return token.Text }

// HTML returns the text of a html token.
func (r *TextRenderer) HTML(token *Token) string { return token.Text }

// Text returns the text of a text token.
func (r *TextRenderer) Text(token *Token) string { return token.Text }

// Link returns the text of a link.
func (r *TextRenderer) Link(token *Token) string { return token.Text }

// Image returns the text of an image.
func (r *TextRenderer) Image(token *Token) string { return token.Text }

// BR returns the text of a line break.
func (r *TextRenderer) BR(token *Token) string { return "" }

// Checkbox returns the raw text of a checkbox.
func (r *TextRenderer) Checkbox(token *Token) string { return token.Raw }

// encodeURIUnescaped holds the characters that JavaScript's `encodeURI` keeps.
const encodeURIUnescaped = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789;,/?:@&=+$-_.!~*'()#"

// EncodeURI escapes a string the way JavaScript's `encodeURI` does. It
// reports false when the string holds a lone surrogate, which makes
// JavaScript throw.
func EncodeURI(href string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(href); {
		r, size := utf8.DecodeRuneInString(href[i:])
		if r == utf8.RuneError && size == 1 {
			return "", false
		}
		i += size
		if r < utf8.RuneSelf {
			if strings.ContainsRune(encodeURIUnescaped, r) {
				out.WriteByte(byte(r))
				continue
			}
			fmt.Fprintf(&out, "%%%02X", r)
			continue
		}
		for _, b := range []byte(string(r)) {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String(), true
}

// CleanUrl escapes the target of a link the way marked does. It reports
// false when the target cannot be escaped.
func CleanUrl(href string) (string, bool) {
	encoded, ok := EncodeURI(href)
	if !ok {
		return "", false
	}
	return replaceAll(other.PercentDecode, encoded, func(string) string { return "%" }), true
}
