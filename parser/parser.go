// Package parser implements the CMake language parser.
//
// It converts a sequence of [token.Token] values (produced by [scanner.Scanner])
// into an [*ast.File] representation of a CMake source file.
//
// The top-level entry point is [ParseFile], which handles the complete pipeline
// from raw bytes to AST:
//
//	fset := token.NewFileSet()
//	f, err := parser.ParseFile(fset, "CMakeLists.txt", src)
//
// The parser preserves all whitespace, comments, and newlines in the AST
// so that the source text can be reproduced byte-for-byte via [Print].
package parser

import (
	"fmt"
	"strings"

	"github.com/vertex-language/go-cmake/ast"
	"github.com/vertex-language/go-cmake/scanner"
	"github.com/vertex-language/go-cmake/token"
)

// ParseError records a parse error at a specific position.
type ParseError struct {
	Pos token.Position
	Msg string
}

func (e *ParseError) Error() string {
	if e.Pos.IsValid() {
		return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
	}
	return e.Msg
}

// ErrorList is a collection of ParseErrors.
type ErrorList []*ParseError

func (e ErrorList) Error() string {
	switch len(e) {
	case 0:
		return "no errors"
	case 1:
		return e[0].Error()
	}
	return fmt.Sprintf("%s (and %d more errors)", e[0], len(e)-1)
}

// ParseFile parses src as a CMake source file and returns the AST.
// If src is nil, ParseFile reads from the real OS filesystem (not yet implemented).
// The fset is used to record source positions; a new file entry is added for
// the given filename.
//
// If there are parse errors, ParseFile returns the partial AST and a non-nil
// error (of type ErrorList).
func ParseFile(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
	p := &parser{}
	p.init(fset, filename, src)
	f := p.parseFile()
	var err error
	if len(p.errors) > 0 {
		err = p.errors
	}
	return f, err
}

// Print writes the CMake source text of f to w, reproducing the original
// source byte-for-byte (assuming all whitespace/comment nodes were preserved).
func Print(w interface{ WriteString(string) (int, error) }, f *ast.File) error {
	for _, stmt := range f.Stmts {
		if err := printStmt(w, stmt); err != nil {
			return err
		}
	}
	return nil
}

