// Package eval implements the CMake configure phase: it walks a parsed
// CMakeLists.txt tree, maintains the variable and cache tables, dispatches
// built-in and user-defined commands, and produces a [State] describing
// everything the project declared.
package eval

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/vertex-language/go-cmake/ast"
	"github.com/vertex-language/go-cmake/expr"
)

// FS is the filesystem abstraction eval needs. Every path that reaches it is
// already absolute or relative to the process working directory; eval does no
// rooting of its own.
type FS interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte) error
	MkdirAll(name string) error
	Glob(pattern string) ([]string, error)
	Stat(name string) (fs.FileInfo, error)
	Remove(name string) error
}

// cmdFunc is the signature for a built-in command. Commands receive arguments
// already expanded, with the quotedness of each preserved.
type cmdFunc func(ctx context.Context, e *evaluator, args []Arg) error

// Control-flow sentinels. These never escape a call to [Eval] except for
// returnSignal, which is absorbed at the file boundary.
type breakSignal struct{}
type continueSignal struct{}
type returnSignal struct{ propagate []string }

func (breakSignal) Error() string    { return "break" }
func (continueSignal) Error() string { return "continue" }
func (returnSignal) Error() string   { return "return" }

// FatalError is returned when a script calls message(FATAL_ERROR) or when a
// command reports an unrecoverable problem. It stops the configure phase.
type FatalError struct {
	Msg  string
	File string
	Line int
}

func (e *FatalError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("CMake Error at %s:%d:\n  %s", e.File, e.Line, e.Msg)
	}
	return "CMake Error:\n  " + e.Msg
}

// commands is the built-in command table, keyed by lowercased name.
var commands = map[string]cmdFunc{}

// register adds a built-in command. It panics on a duplicate name so that two
// files cannot silently claim the same command.
func register(name string, fn cmdFunc) {
	if _, dup := commands[name]; dup {
		panic("eval: duplicate command registration: " + name)
	}
	commands[name] = fn
}

// cmdNoOp accepts and ignores a command whose effect is outside this
// implementation's model. It is what a command is registered as when CMake
// does something this package deliberately does not, and accepting it silently
// is better than failing a configure over a directive that changes nothing
// here.
func cmdNoOp(context.Context, *evaluator, []Arg) error { return nil }

// evaluator carries the state, filesystem, and call context through one
// configure run. It exists so that command implementations can recurse into
// the evaluator (include, add_subdirectory, cmake_language(EVAL)) without
// threading four parameters through every signature.
type evaluator struct {
	state *State
	fs    FS
}

// Eval evaluates a single parsed CMake file against state.
func Eval(ctx context.Context, file *ast.File, state *State, filesystem FS) error {
	e := &evaluator{state: state, fs: filesystem}
	err := e.evalStmts(ctx, file.Stmts)
	if _, ok := err.(returnSignal); ok {
		// return() at file scope ends the file, not the configure run.
		return nil
	}
	return err
}

// expand expands a command's argument nodes against the current state.
func (e *evaluator) expand(args []ast.Arg) []Arg {
	return expandArgList(args, e.state.Lookup)
}

// evalStmts evaluates a statement list, resolving block structure as it goes.
func (e *evaluator) evalStmts(ctx context.Context, stmts []ast.Stmt) error {
	for i := 0; i < len(stmts); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := cmdName(stmts[i])
		if name == "" {
			continue // line ending, comment, or whitespace
		}

		if _, isBlock := blockOpeners[name]; isBlock {
			end, err := matchBlock(stmts, i)
			if err != nil {
				return e.errorAt(stmts[i], err.Error())
			}
			if err := e.evalBlockAt(ctx, stmts, i, end); err != nil {
				return err
			}
			i = end
			continue
		}

		if blockClosers[name] {
			return e.errorAt(stmts[i], name+" without a matching opening command")
		}

		if err := e.evalCommand(ctx, invocation(stmts[i])); err != nil {
			return err
		}
	}
	return nil
}

