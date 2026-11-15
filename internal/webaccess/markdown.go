package webaccess

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// htmlToMarkdown converts readable HTML to bounded Markdown. It returns the
// title and whether the output hit the cap. It never allocates the full
// unbounded rendering: writes stop at the limit.
func htmlToMarkdown(data []byte, base *url.URL, limit int) (title, markdown string, truncated bool, err error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", "", false, err
	}
	if htmlTooDeep(doc, maxHTMLDepth) {
		return "", "", false, fmt.Errorf("html nesting exceeds %d levels", maxHTMLDepth)
	}
	root := findElement(doc, "body")
	if root == nil {
		root = doc
	}
	title = elementText(findElement(doc, "title"))
	if title == "" {
		title = elementText(findElement(root, "h1"))
	}
	r := &renderer{base: base, out: strings.Builder{}, limit: limit}
	r.block(root)
	text := tidyMarkdown(r.out.String())
	if len(text) > limit {
		text = runePrefix(text, limit)
		truncated = true
	}
	return title, text, truncated, nil
}

const (
	maxHTMLDepth = 512
	// Inline formatting creates a temporary renderer at each level. Bound the
	// depth so deeply nested markup cannot duplicate a large text buffer 512
	// times. Levels below the cap keep their formatting; deeper ones flatten.
	maxInlineRenderDepth = 16
)

// htmlTooDeep rejects pathological nesting before recursive projection so a
// hostile page cannot exhaust the goroutine stack.
func htmlTooDeep(root *html.Node, limit int) bool {
	type frame struct {
		node  *html.Node
		depth int
	}
	stack := []frame{{node: root}}
	for len(stack) > 0 {
		last := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if last.depth > limit {
			return true
		}
		for child := last.node.FirstChild; child != nil; child = child.NextSibling {
			stack = append(stack, frame{node: child, depth: last.depth + 1})
		}
	}
	return false
}

type renderer struct {
	base        *url.URL
	out         strings.Builder
	limit       int
	inlineDepth int
	truncated   bool
}

func (r *renderer) write(s string) {
	if s == "" || r.truncated {
		return
	}
	if r.out.Len()+len(s) <= r.limit {
		r.out.WriteString(s)
		return
	}
	r.out.WriteString(runePrefix(s, r.limit-r.out.Len()))
	r.truncated = true
}

func (r *renderer) inlineRenderer() *renderer {
	depth := r.inlineDepth + 1
	if depth > maxInlineRenderDepth {
		depth = maxInlineRenderDepth
	}
	return &renderer{base: r.base, limit: r.limit, inlineDepth: depth}
}

func (r *renderer) inlineText(n *html.Node) string {
	sub := r.inlineRenderer()
	sub.inline(n)
	return sub.out.String()
}

func (r *renderer) inlineChildren(n *html.Node) string {
	sub := r.inlineRenderer()
	sub.inlineChildrenInPlace(n)
	return sub.out.String()
}

func (r *renderer) inlineChildrenInPlace(n *html.Node) {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		r.inline(child)
	}
}

func (r *renderer) inline(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		r.write(escapeMarkdown(collapseSpace(n.Data)))
		return
	case html.ElementNode:
	default:
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			r.inline(child)
		}
		return
	}
	switch n.Data {
	case "script", "style", "noscript", "svg":
		return
	case "br":
		r.write("\n")
	case "img":
		alt := attr(n, "alt")
		if alt != "" {
			r.write(escapeMarkdown(alt))
		}
	case "a":
		if r.inlineDepth >= maxInlineRenderDepth {
			r.inlineChildrenInPlace(n)
			return
		}
		text := strings.TrimSpace(r.inlineChildren(n))
		href := r.resolveLink(attr(n, "href"))
		if href != "" && text != "" {
			r.write("[" + text + "](" + href + ")")
		} else {
			r.write(text)
		}
	case "code":
		if r.inlineDepth >= maxInlineRenderDepth {
			r.inlineChildrenInPlace(n)
			return
		}
		text := r.inlineChildren(n)
		if text != "" {
			r.write("`" + strings.ReplaceAll(text, "`", "'") + "`")
		}
	case "strong", "b":
		if r.inlineDepth >= maxInlineRenderDepth {
			r.inlineChildrenInPlace(n)
			return
		}
		text := r.inlineChildren(n)
		if text = strings.TrimSpace(text); text != "" {
			r.write("**" + text + "**")
		}
	case "em", "i":
		if r.inlineDepth >= maxInlineRenderDepth {
			r.inlineChildrenInPlace(n)
			return
		}
		text := r.inlineChildren(n)
		if text = strings.TrimSpace(text); text != "" {
			r.write("*" + text + "*")
		}
	default:
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			r.inline(child)
		}
	}
}

