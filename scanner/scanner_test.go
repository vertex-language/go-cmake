package scanner_test

import (
	"testing"

	"github.com/vertex-language/go-cmake/scanner"
	"github.com/vertex-language/go-cmake/token"
)

func tokenize(src string) []token.Token {
	fset := token.NewFileSet()
	file := fset.AddFile("test.cmake", len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil)
	var toks []token.Token
	for {
		tok := s.Scan()
		toks = append(toks, tok)
		if tok.Kind == token.EOF {
			break
		}
	}
	return toks
}

func kinds(toks []token.Token) []token.Kind {
	out := make([]token.Kind, len(toks))
	for i, t := range toks {
		out[i] = t.Kind
	}
	return out
}

func lits(toks []token.Token) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.Lit
	}
	return out
}

func TestSimpleCommand(t *testing.T) {
	src := "message(hello)\n"
	toks := tokenize(src)
	wantKinds := []token.Kind{
		token.IDENTIFIER,
		token.LPAREN,
		token.UNQUOTED_ARG,
		token.RPAREN,
		token.NEWLINE,
		token.EOF,
	}
	gotKinds := kinds(toks)
	if len(gotKinds) != len(wantKinds) {
		t.Fatalf("got %d tokens, want %d: %v", len(gotKinds), len(wantKinds), gotKinds)
	}
	for i := range wantKinds {
		if gotKinds[i] != wantKinds[i] {
			t.Errorf("token[%d]: got %v, want %v (lit=%q)", i, gotKinds[i], wantKinds[i], toks[i].Lit)
		}
	}
}

func TestQuotedArg(t *testing.T) {
	src := `message("hello world")`
	toks := tokenize(src)
	wantKinds := []token.Kind{
		token.IDENTIFIER,
		token.LPAREN,
		token.QUOTED_ARG,
		token.RPAREN,
		token.EOF,
	}
	gotKinds := kinds(toks)
	if len(gotKinds) != len(wantKinds) {
		t.Fatalf("got %v want %v", gotKinds, wantKinds)
	}
	for i := range wantKinds {
		if gotKinds[i] != wantKinds[i] {
			t.Errorf("token[%d]: got %v want %v", i, gotKinds[i], wantKinds[i])
		}
	}
	if toks[2].Lit != `"hello world"` {
		t.Errorf("quoted lit = %q, want %q", toks[2].Lit, `"hello world"`)
	}
}

func TestBracketArgLevel0(t *testing.T) {
	src := "message([[bracket content]])\n"
	toks := tokenize(src)
	var bracketTok *token.Token
	for i := range toks {
		if toks[i].Kind == token.BRACKET_ARG {
			bracketTok = &toks[i]
		}
	}
	if bracketTok == nil {
		t.Fatal("no BRACKET_ARG token found")
	}
	if bracketTok.Lit != "[[bracket content]]" {
		t.Errorf("lit = %q, want %q", bracketTok.Lit, "[[bracket content]]")
	}
}

func TestBracketArgLevel1(t *testing.T) {
	src := "message([=[hello]=])\n"
	toks := tokenize(src)
	var bracketTok *token.Token
	for i := range toks {
		if toks[i].Kind == token.BRACKET_ARG {
			bracketTok = &toks[i]
		}
	}
	if bracketTok == nil {
		t.Fatal("no BRACKET_ARG token found")
	}
	if bracketTok.Lit != "[=[hello]=]" {
		t.Errorf("lit = %q, want %q", bracketTok.Lit, "[=[hello]=]")
	}
}

func TestBracketComment(t *testing.T) {
	src := "#[[This is a bracket comment.]]\n"
	toks := tokenize(src)
	if len(toks) < 2 {
		t.Fatal("expected at least 2 tokens")
	}
	if toks[0].Kind != token.BRACKET_COMMENT {
		t.Errorf("got %v, want BRACKET_COMMENT", toks[0].Kind)
	}
}

