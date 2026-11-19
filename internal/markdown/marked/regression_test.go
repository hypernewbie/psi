package marked

import (
	"fmt"
	"strings"
	"testing"
)

// TestRenameNamedGroupsKeepsGroupNumbers checks that the port numbers the
// groups of a match the way JavaScript does.
//
// JavaScript numbers the groups of an expression by the order of their
// opening parenthesis, so a named group counts towards the number of the
// groups that follow it. The .NET dialect numbers the unnamed groups first.
// The port renames the named groups so that both dialects agree, because
// marked reads the groups of a match by number.
func TestRenameNamedGroupsKeepsGroupNumbers(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "a named group keeps its place",
			source: `(?<a>x)(y)`,
			want:   `(x)(y)`,
		},
		{
			name:   "a named backreference becomes a number",
			source: `(?<a>x)\k<a>`,
			want:   `(x)\1`,
		},
		{
			name:   "a lookbehind is not a group",
			source: `(?<=a)b`,
			want:   `(?<=a)b`,
		},
		{
			name:   "a negative lookbehind is not a group",
			source: `(?<!a)b`,
			want:   `(?<!a)b`,
		},
		{
			name:   "a parenthesis in a class is not a group",
			source: `[(?<a>]`,
			want:   `[(?<a>]`,
		},
		{
			name:   "an escaped parenthesis is not a group",
			source: `\(?<a>`,
			want:   `\(?<a>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renameNamedGroups(test.source); got != test.want {
				t.Fatalf("renameNamedGroups(%q) = %q, want %q", test.source, got, test.want)
			}
		})
	}
}

// TestBlockSkipMasksCodeSpans checks that the port masks a code span the way
// marked does.
//
// marked masks a link, a code span and a html tag with a run of "a" of the
// same length, so the masked source keeps the length of the source it
// replaces. The emphasis rules read the masked source, so a delimiter inside
// a code span must not close an emphasis span.
func TestBlockSkipMasksCodeSpans(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "a code span", src: "*foo`*`", want: "*foo[a]"},
		{name: "a link", src: "a [b](c) d", want: "a [aaaa] d"},
		{name: "a html tag", src: "a <b> c", want: "a [a] c"},
		{name: "no match", src: "plain text", want: "plain text"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := maskBlockSkip(test.src)
			if got != test.want {
				t.Fatalf("maskBlockSkip(%q) = %q, want %q", test.src, got, test.want)
			}
		})
	}
}

// maskBlockSkip masks the links, code spans and html tags of a source the way
// marked's inline lexer does.
func maskBlockSkip(src string) string {
	return replaceAllMatches(inlineNormal.BlockSkip, src, func(found *match) string {
		match0 := found.whole()
		context := found.group(2)
		offset := 0
		if context != "" {
			offset = utf16Len(context)
		}
		return cutPrefix(match0, offset) + "[" + strings.Repeat("a", utf16Len(match0)-offset-2) + "]"
	})
}

// TestEmphasisStopsAtCodeSpan checks that a delimiter inside a code span does
// not close an emphasis span.
//
// This is the case of the CommonMark example 341.
func TestEmphasisStopsAtCodeSpan(t *testing.T) {
	tokens, _ := Lex("*foo`*`\n", DefaultOptions())
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	inline := tokens[0].Tokens
	if len(inline) != 2 {
		t.Fatalf("got %d inline tokens, want 2", len(inline))
	}
	if inline[0].Type != TypeText || inline[0].Text != "*foo" {
		t.Fatalf("first inline token is %q %q, want text %q", inline[0].Type, inline[0].Text, "*foo")
	}
	if inline[1].Type != TypeCodespan || inline[1].Text != "*" {
		t.Fatalf("second inline token is %q %q, want codespan %q", inline[1].Type, inline[1].Text, "*")
	}
}

// TestLinkTitleAfterClosingParenthesis checks that a link that holds a
// parenthesis in its text does not take the text as its title.
//
// marked cuts the link at the closing parenthesis of its target and drops the
// title that the expression matched.
func TestLinkTitleAfterClosingParenthesis(t *testing.T) {
	tokens, _ := Lex("![](img1.svg) (or ![](img2.svg))\n", DefaultOptions())
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	inline := tokens[0].Tokens
	if len(inline) != 4 {
		t.Fatalf("got %d inline tokens, want 4", len(inline))
	}
	if inline[0].Type != TypeImage {
		t.Fatalf("first inline token is %q, want %q", inline[0].Type, TypeImage)
	}
	if inline[0].Raw != "![](img1.svg)" {
		t.Fatalf("image raw is %q, want %q", inline[0].Raw, "![](img1.svg)")
	}
	if inline[0].Title != nil && *inline[0].Title != "" {
		t.Fatalf("image title is %q, want an empty title", *inline[0].Title)
	}
}

// TestEmphasisWithAstralCharacter checks that the length of a delimiter run
// that follows an astral character is measured in UTF-16 code units.
//
// marked measures the character before the closing delimiter in UTF-16 code
// units, so an emoji in front of the delimiter moves the end of the emphasis
// span.
func TestEmphasisWithAstralCharacter(t *testing.T) {
	tokens, _ := Lex("**test 💁**\n", DefaultOptions())
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	inline := tokens[0].Tokens
	if len(inline) != 1 {
		t.Fatalf("got %d inline tokens, want 1", len(inline))
	}
	if inline[0].Type != TypeStrong {
		t.Fatalf("inline token is %q, want %q", inline[0].Type, TypeStrong)
	}
	if inline[0].Raw != "**test 💁**" {
		t.Fatalf("strong raw is %q, want %q", inline[0].Raw, "**test 💁**")
	}
	if inline[0].Text != "test 💁" {
		t.Fatalf("strong text is %q, want %q", inline[0].Text, "test 💁")
	}
}

// TestLooseTaskListKeepsCheckboxInParagraph checks that the checkbox of a
// task list item in a loose list stays in front of the paragraph of the item.
//
// marked puts the checkbox in front of the inline tokens of the first
// paragraph of the item.
func TestLooseTaskListKeepsCheckboxInParagraph(t *testing.T) {
	tokens, _ := Lex("- [ ] item 1\n\n- [ ] item 2\n", DefaultOptions())
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	list := tokens[0]
	if !list.Loose {
		t.Fatal("the list is not loose")
	}
	if len(list.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(list.Items))
	}
	for i, item := range list.Items {
		if len(item.Tokens) == 0 {
			t.Fatalf("item %d has no tokens", i)
		}
		paragraph := item.Tokens[0]
		if paragraph.Type != TypeParagraph {
			t.Fatalf("item %d starts with a %q token, want %q", i, paragraph.Type, TypeParagraph)
		}
		if !strings.HasPrefix(paragraph.Text, "[ ] ") {
			t.Fatalf("item %d paragraph text is %q, want it to start with %q", i, paragraph.Text, "[ ] ")
		}
		if len(paragraph.Tokens) != 2 {
			t.Fatalf("item %d paragraph has %d inline tokens, want 2", i, len(paragraph.Tokens))
		}
		if paragraph.Tokens[0].Type != TypeCheckbox {
			t.Fatalf("item %d starts with a %q token, want %q", i, paragraph.Tokens[0].Type, TypeCheckbox)
		}
		if paragraph.Tokens[1].Type != TypeText || paragraph.Tokens[1].Text != "item "+string(rune('1'+i)) {
			t.Fatalf("item %d has the inline token %q %q, want the text of the item", i,
				paragraph.Tokens[1].Type, paragraph.Tokens[1].Text)
		}
	}
}

// TestTightTaskListKeepsCheckboxInItem checks that the checkbox of a task
// list item in a tight list stands in front of the tokens of the item.
func TestTightTaskListKeepsCheckboxInItem(t *testing.T) {
	tokens, _ := Lex("- [x] item\n", DefaultOptions())
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	list := tokens[0]
	if list.Loose {
		t.Fatal("the list is loose")
	}
	if len(list.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(list.Items))
	}
	item := list.Items[0]
	if !item.Task || !item.Checked {
		t.Fatalf("item is task %v checked %v, want task true checked true", item.Task, item.Checked)
	}
	if len(item.Tokens) != 2 {
		t.Fatalf("got %d tokens, want 2", len(item.Tokens))
	}
	if item.Tokens[0].Type != TypeCheckbox {
		t.Fatalf("the item starts with a %q token, want %q", item.Tokens[0].Type, TypeCheckbox)
	}
}

// TestCachedIndentRegexUsesTheClampedIndex checks that the helper
// expressions of marked that depend on the indent share their cache the way
// marked shares it.
//
// marked clamps the indent to the range of zero to three before it builds
// the expression, and the expression is built with the clamped indent.
func TestCachedIndentRegexUsesTheClampedIndex(t *testing.T) {
	for indent := 1; indent <= 8; indent++ {
		want := fmt.Sprintf("^ {0,%d}", max(0, min(3, indent-1)))
		if pattern := other.NextBulletRegex(indent).String(); !strings.HasPrefix(pattern, want) {
			t.Fatalf("the bullet expression for indent %d is %q, want it to start with %q", indent, pattern, want)
		}
	}
}
