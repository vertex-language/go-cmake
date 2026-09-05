package eval

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vertex-language/go-cmake/parser"
	"github.com/vertex-language/go-cmake/token"
)

// maxIncludeDepth bounds include() recursion. CMake has no such limit and will
// recurse until the process stack is exhausted; a library must not.
const maxIncludeDepth = 200

// EvalFile parses and evaluates one CMake file, maintaining the
// CMAKE_CURRENT_LIST_* variables that scripts use to locate themselves.
func (e *evaluator) evalFile(ctx context.Context, path string) error {
	if len(e.state.Includes) >= maxIncludeDepth {
		return e.fatalf("include depth of %d exceeded while including %s", maxIncludeDepth, path)
	}
	src, err := e.fs.ReadFile(path)
	if err != nil {
		return e.fatalf("could not read %s: %v", path, err)
	}
	return e.evalBytes(ctx, src, path)
}

// evalSource evaluates a string of CMake code, as cmake_language(EVAL CODE)
// and string(CONFIGURE) style callers need.
func (e *evaluator) evalSource(ctx context.Context, src, name string) error {
	return e.evalBytes(ctx, []byte(src), name)
}

func (e *evaluator) evalBytes(ctx context.Context, src []byte, path string) error {
	if e.state.FileSet == nil {
		e.state.FileSet = token.NewFileSet()
	}
	f, err := parser.ParseFile(e.state.FileSet, path, src)
	if err != nil {
		return &FatalError{Msg: err.Error(), File: path}
	}

	// CMAKE_CURRENT_LIST_FILE and _DIR track the file being read, not the
	// directory being configured, which is what lets a shared .cmake module
	// find resources next to itself.
	savedFile, hadFile := e.state.Current.Get("CMAKE_CURRENT_LIST_FILE")
	savedDir, hadDir := e.state.Current.Get("CMAKE_CURRENT_LIST_DIR")
	e.state.Current.Set("CMAKE_CURRENT_LIST_FILE", slashPath(path))
	e.state.Current.Set("CMAKE_CURRENT_LIST_DIR", slashPath(dirOf(path)))
	e.state.Includes = append(e.state.Includes, path)

	defer func() {
		e.state.Includes = e.state.Includes[:len(e.state.Includes)-1]
		restore := func(name, val string, had bool) {
			if had {
				e.state.Current.Set(name, val)
			} else {
				e.state.Current.Unset(name)
			}
		}
		restore("CMAKE_CURRENT_LIST_FILE", savedFile, hadFile)
		restore("CMAKE_CURRENT_LIST_DIR", savedDir, hadDir)
	}()

	if err := e.evalStmts(ctx, f.Stmts); err != nil {
		return err
	}
	return e.flushDeferred(ctx)
}

// flushDeferred runs the calls recorded by cmake_language(DEFER CALL ...) for
// the directory that is finishing.
func (e *evaluator) flushDeferred(ctx context.Context) error {
	d := e.state.Dir()
	if d == nil || len(d.Deferred) == 0 {
		return nil
	}
	pending := d.Deferred
	d.Deferred = nil
	for _, call := range pending {
		if err := e.callByName(ctx, call[0], Strings(call[1:]...)); err != nil {
			return err
		}
	}
	return nil
}

// callByName invokes a command by name with already-expanded arguments, which
// is what cmake_language(CALL) and the deferred-call queue need.
func (e *evaluator) callByName(ctx context.Context, name string, args []Arg) error {
	lower := strings.ToLower(name)
	if fn, ok := e.state.Functions[lower]; ok {
		return e.callFunction(ctx, fn, args)
	}
	if mac, ok := e.state.Macros[lower]; ok {
		return e.callMacro(ctx, mac, args)
	}
	if handler, ok := commands[lower]; ok {
		return handler(ctx, e, args)
	}
	return e.fatalf("Unknown CMake command %q.", name)
}

// EvalProject configures a whole source tree: it evaluates the top-level
// CMakeLists.txt with the root directory already on the stack.
func EvalProject(ctx context.Context, state *State, filesystem FS) error {
	e := &evaluator{state: state, fs: filesystem}
	listFile := joinPath(state.SourceDir, "CMakeLists.txt")
	if _, err := filesystem.Stat(listFile); err != nil {
		return &FatalError{Msg: "The source directory\n\n  " + state.SourceDir +
			"\n\ndoes not appear to contain CMakeLists.txt."}
	}
	err := e.evalFile(ctx, listFile)
	if _, ok := err.(returnSignal); ok {
		return nil
	}
	return err
}

func numCPU() int     { return runtime.NumCPU() }
func isWindows() bool { return runtime.GOOS == "windows" }

// EvalScript runs a single file in script mode, as `cmake -P` does. There is no
// project, no cache file, and no binary directory: the script's own directory
// stands in for both, which is what makes CMAKE_CURRENT_SOURCE_DIR meaningful
// in a standalone script.
func EvalScript(ctx context.Context, state *State, filesystem FS, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = slashPath(abs)
	e := &evaluator{state: state, fs: filesystem}
	state.Current.Set("CMAKE_SCRIPT_MODE_FILE", abs)
	err = e.evalFile(ctx, abs)
	if _, ok := err.(returnSignal); ok {
		return nil
	}
	return err
}

// EvalCacheFile evaluates a -C initial-cache script into an existing state.
//
// It differs from [EvalScript] in what it leaves behind rather than in how it
// runs: no CMAKE_SCRIPT_MODE_FILE is set, because the script is not the thing
// being run -- it is a prelude to a project, whose whole purpose is the
// set(... CACHE) calls it makes before the first CMakeLists.txt line executes.
func EvalCacheFile(ctx context.Context, state *State, filesystem FS, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	e := &evaluator{state: state, fs: filesystem}
	err = e.evalFile(ctx, slashPath(abs))
	if _, ok := err.(returnSignal); ok {
		return nil
	}
	return err
}
