// Package marked is a Go port of marked 18.0.11.
//
// The port mirrors the upstream file and function structure so the two
// implementations can be compared line by line:
//
//	marked/src/tokens.ts     -> marked/tokens.go
//	marked/src/helpers.ts    -> marked/helpers.go
//	marked/src/rules.ts      -> marked/rules.go
//	marked/src/Tokenizer.ts  -> marked/tokenizer.go
//	marked/src/Lexer.ts      -> marked/lexer.go
//	marked/src/Instance.ts   -> marked/instance.go
//	marked/src/marked.ts     -> marked/marked.go
//
// marked builds its grammar from regular expressions that use lookahead, which
// Go's standard regexp package cannot express. The port therefore uses
// github.com/dlclark/regexp2, a backtracking engine with the same semantics,
// and keeps marked's expressions verbatim.
//
// Upstream: https://github.com/markedjs/marked (MIT, Copyright (c) 2018+, MarkedJS).
package marked

// Token is marked's token shape. marked describes its tokens with a
// discriminated union in TypeScript, but the runtime values are plain objects
// with every field present, so the port uses a single struct.
type Token struct {
	Type   string
	Raw    string
	Text   string
	Tokens []Token

	// Code.
	CodeBlockStyle string
	Lang           string
	Escaped        bool

	// Def.
	Tag string

	// Link, image.
	Href  string
	Title *string

	// Heading.
	Depth int

	// HTML.
	Pre        bool
	Block      bool
	InLink     bool
	InRawBlock bool

	// List.
	Ordered bool
	// Start is marked's `number | ''`. HasStart reports the number case.
	Start    int
	HasStart bool
	Loose    bool
	Items    []ListItem

	// List item.
	Task    bool
	Checked bool

	// Table.
	Align  []string
	Header []TableCell
	Rows   [][]TableCell

	// HasInline reports that the token has inline tokens that are lexed
	// after the block tokens, the way marked defers the inline runs.
	HasInline bool
	// InlineIndex is the index of the token's inline run in the lexer's
	// inline queue.
	InlineIndex int
	// InlinePrepend holds the tokens that go in front of the inline tokens
	// of the token. marked fills the array of inline tokens after the block
	// tokens are lexed, so a token that puts a checkbox in front of the
	// inline tokens of a task list item records the checkbox here.
	InlinePrepend []Token
}

// ListItem is one item of a list token.
type ListItem struct {
	Raw     string
	Task    bool
	Checked bool
	Loose   bool
	Text    string
	Tokens  []Token
}

// TableCell is one cell of a table token.
type TableCell struct {
	Text   string
	Tokens []Token
	Header bool
	Align  string

	// HasInline reports that the cell has inline tokens that are lexed after
	// the block tokens, the way marked defers the inline runs.
	HasInline bool
	// InlineIndex is the index of the cell's inline run in the lexer's inline
	// queue.
	InlineIndex int
}

// Links holds the link reference definitions of a document.
type Links map[string]Link

// Link is one link reference definition.
type Link struct {
	Href  string
	Title string
}

// Token type names, copied from marked.
const (
	TypeSpace      = "space"
	TypeCode       = "code"
	TypeHeading    = "heading"
	TypeHR         = "hr"
	TypeHTML       = "html"
	TypeParagraph  = "paragraph"
	TypeText       = "text"
	TypeList       = "list"
	TypeListItem   = "list_item"
	TypeTable      = "table"
	TypeBlockquote = "blockquote"
	TypeEscape     = "escape"
	TypeLink       = "link"
	TypeImage      = "image"
	TypeStrong     = "strong"
	TypeEm         = "em"
	TypeCodespan   = "codespan"
	TypeBr         = "br"
	TypeDel        = "del"
	TypeCheckbox   = "checkbox"
	TypeTag        = "html"
	TypeDef        = "def"
	TypeGeneric    = "generic"
)
