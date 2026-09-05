package ast

import "github.com/vertex-language/go-cmake/token"

// Node is the base interface for all AST nodes.
type Node interface {
	Pos() token.Pos // position of the first character of the node
	End() token.Pos // position of the first character after the node
}

// Stmt is a statement node (currently only CommandInvocation at file level).
type Stmt interface {
	Node
	stmtNode()
}

// Arg is an argument node — one of BracketArg, QuotedArg, or UnquotedArg.
type Arg interface {
	Node
	argNode()
	// Text returns the raw literal text of the argument as it appears in source.
	Text() string
}

// ----------------------------------------------------------------------------
// File

// File is the root of an AST for a single CMake source file.
// It consists of zero or more top-level command invocations.
type File struct {
	Stmts   []Stmt    // top-level command invocations (and whitespace stmts)
	FilePos token.Pos // position of the first byte of the file
	FileEnd token.Pos // position one past the last byte of the file
}

func (f *File) Pos() token.Pos { return f.FilePos }
func (f *File) End() token.Pos { return f.FileEnd }

// ----------------------------------------------------------------------------
// Statements

// CommandInvocation is a single command call: name '(' args ')'.
//
//	add_executable(hello world.c)
type CommandInvocation struct {
	Name   token.Token // IDENTIFIER — the command name
	LParen token.Pos   // position of '('
	Args   []Arg       // argument list (may be empty)
	RParen token.Pos   // position of ')'
}

func (c *CommandInvocation) Pos() token.Pos { return c.Name.Pos }
func (c *CommandInvocation) End() token.Pos { return c.RParen + 1 }
func (c *CommandInvocation) stmtNode()      {}

// LineEnding represents a newline (possibly preceded by a line comment)
// at the file level, between command invocations. Preserved for round-trip.
type LineEnding struct {
	Comment token.Token // LINE_COMMENT or zero Token if none
	Newline token.Pos   // position of '\n'
}

func (l *LineEnding) Pos() token.Pos {
	if l.Comment.Kind != token.ILLEGAL {
		return l.Comment.Pos
	}
	return l.Newline
}
func (l *LineEnding) End() token.Pos { return l.Newline + 1 }
func (l *LineEnding) stmtNode()      {}

// BracketComment is a bracket comment at the file level.
type BracketCommentStmt struct {
	Tok token.Token // BRACKET_COMMENT
}

func (b *BracketCommentStmt) Pos() token.Pos { return b.Tok.Pos }
func (b *BracketCommentStmt) End() token.Pos { return b.Tok.Pos + token.Pos(len(b.Tok.Lit)) }
func (b *BracketCommentStmt) stmtNode()      {}

// SpaceStmt is horizontal whitespace between invocations. Preserved for round-trip.
type SpaceStmt struct {
	Tok token.Token // SPACE
}

func (s *SpaceStmt) Pos() token.Pos { return s.Tok.Pos }
func (s *SpaceStmt) End() token.Pos { return s.Tok.Pos + token.Pos(len(s.Tok.Lit)) }
func (s *SpaceStmt) stmtNode()      {}

// ----------------------------------------------------------------------------
// Arguments

// BracketArg is a bracket argument: [==[content]==]
// The content is stored as-is; no escape or variable expansion is performed.
type BracketArg struct {
	TokPos token.Pos // position of the opening '['
	Lit    string    // full text including brackets: "[==[...]==]"
}

func (b *BracketArg) Pos() token.Pos { return b.TokPos }
func (b *BracketArg) End() token.Pos { return b.TokPos + token.Pos(len(b.Lit)) }
func (b *BracketArg) Text() string   { return b.Lit }
func (b *BracketArg) argNode()       {}

// Content returns the inner content of the bracket argument,
// stripping the opening/closing brackets. If the first character
// of the content is a newline it is also stripped per the spec.
func (b *BracketArg) Content() string {
	// Find the level: [=*[
	level := 0
	for i := 1; i < len(b.Lit); i++ {
		if b.Lit[i] == '=' {
			level++
		} else {
			break
		}
	}
	openLen := level + 2  // '[' + '='*level + '['
	closeLen := level + 2 // ']' + '='*level + ']'
	content := b.Lit[openLen : len(b.Lit)-closeLen]
	if len(content) > 0 && content[0] == '\n' {
		content = content[1:]
	}
	return content
}