func TestLineComment(t *testing.T) {
	src := "# this is a comment\n"
	toks := tokenize(src)
	if toks[0].Kind != token.LINE_COMMENT {
		t.Errorf("got %v, want LINE_COMMENT", toks[0].Kind)
	}
	if toks[0].Lit != "# this is a comment" {
		t.Errorf("lit = %q", toks[0].Lit)
	}
}

func TestMultipleArgs(t *testing.T) {
	src := "set(FOO a b c)\n"
	toks := tokenize(src)
	var unquoted []string
	for _, t := range toks {
		if t.Kind == token.UNQUOTED_ARG {
			unquoted = append(unquoted, t.Lit)
		}
	}
	want := []string{"FOO", "a", "b", "c"}
	if len(unquoted) != len(want) {
		t.Fatalf("got %v, want %v", unquoted, want)
	}
	for i := range want {
		if unquoted[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, unquoted[i], want[i])
		}
	}
}

func TestRParen(t *testing.T) {
	src := "if(TRUE)\nendif()\n"
	toks := tokenize(src)
	rparens := 0
	for _, tok := range toks {
		if tok.Kind == token.RPAREN {
			rparens++
		}
	}
	if rparens != 2 {
		t.Errorf("got %d RPARENs, want 2", rparens)
	}
}

func TestNestedParens(t *testing.T) {
	// if(FALSE AND (FALSE OR TRUE))
	src := "if(FALSE AND (FALSE OR TRUE))\n"
	toks := tokenize(src)
	lparens, rparens := 0, 0
	for _, tok := range toks {
		switch tok.Kind {
		case token.LPAREN:
			lparens++
		case token.RPAREN:
			rparens++
		}
	}
	if lparens != 2 || rparens != 2 {
		t.Errorf("got %d LPARENs and %d RPARENs, want 2 each", lparens, rparens)
	}
}

func TestInlineComment(t *testing.T) {
	src := `message("First" #[[bracket]] "Second")` + "\n"
	toks := tokenize(src)
	var bc *token.Token
	for i := range toks {
		if toks[i].Kind == token.BRACKET_COMMENT {
			bc = &toks[i]
		}
	}
	if bc == nil {
		t.Fatal("no BRACKET_COMMENT inside arg list")
	}
}

func TestEmptyFile(t *testing.T) {
	toks := tokenize("")
	if len(toks) != 1 || toks[0].Kind != token.EOF {
		t.Errorf("empty file should produce single EOF, got %v", toks)
	}
}

func TestBOM(t *testing.T) {
	// UTF-8 BOM + command
	src := "\xef\xbb\xbfmessage(hi)\n"
	toks := tokenize(src)
	if toks[0].Kind != token.IDENTIFIER || toks[0].Lit != "message" {
		t.Errorf("first token after BOM: got %v %q", toks[0].Kind, toks[0].Lit)
	}
}

func TestCRLF(t *testing.T) {
	src := "message()\r\nmessage()\r\n"
	toks := tokenize(src)
	newlines := 0
	for _, tok := range toks {
		if tok.Kind == token.NEWLINE {
			newlines++
		}
	}
	if newlines != 2 {
		t.Errorf("got %d newlines, want 2", newlines)
	}
}

func TestQuotedEscapes(t *testing.T) {
	src := `message("a\nb\tc")` + "\n"
	toks := tokenize(src)
	var q *token.Token
	for i := range toks {
		if toks[i].Kind == token.QUOTED_ARG {
			q = &toks[i]
		}
	}
	if q == nil {
		t.Fatal("no QUOTED_ARG")
	}
	if q.Lit != `"a\nb\tc"` {
		t.Errorf("lit = %q", q.Lit)
	}
}

func TestSemicolonInUnquoted(t *testing.T) {
	// A semicolon in an unquoted arg should be part of the arg literal.
	src := "set(X a;b;c)\n"
	toks := tokenize(src)
	var ua string
	for _, tok := range toks {
		if tok.Kind == token.UNQUOTED_ARG && tok.Lit == "a;b;c" {
			ua = tok.Lit
		}
	}
	if ua == "" {
		t.Error("expected unquoted arg 'a;b;c'")
	}
}
