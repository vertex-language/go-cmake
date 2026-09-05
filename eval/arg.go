package eval

import (
	"github.com/vertex-language/go-cmake/ast"
	"github.com/vertex-language/go-cmake/expr"
)

// Arg is one expanded command argument. Quoted records whether the argument
// came from a quoted or bracket argument in the source, which if() needs in
// order to implement CMP0054: a quoted argument is never dereferenced as a
// variable name.
type Arg struct {
	Val    string
	Quoted bool
}

// Args converts a slice of Arg to a plain []string.
func Args(args []Arg) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a.Val
	}
	return out
}

// Strings wraps a []string as unquoted Args.
func Strings(vals ...string) []Arg {
	out := make([]Arg, len(vals))
	for i, v := range vals {
		out[i] = Arg{Val: v}
	}
	return out
}

// expandArg expands one AST argument, preserving whether it was quoted.
func expandArg(a ast.Arg, lookup expr.Lookup) []Arg {
	switch n := a.(type) {
	case *ast.BracketArg:
		return []Arg{{Val: n.Content(), Quoted: true}}
	case *ast.QuotedArg:
		return []Arg{{Val: expr.ExpandString(n.Inner(), lookup), Quoted: true}}
	case *ast.UnquotedArg:
		vals := expr.ExpandArg(n, lookup)
		out := make([]Arg, 0, len(vals))
		for _, v := range vals {
			out = append(out, Arg{Val: v})
		}
		return out
	default:
		return nil
	}
}

// expandArgList expands every semantic argument of a command invocation.
func expandArgList(args []ast.Arg, lookup expr.Lookup) []Arg {
	var out []Arg
	for _, a := range args {
		out = append(out, expandArg(a, lookup)...)
	}
	return out
}