// evalBlockAt dispatches one block construct.
func (e *evaluator) evalBlockAt(ctx context.Context, stmts []ast.Stmt, open, end int) error {
	switch cmdName(stmts[open]) {
	case "if":
		return e.evalIf(ctx, stmts, open, end)
	case "foreach":
		return e.evalForeach(ctx, stmts, open, end)
	case "while":
		return e.evalWhile(ctx, stmts, open, end)
	case "block":
		return e.evalBlockScope(ctx, stmts, open, end)
	case "function":
		return e.defineFunction(stmts, open, end)
	case "macro":
		return e.defineMacro(stmts, open, end)
	}
	return nil
}

// evalCommand expands and dispatches one command invocation.
func (e *evaluator) evalCommand(ctx context.Context, c *ast.CommandInvocation) error {
	name := strings.ToLower(c.Name.Lit)

	// break/continue/return need no argument expansion beyond their own.
	switch name {
	case "break":
		return breakSignal{}
	case "continue":
		return continueSignal{}
	case "return":
		args := Args(e.expand(c.Args))
		var propagate []string
		if len(args) > 0 && args[0] == "PROPAGATE" {
			propagate = args[1:]
		}
		return returnSignal{propagate: propagate}
	}

	args := e.expand(c.Args)
	e.state.setPosition(c)

	// A user-defined command shadows nothing: CMake looks up scripted commands
	// first, so a project may override a built-in by defining a function of the
	// same name.
	if fn, ok := e.state.Functions[name]; ok {
		return e.wrap(c, e.callFunction(ctx, fn, args))
	}
	if mac, ok := e.state.Macros[name]; ok {
		return e.wrap(c, e.callMacro(ctx, mac, args))
	}
	if handler, ok := commands[name]; ok {
		if e.state.ScriptMode && projectOnlyCommands[name] {
			return e.errorAt(c, name+" command is not scriptable")
		}
		return e.wrap(c, handler(ctx, e, args))
	}

	return e.errorAt(c, fmt.Sprintf("Unknown CMake command %q.", c.Name.Lit))
}

// wrap attaches source position to a fatal error raised by a command.
func (e *evaluator) wrap(c *ast.CommandInvocation, err error) error {
	if fe, ok := err.(*FatalError); ok && fe.File == "" {
		fe.File = e.state.File
		fe.Line = e.state.Line
	}
	return err
}

// errorAt builds a positioned fatal error for a statement.
func (e *evaluator) errorAt(s ast.Stmt, msg string) error {
	if c := invocation(s); c != nil {
		e.state.setPosition(c)
	}
	return &FatalError{Msg: msg, File: e.state.File, Line: e.state.Line}
}

// errorf reports an error that does not stop the configure run. CMake has two
// kinds: the fatal sort that abandons the file, and this sort, which is printed,
// remembered, and stepped over so that the rest of the project is still read and
// the user learns about every problem in one pass rather than one per run.
func (e *evaluator) errorf(format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	e.state.Errors = append(e.state.Errors, msg)
	e.state.log("ERROR", msg)
	return nil
}

// fatalf builds a positioned fatal error from a format string.
func (e *evaluator) fatalf(format string, a ...any) error {
	return &FatalError{Msg: fmt.Sprintf(format, a...), File: e.state.File, Line: e.state.Line}
}

// ----------------------------------------------------------------------------
// List helpers
//
// A CMake list is a string with ';' separators. Splitting and joining it is the
// single most common operation in the language, so both live here rather than
// being re-derived in every command.

// SplitList splits a CMake list value into its elements.
func SplitList(v string) []string { return expr.SplitList(v) }

// JoinList joins elements into a CMake list value.
func JoinList(elems []string) string { return expr.JoinList(elems) }

// Commands returns the names of every built-in command, sorted.
//
// The command table is assembled from an init in each cmd_*.go file, which
// keeps a command's registration next to its implementation but means nothing
// checks that the table is complete. This is what lets a test assert that it is.
func Commands() []string {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