// QuotedArg is a double-quoted argument: "content"
// The content may contain escape sequences and variable references.
type QuotedArg struct {
	TokPos token.Pos // position of the opening '"'
	Lit    string    // full text including quotes: "\"...\""
}

func (q *QuotedArg) Pos() token.Pos { return q.TokPos }
func (q *QuotedArg) End() token.Pos { return q.TokPos + token.Pos(len(q.Lit)) }
func (q *QuotedArg) Text() string   { return q.Lit }
func (q *QuotedArg) argNode()       {}

// Inner returns the content between the quotes (the Lit without leading/trailing '"').
func (q *QuotedArg) Inner() string {
	if len(q.Lit) < 2 {
		return ""
	}
	return q.Lit[1 : len(q.Lit)-1]
}

// UnquotedArg is an unquoted argument. It may contain escape sequences and
// variable references. When expanded, semicolons split it into multiple values.
type UnquotedArg struct {
	TokPos token.Pos // position of the first character
	Lit    string    // raw text
}

func (u *UnquotedArg) Pos() token.Pos { return u.TokPos }
func (u *UnquotedArg) End() token.Pos { return u.TokPos + token.Pos(len(u.Lit)) }
func (u *UnquotedArg) Text() string   { return u.Lit }
func (u *UnquotedArg) argNode()       {}

// InlineComment is a line comment appearing inside an argument list,
// between arguments. Preserved for round-trip printing.
type InlineComment struct {
	TokPos token.Pos
	Lit    string // full text including '#'
}

func (i *InlineComment) Pos() token.Pos { return i.TokPos }
func (i *InlineComment) End() token.Pos { return i.TokPos + token.Pos(len(i.Lit)) }
func (i *InlineComment) Text() string   { return i.Lit }
func (i *InlineComment) argNode()       {}

// InlineBracketComment is a bracket comment inside an argument list.
type InlineBracketComment struct {
	TokPos token.Pos
	Lit    string
}

func (b *InlineBracketComment) Pos() token.Pos { return b.TokPos }
func (b *InlineBracketComment) End() token.Pos { return b.TokPos + token.Pos(len(b.Lit)) }
func (b *InlineBracketComment) Text() string   { return b.Lit }
func (b *InlineBracketComment) argNode()       {}

// InlineSpace is whitespace or newline between arguments. Preserved for round-trip.
type InlineSpace struct {
	TokPos token.Pos
	Lit    string
}

func (s *InlineSpace) Pos() token.Pos { return s.TokPos }
func (s *InlineSpace) End() token.Pos { return s.TokPos + token.Pos(len(s.Lit)) }
func (s *InlineSpace) Text() string   { return s.Lit }
func (s *InlineSpace) argNode()       {}

// ----------------------------------------------------------------------------
// Visitor

// Visitor is implemented by types that process AST nodes.
// Visit is called for each node; if it returns a non-nil Visitor,
// that visitor is used for the children of the node.
type Visitor interface {
	Visit(node Node) (w Visitor)
}

// Walk traverses the AST rooted at n in depth-first order,
// calling v.Visit(node) before visiting children and v.Visit(nil) after.
func Walk(v Visitor, node Node) {
	if v = v.Visit(node); v == nil {
		return
	}
	switch n := node.(type) {
	case *File:
		for _, s := range n.Stmts {
			Walk(v, s)
		}
	case *CommandInvocation:
		for _, a := range n.Args {
			Walk(v, a)
		}
	case *LineEnding:
		// leaf
	case *BracketCommentStmt:
		// leaf
	case *SpaceStmt:
		// leaf
	case *BracketArg:
		// leaf
	case *QuotedArg:
		// leaf
	case *UnquotedArg:
		// leaf
	case *InlineComment:
		// leaf
	case *InlineBracketComment:
		// leaf
	case *InlineSpace:
		// leaf
	}
	v.Visit(nil)
}

// Inspect traverses the AST in depth-first order. For each node it calls f.
// If f returns false, Inspect does not visit the children of that node.
func Inspect(node Node, f func(Node) bool) {
	Walk(inspector(f), node)
}

type inspector func(Node) bool

func (fn inspector) Visit(node Node) Visitor {
	if node != nil && fn(node) {
		return fn
	}
	return nil
}