func (r *renderer) block(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if text := strings.TrimSpace(collapseSpace(n.Data)); text != "" {
			r.write(escapeMarkdown(text))
		}
		return
	case html.ElementNode:
	default:
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			r.block(child)
		}
		return
	}
	switch n.Data {
	case "script", "style", "noscript", "svg", "nav", "footer", "aside":
		return
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(n.Data[1] - '0')
		text := strings.TrimSpace(r.inlineText(n))
		if text != "" {
			r.write("\n\n" + strings.Repeat("#", level) + " " + text + "\n\n")
		}
	case "p":
		text := strings.TrimSpace(r.inlineText(n))
		if text != "" {
			r.write("\n\n" + text + "\n\n")
		}
	case "br":
		r.write("\n")
	case "hr":
		r.write("\n\n---\n\n")
	case "ul", "ol":
		r.write("\n\n")
		r.list(n, n.Data == "ol", 0)
		r.write("\n")
	case "pre":
		r.codeBlock(n)
	case "blockquote":
		inner := &renderer{base: r.base, limit: r.limit}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			inner.block(child)
		}
		body := strings.TrimSpace(inner.out.String())
		if body != "" {
			var lines []string
			for _, line := range strings.Split(body, "\n") {
				lines = append(lines, "> "+line)
			}
			r.write("\n\n" + strings.Join(lines, "\n") + "\n\n")
		}
	case "table":
		r.table(n)
	case "li":
		// A stray list item outside a list still renders as a bullet.
		r.write("\n- " + strings.TrimSpace(r.inlineText(n)) + "\n")
	default:
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			r.block(child)
		}
	}
}

func (r *renderer) list(n *html.Node, ordered bool, depth int) {
	index := 1
	for item := n.FirstChild; item != nil; item = item.NextSibling {
		if item.Type != html.ElementNode || item.Data != "li" {
			continue
		}
		marker := "- "
		if ordered {
			marker = itoa(index) + ". "
			index++
		}
		indent := strings.Repeat("  ", depth)
		var inline strings.Builder
		var nested []*html.Node
		for child := item.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && (child.Data == "ul" || child.Data == "ol") {
				nested = append(nested, child)
				continue
			}
			inline.WriteString(r.inlineText(child))
		}
		text := strings.TrimSpace(collapseSpace(inline.String()))
		r.write(indent + marker + text + "\n")
		for _, list := range nested {
			r.list(list, list.Data == "ol", depth+1)
		}
	}
}

func (r *renderer) codeBlock(n *html.Node) {
	text := elementTextPreserve(n)
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return
	}
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	r.write("\n\n" + fence + "\n" + text + "\n" + fence + "\n\n")
}

func (r *renderer) table(n *html.Node) {
	r.write("\n\n")
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "tr" {
			var cells []string
			for cell := node.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type != html.ElementNode || (cell.Data != "td" && cell.Data != "th") {
					continue
				}
				text := strings.TrimSpace(collapseSpace(r.inlineText(cell)))
				cells = append(cells, text)
			}
			if len(cells) > 0 {
				r.write(strings.Join(cells, " | ") + "\n")
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	r.write("\n")
}

func (r *renderer) resolveLink(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if r.base != nil {
		u = r.base.ResolveReference(u)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.String()
	default:
		return ""
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findElement(n *html.Node, name string) *html.Node {
	if n.Type == html.ElementNode && n.Data == name {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, name); found != nil {
			return found
		}
	}
	return nil
}

func elementText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.TrimSpace(collapseSpace(sb.String()))
}

// elementTextPreserve keeps whitespace for preformatted content.
func elementTextPreserve(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return sb.String()
}

func collapseSpace(s string) string {
	var sb strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		space = false
		sb.WriteRune(r)
	}
	if space && sb.Len() > 0 {
		sb.WriteByte(' ')
	}
	return sb.String()
}

func escapeMarkdown(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '<', '>', '|':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func tidyMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// runePrefix returns at most n bytes without splitting a rune.
func runePrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
