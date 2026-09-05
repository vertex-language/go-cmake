// Package cli is the command-line front end: it turns arguments into a
// [cmake.Config] and runs the phase the user asked for.
//
// Every effect reaches it through [Env], so the whole command line is testable
// without a process: a test supplies arguments, a working directory, an
// environment, and three streams, and reads back an exit status.
package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/run"
)

// Env is everything the command line touches outside itself.
type Env struct {
	Args []string
	Dir  string
	Env  []string
	In   io.Reader
	Out  io.Writer
	Err  io.Writer
}

// Version is the CMake version this implementation reports. It is the version
// of the language and command line being implemented, not of this program.
const Version = "4.4.3"

// Main runs the command line and returns the process exit status.
func Main(ctx context.Context, e Env) int {
	if len(e.Args) == 0 {
		usage(e.Out)
		return 1
	}

	// The three sub-command modes are recognised before anything else, because
	// each takes the rest of the command line on its own terms.
	switch e.Args[0] {
	case "-E":
		return runToolMode(ctx, e, e.Args[1:])
	case "--build":
		return runBuild(ctx, e, e.Args[1:])
	case "--install":
		return runInstall(ctx, e, e.Args[1:])
	case "--help", "-help", "-h", "-H", "-usage", "/?":
		usage(e.Out)
		return 0
	}

	// --version and the --help-* family take an optional =<value>, so they are
	// matched on the name rather than the whole argument.
	switch name, _, _ := strings.Cut(e.Args[0], "="); name {
	case "--version", "-version", "/V", "/version":
		return printVersion(e.Out, e.Args[0])
	}
	if code, handled := runHelp(e, e.Args); handled {
		return code
	}

	opts, err := parseConfigure(e)
	if err != nil {
		fmt.Fprintf(e.Err, "CMake Error: %v\n", err)
		return 1
	}
	if opts.script != "" {
		return runScript(ctx, e, opts)
	}
	return runConfigure(ctx, e, opts)
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Usage: cmake [options] <path-to-source>
       cmake [options] <path-to-existing-build>
       cmake [options] -S <path-to-source> -B <path-to-build>
       cmake --build <dir> [options]
       cmake --install <dir> [options]
       cmake -E <command> [args]
       cmake -P <script> [args]

Options:
  -S <path>                    source directory
  -B <path>                    build directory
  -C <initial-cache>           pre-load a script to populate the cache
  -D <var>[:<type>]=<value>    create or update a cache entry
  -U <globbing_expr>           remove matching cache entries
  -G <generator>               build system generator (Ninja)
  --toolchain <file>           CMAKE_TOOLCHAIN_FILE
  --install-prefix <dir>       CMAKE_INSTALL_PREFIX
  --preset <name>              use a configure preset
  --list-presets[=<type>]      list available presets
  -L[A][H]                     list cache variables; A includes advanced,
                               H includes help text
  -LR[A][H] <regex>            list cache variables matching a regex
  -N                           view mode: configure without generating
  --log-level <level>          ERROR, WARNING, NOTICE, STATUS, VERBOSE, DEBUG, TRACE
  --fresh                      discard the build directory's cache first
  -j, --parallel [<n>]         number of parallel jobs
  -E <command>                 command mode; run "cmake -E" for a summary
  -P <script>                  script mode
  --help-command-list          list the commands this cmake implements
  --version[=json-v1]          print the version
  -h, --help                   print this message

Options that select diagnostics -- -W<category>, --trace, --debug-find,
--warn-uninitialized and the rest -- are accepted and ignored.
`)
}

// runScript runs `cmake -P script.cmake`: the language with no project, no
// cache, and no build directory.
func runScript(ctx context.Context, e Env, o *configureOptions) int {
	dir := e.Dir
	if dir == "" {
		dir = "."
	}
	// The absolute form, because it is what a diagnostic measures the script's
	// own path against: cmake names "s.cmake", not the path it resolved to.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	state := eval.NewState(dir, dir, e.Env)
	state.Runner = run.OS()
	state.Out, state.Err = e.Out, e.Err
	state.LogSink = messageSink(e, state)
	// -- arguments after the script are visible to it as CMAKE_ARGV<n>.
	state.SetVar("CMAKE_ARGC", strconv.Itoa(len(o.scriptArg)))
	for i, a := range o.scriptArg {
		state.SetVar("CMAKE_ARGV"+strconv.Itoa(i), a)
	}

	// The script is named relative to the working directory, not to wherever
	// the process happens to have been started.
	script := o.script
	if !filepath.IsAbs(script) {
		script = filepath.Join(dir, script)
	}
	if err := eval.EvalScript(ctx, state, scriptFS{}, script); err != nil {
		report(e.Err, err)
		return 1
	}
	if len(state.Errors) > 0 {
		return 1
	}
	return 0
}

// report prints an error the way CMake does, without the Go error decoration.
// messageSink prints message() output the way CMake does.
//
// STATUS and the plain form go to stdout unadorned; everything that is a
// complaint goes to stderr under a banner naming the file, the line and the
// command, with the text filled to CMake's column. The author and deprecation
// warnings carry a footer saying who they are for, which is how a project
// developer tells their own warnings from a user's.
func messageSink(e Env, state *eval.State) func(mode, text string) {
	return func(mode, text string) {
		banner := func(kind string) string {
			if state.File == "" {
				return kind
			}
			at := fmt.Sprintf("%s at %s:%d", kind, eval.ReportPath(state.SourceDir, state.File), state.Line)
			if state.Cmd != "" {
				at += " (" + state.Cmd + ")"
			}
			return at
		}
		switch mode {
		case "STATUS":
			fmt.Fprintln(e.Out, "-- "+text)
		case "ERROR":
			fmt.Fprint(e.Err, eval.Diagnostic(banner("CMake Error"), text), "\n\n")
		case "WARNING":
			fmt.Fprint(e.Err, eval.Diagnostic(banner("CMake Warning"), text), "\n\n")
		case "AUTHOR_WARNING":
			fmt.Fprint(e.Err, eval.Diagnostic(banner("CMake Warning (author)"), text),
				"This warning is for project developers.  Use -Wno-author to suppress it.\n\n")
		case "DEPRECATION":
			fmt.Fprint(e.Err, eval.Diagnostic(banner("CMake Warning (deprecated)"), text),
				"This warning is for project developers.  Use -Wno-author or -Wno-deprecated\nto suppress it.\n\n")
		case "POLICY":
			fmt.Fprint(e.Err, eval.Diagnostic(banner("CMake Warning (policy)"), text),
				"This warning is for project developers.  Use -Wno-author or -Wno-policy to\nsuppress it.\n\n")
		default:
			fmt.Fprintln(e.Out, text)
		}
	}
}

func report(w io.Writer, err error) {
	if fe, ok := err.(*eval.FatalError); ok {
		// The two blank lines are CMake's: every diagnostic it writes is
		// followed by them, and a log that has them everywhere but here reads
		// as if something were cut off.
		fmt.Fprintf(w, "%s\n\n\n", fe.Error())
		return
	}
	fmt.Fprintf(w, "CMake Error: %v\n", err)
}

// parentDir returns the directory to create for a path being written. It is
// deliberately not eval's dirOf: this one yields "." for a bare filename so the
// result can be passed to MkdirAll, where CMake's PARENT_PATH semantics yield
// "" and "/" for the same inputs.
func parentDir(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}
