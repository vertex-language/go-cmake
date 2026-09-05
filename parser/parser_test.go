package parser_test

import (
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/ast"
	"github.com/vertex-language/go-cmake/parser"
	"github.com/vertex-language/go-cmake/token"
)

func parse(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.cmake", []byte(src))
	if err != nil {
		t.Fatalf("ParseFile error: %v", err)
	}
	return f
}

func TestSimpleCommand(t *testing.T) {
	f := parse(t, "message(hello)\n")
	names := parser.CommandNames(f)
	if len(names) != 1 || names[0] != "message" {
		t.Errorf("CommandNames = %v, want [message]", names)
	}
}

func TestCommandNameCaseInsensitive(t *testing.T) {
	f := parse(t, "MESSAGE(hello)\n")
	names := parser.CommandNames(f)
	if len(names) != 1 || names[0] != "message" {
		t.Errorf("CommandNames = %v, want [message]", names)
	}
}

func TestMultipleCommands(t *testing.T) {
	src := "cmake_minimum_required(VERSION 3.20)\nproject(MyProject)\nadd_executable(main main.cpp)\n"
	f := parse(t, src)
	names := parser.CommandNames(f)
	want := []string{"cmake_minimum_required", "project", "add_executable"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, names[i], want[i])
		}
	}
}

func TestRealArgs(t *testing.T) {
	f := parse(t, "set(FOO a b c)\n")
	var cmd *ast.CommandInvocation
	for _, s := range f.Stmts {
		if c, ok := s.(*ast.CommandInvocation); ok {
			cmd = c
			break
		}
	}
	if cmd == nil {
		t.Fatal("no command found")
	}
	args := parser.RealArgs(cmd)
	var lits []string
	for _, a := range args {
		lits = append(lits, a.Text())
	}
	want := []string{"FOO", "a", "b", "c"}
	if len(lits) != len(want) {
		t.Fatalf("RealArgs = %v, want %v", lits, want)
	}
	for i := range want {
		if lits[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, lits[i], want[i])
		}
	}
}

func TestBracketArgInCommand(t *testing.T) {
	f := parse(t, "message([=[hello]=])\n")
	for _, s := range f.Stmts {
		if cmd, ok := s.(*ast.CommandInvocation); ok {
			args := parser.RealArgs(cmd)
			if len(args) != 1 {
				t.Fatalf("want 1 real arg, got %d", len(args))
			}
			ba, ok := args[0].(*ast.BracketArg)
			if !ok {
				t.Fatalf("want *ast.BracketArg, got %T", args[0])
			}
			if ba.Lit != "[=[hello]=]" {
				t.Errorf("lit = %q", ba.Lit)
			}
			if ba.Content() != "hello" {
				t.Errorf("Content() = %q", ba.Content())
			}
		}
	}
}

func TestQuotedArgInCommand(t *testing.T) {
	f := parse(t, `message("hello world")`+"\n")
	for _, s := range f.Stmts {
		if cmd, ok := s.(*ast.CommandInvocation); ok {
			args := parser.RealArgs(cmd)
			if len(args) != 1 {
				t.Fatalf("want 1 real arg, got %d", len(args))
			}
			qa, ok := args[0].(*ast.QuotedArg)
			if !ok {
				t.Fatalf("want *ast.QuotedArg, got %T", args[0])
			}
			if qa.Inner() != "hello world" {
				t.Errorf("Inner() = %q", qa.Inner())
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	// The printer should reproduce the original source exactly.
	sources := []string{
		"message(hello)\n",
		"# comment\nadd_executable(main main.cpp)\n",
		"cmake_minimum_required(VERSION 3.20)\nproject(MyProject CXX)\n",
		"if(TRUE)\n  message(yes)\nendif()\n",
		"set(SRCS a.cpp\n         b.cpp\n         c.cpp)\n",
	}
	for _, src := range sources {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "test.cmake", []byte(src))
		if err != nil {
			t.Errorf("ParseFile(%q): %v", src, err)
			continue
		}
		var sb strings.Builder
		if err := parser.Print(&sb, f); err != nil {
			t.Errorf("Print(%q): %v", src, err)
			continue
		}
		if sb.String() != src {
			t.Errorf("round-trip mismatch:\ngot:  %q\nwant: %q", sb.String(), src)
		}
	}
}

func TestNestedParens(t *testing.T) {
	// if(FALSE AND (FALSE OR TRUE)) — nested parens are literal unquoted args.
	src := "if(FALSE AND (FALSE OR TRUE))\n"
	f := parse(t, src)
	for _, s := range f.Stmts {
		if cmd, ok := s.(*ast.CommandInvocation); ok {
			args := parser.RealArgs(cmd)
			// Should have: FALSE AND ( FALSE OR TRUE )
			found := false
			for _, a := range args {
				if a.Text() == "(" {
					found = true
				}
			}
			if !found {
				t.Error("expected '(' as unquoted arg inside nested parens")
			}
		}
	}
}

func TestEmptyFile(t *testing.T) {
	f := parse(t, "")
	if len(parser.CommandNames(f)) != 0 {
		t.Error("empty file should have no commands")
	}
}

func TestCommentOnlyFile(t *testing.T) {
	f := parse(t, "# just a comment\n")
	if len(parser.CommandNames(f)) != 0 {
		t.Error("comment-only file should have no commands")
	}
}
