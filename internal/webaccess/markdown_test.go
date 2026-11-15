package webaccess

import (
	"strings"
	"testing"
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
