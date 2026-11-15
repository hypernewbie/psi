package webaccess

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestHTMLDeepBlockquoteFormattingBounded(t *testing.T) {
	const depth = 40
	body := "<html><body>" + strings.Repeat("<blockquote>", depth) + "x" + strings.Repeat("</blockquote>", depth) + "</body></html>"
	_, markdown, _, err := htmlToMarkdown([]byte(body), nil, 10000)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(markdown, "x") {
		t.Fatalf("text lost: %q", markdown)
	}
	if got := strings.Count(markdown, "> "); got > maxBlockquoteRenderDepth {
		t.Fatalf("blockquote nesting not bounded: %d markers", got)
	}
}

func TestHTMLDeepInlineFormattingBounded(t *testing.T) {
	const depth = 40
	body := "<html><body><p>" + strings.Repeat("<strong>", depth) + "x" + strings.Repeat("</strong>", depth) + "</p></body></html>"
	_, markdown, _, err := htmlToMarkdown([]byte(body), nil, 10000)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(markdown, "x") {
		t.Fatalf("text lost: %q", markdown)
	}
	if got := strings.Count(markdown, "**"); got > 2*maxInlineRenderDepth {
		t.Fatalf("inline nesting not bounded: %d markers", got)
	}
}

func TestHTMLTableRowsKeepCellBoundaries(t *testing.T) {
	body := `<html><body><table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table></body></html>`
	_, markdown, _, err := htmlToMarkdown([]byte(body), nil, 10000)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	for _, want := range []string{"A | B", "1 | 2"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("table row %q missing:\n%s", want, markdown)
		}
	}
}

func TestHTMLOutputTruncationIsReported(t *testing.T) {
	body := `<html><body><p>` + strings.Repeat("a", 100) + `</p></body></html>`
	_, markdown, truncated, err := htmlToMarkdown([]byte(body), nil, 20)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !truncated || len(markdown) > 20 {
		t.Fatalf("truncated=%v len=%d", truncated, len(markdown))
	}
}

func TestHTMLLinksRejectInvalidTargets(t *testing.T) {
	body := `<html><body><p>` +
		`<a href="https://user:secret@example.com/private">credentials</a> ` +
		`<a href="https://example.com:0/bad">zero</a> ` +
		`<a href="https://example.com/ok">ok</a>` +
		`</p></body></html>`
	_, markdown, _, err := htmlToMarkdown([]byte(body), nil, 10000)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if strings.Contains(markdown, "user:secret@") || strings.Contains(markdown, ":0/bad") {
		t.Fatalf("invalid link emitted: %s", markdown)
	}
	if !strings.Contains(markdown, "[ok](https://example.com/ok)") {
		t.Fatalf("valid link missing: %s", markdown)
	}
}

func TestHTMLInlineTruncationPropagatesToParent(t *testing.T) {
	r := &renderer{limit: 4}
	text := r.inlineText(&html.Node{Type: html.TextNode, Data: "abcdef"})
	if text != "abcd" {
		t.Fatalf("inline text = %q", text)
	}
	// The returned prefix exactly fills the parent. Writing it must still
	// retain the fact that the inline renderer dropped the rest.
	r.write(text)
	if !r.truncated {
		t.Fatal("inline truncation was lost by the parent renderer")
	}
}

func TestMarkdownCodeBlocksPreserveWhitespace(t *testing.T) {
	body := "<html><body><pre>a  \n\n\nb</pre></body></html>"
	_, markdown, _, err := htmlToMarkdown([]byte(body), nil, 10000)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !strings.Contains(markdown, "a  \n\n\nb") {
		t.Fatalf("code whitespace was changed:\n%q", markdown)
	}
}
