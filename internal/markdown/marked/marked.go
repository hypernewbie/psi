package marked

// The package level API, ported from marked/src/marked.ts. marked renders the
// tokens to HTML; psi renders them in the terminal, so the port stops at the
// token stream.

// Lex parses a markdown document into tokens, like marked's `marked.lexer`.
func Lex(src string, options *MarkedOptions) ([]Token, Links) {
	lexer := NewLexer(options)
	tokens := lexer.Lex(src)
	if options != nil && options.WalkTokens != nil {
		walkTokens(tokens, options.WalkTokens)
	}
	return tokens, lexer.Links
}

// LexInline parses inline markdown into tokens, like marked's
// `marked.parseInline`.
func LexInline(src string, options *MarkedOptions) []Token {
	lexer := NewLexer(options)
	return lexer.inlineTokens(src, nil)
}

// walkTokens calls the walk function with every token, like marked's
// `walkTokens`.
func walkTokens(tokens []Token, walk func(*Token)) {
	for i := range tokens {
		walk(&tokens[i])
		walkTokens(tokens[i].Tokens, walk)
		for j := range tokens[i].Items {
			walkTokens(tokens[i].Items[j].Tokens, walk)
		}
		for j := range tokens[i].Header {
			walkTokens(tokens[i].Header[j].Tokens, walk)
		}
		for j := range tokens[i].Rows {
			for k := range tokens[i].Rows[j] {
				walkTokens(tokens[i].Rows[j][k].Tokens, walk)
			}
		}
	}
}