func printStmt(w interface{ WriteString(string) (int, error) }, stmt ast.Stmt) error {
	switch s := stmt.(type) {
	case *ast.CommandInvocation:
		w.WriteString(s.Name.Lit)
		w.WriteString("(")
		for _, arg := range s.Args {
			w.WriteString(arg.Text())
		}
		w.WriteString(")")
	case *ast.LineEnding:
		if s.Comment.Kind != token.ILLEGAL {
			w.WriteString(s.Comment.Lit)
		}
		w.WriteString("\n")
	case *ast.BracketCommentStmt:
		w.WriteString(s.Tok.Lit)
	case *ast.SpaceStmt:
		w.WriteString(s.Tok.Lit)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Internal parser

type parser struct {
	fset   *token.FileSet
	file   *token.File
	src    []byte
	errors ErrorList

	scanner scanner.Scanner
	tokens  []token.Token // look-ahead buffer
	pos     int           // current position in tokens
}

func (p *parser) init(fset *token.FileSet, filename string, src []byte) {
	p.fset = fset
	p.src = src
	p.file = fset.AddFile(filename, len(src))
	p.scanner.Init(p.file, src, func(pos token.Position, msg string) {
		p.errors = append(p.errors, &ParseError{Pos: pos, Msg: msg})
	})
	// Pre-fill a small look-ahead.
	p.tokens = nil
	p.pos = 0
}

func (p *parser) peek() token.Token {
	for p.pos >= len(p.tokens) {
		tok := p.scanner.Scan()
		p.tokens = append(p.tokens, tok)
	}
	return p.tokens[p.pos]
}

func (p *parser) next() token.Token {
	tok := p.peek()
	p.pos++
	return tok
}

func (p *parser) errorf(pos token.Pos, format string, args ...any) {
	p.errors = append(p.errors, &ParseError{
		Pos: p.fset.Position(pos),
		Msg: fmt.Sprintf(format, args...),
	})
}

// skipTrivia skips over SPACE, NEWLINE, LINE_COMMENT, BRACKET_COMMENT at the
// file level and returns them as SpaceStmt / LineEnding / BracketCommentStmt nodes.
func (p *parser) collectFileTrivia() []ast.Stmt {
	var stmts []ast.Stmt
	for {
		tok := p.peek()
		switch tok.Kind {
		case token.SPACE:
			p.next()
			stmts = append(stmts, &ast.SpaceStmt{Tok: tok})
		case token.NEWLINE:
			p.next()
			stmts = append(stmts, &ast.LineEnding{Newline: tok.Pos})
		case token.LINE_COMMENT:
			comment := p.next()
			// Expect NEWLINE after line comment.
			var nl token.Pos
			if p.peek().Kind == token.NEWLINE {
				nlTok := p.next()
				nl = nlTok.Pos
			} else {
				nl = comment.Pos + token.Pos(len(comment.Lit))
			}
			stmts = append(stmts, &ast.LineEnding{Comment: comment, Newline: nl})
		case token.BRACKET_COMMENT:
			p.next()
			stmts = append(stmts, &ast.BracketCommentStmt{Tok: tok})
		default:
			return stmts
		}
	}
}

// parseFile parses a complete CMake source file.
func (p *parser) parseFile() *ast.File {
	f := &ast.File{FilePos: p.file.Base()}

	for {
		// Collect leading whitespace / comments / newlines.
		trivia := p.collectFileTrivia()
		f.Stmts = append(f.Stmts, trivia...)

		tok := p.peek()
		if tok.Kind == token.EOF {
			p.next()
			break
		}
		if tok.Kind != token.IDENTIFIER {
			p.errorf(tok.Pos, "expected command name, got %v", tok.Kind)
			p.next() // error recovery
			continue
		}

		cmd := p.parseCommandInvocation()
		if cmd != nil {
			f.Stmts = append(f.Stmts, cmd)
		}
	}

	f.FileEnd = p.file.Pos(len(p.src))
	return f
}

// parseCommandInvocation parses: IDENTIFIER SPACE* LPAREN args RPAREN
func (p *parser) parseCommandInvocation() *ast.CommandInvocation {
	nameTok := p.next() // IDENTIFIER

	// Collect any space between name and '('.
	// We keep spaces as InlineSpace args so Print can reproduce them.
	var preParenSpace *ast.InlineSpace
	if p.peek().Kind == token.SPACE {
		sp := p.next()
		preParenSpace = &ast.InlineSpace{TokPos: sp.Pos, Lit: sp.Lit}
	}

	if p.peek().Kind != token.LPAREN {
		p.errorf(p.peek().Pos, "expected '(' after command name %q, got %v", nameTok.Lit, p.peek().Kind)
		return nil
	}
	lparen := p.next() // LPAREN

	cmd := &ast.CommandInvocation{
		Name:   nameTok,
		LParen: lparen.Pos,
	}
	if preParenSpace != nil {
		cmd.Args = append(cmd.Args, preParenSpace)
	}

	// Parse arguments until the matching RPAREN.
	args := p.parseArguments(1)
	cmd.Args = append(cmd.Args, args...)

	// Consume RPAREN.
	if p.peek().Kind == token.RPAREN {
		cmd.RParen = p.next().Pos
	} else {
		p.errorf(p.peek().Pos, "expected ')' to close command %q", nameTok.Lit)
		cmd.RParen = p.peek().Pos
	}

	// Collect line ending after command.
	p.collectLineEnding(cmd)

	return cmd
}

// collectLineEnding swallows optional SPACE, LINE_COMMENT, NEWLINE after a command.
// These are discarded (not attached to the command) because the file-level loop
// will collect them as standalone LineEnding stmts. Actually, to be safe we just
// peek and if it's a space or comment we do nothing — the file-level loop handles it.
func (p *parser) collectLineEnding(_ *ast.CommandInvocation) {
	// Nothing: the file-level loop will pick up spaces, comments, newlines.
}

// parseArguments parses the argument list content (between parens).
// depth is the nesting depth (1 for the outermost call).
// Returns the collected Arg nodes including whitespace/comment nodes.
func (p *parser) parseArguments(depth int) []ast.Arg {
	var args []ast.Arg
	for {
		tok := p.peek()
		switch tok.Kind {
		case token.RPAREN:
			if depth <= 1 {
				// This RPAREN closes the argument list — leave it for the caller.
				return args
			}
			// Nested RPAREN — it was opened by an LPAREN inside the args.
			p.next()
			args = append(args, &ast.UnquotedArg{TokPos: tok.Pos, Lit: ")"})
			depth--

		case token.LPAREN:
			// Nested paren: per the spec, '(' inside args is a literal unquoted arg.
			p.next()
			args = append(args, &ast.UnquotedArg{TokPos: tok.Pos, Lit: "("})
			depth++

		case token.EOF:
			p.errorf(tok.Pos, "unexpected EOF in argument list")
			return args

		case token.NEWLINE:
			p.next()
			args = append(args, &ast.InlineSpace{TokPos: tok.Pos, Lit: "\n"})

		case token.SPACE:
			p.next()
			args = append(args, &ast.InlineSpace{TokPos: tok.Pos, Lit: tok.Lit})

		case token.LINE_COMMENT:
			p.next()
			args = append(args, &ast.InlineComment{TokPos: tok.Pos, Lit: tok.Lit})

		case token.BRACKET_COMMENT:
			p.next()
			args = append(args, &ast.InlineBracketComment{TokPos: tok.Pos, Lit: tok.Lit})

		case token.BRACKET_ARG:
			p.next()
			args = append(args, &ast.BracketArg{TokPos: tok.Pos, Lit: tok.Lit})

		case token.QUOTED_ARG:
			p.next()
			args = append(args, &ast.QuotedArg{TokPos: tok.Pos, Lit: tok.Lit})

		case token.UNQUOTED_ARG:
			p.next()
			args = append(args, &ast.UnquotedArg{TokPos: tok.Pos, Lit: tok.Lit})

		default:
			p.errorf(tok.Pos, "unexpected token %v in argument list", tok.Kind)
			p.next()
		}
	}
}

// CommandNames returns a slice of all command names found in the file (in order).
// Useful for quick inspection without walking the full AST.
func CommandNames(f *ast.File) []string {
	var names []string
	for _, stmt := range f.Stmts {
		if cmd, ok := stmt.(*ast.CommandInvocation); ok {
			names = append(names, strings.ToLower(cmd.Name.Lit))
		}
	}
	return names
}

// RealArgs returns only the semantic argument nodes from a CommandInvocation,
// filtering out InlineSpace, InlineComment, and InlineBracketComment nodes.
func RealArgs(cmd *ast.CommandInvocation) []ast.Arg {
	var out []ast.Arg
	for _, a := range cmd.Args {
		switch a.(type) {
		case *ast.InlineSpace, *ast.InlineComment, *ast.InlineBracketComment:
			// skip
		default:
			out = append(out, a)
		}
	}
	return out
}
