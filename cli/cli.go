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
	"strconv"
	"strings"

	cmake "github.com/vertex-language/go-cmake"
	"github.com/vertex-language/go-cmake/build"
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
	case "--version", "-version", "/V":
		fmt.Fprintf(e.Out, "cmake version %s\n\n", Version)
		fmt.Fprintln(e.Out, "CMake suite implemented in Go (github.com/vertex-language/go-cmake).")
		return 0
	case "--help", "-help", "-h", "/?":
		usage(e.Out)
		return 0
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
       cmake --build <dir> [options]
       cmake --install <dir> [options]
       cmake -E <command> [args]
       cmake -P <script>

Options:
  -S <path>            source directory
  -B <path>            build directory
  -G <generator>       build system generator (Ninja)
  -D <var>[:<type>]=<value>
                       define a cache entry
  -U <globbing_expr>   remove matching cache entries
  --preset <name>      use a configure preset
  --fresh              discard any existing cache
  --log-level <level>  ERROR, WARNING, NOTICE, STATUS, VERBOSE, DEBUG, TRACE
  -j, --parallel <n>   number of parallel jobs
  --version            print the version
`)
}

// configureOptions is the parsed form of a configure command line.
type configureOptions struct {
	source    string
	binary    string
	generator string
	preset    string
	toolchain string
	vars      map[string]string
	unset     []string
	jobs      int
	flags     cmake.Flags
	script    string
	scriptArg []string
}

func parseConfigure(e Env) (*configureOptions, error) {
	o := &configureOptions{vars: map[string]string{}, generator: "Ninja"}

	// takes reports the value of a flag written either as "-S dir" or "-Sdir".
	next := func(i *int, arg, name string) (string, error) {
		if len(arg) > len(name) {
			return arg[len(name):], nil
		}
		if *i+1 >= len(e.Args) {
			return "", fmt.Errorf("%s must be followed by a value", name)
		}
		*i++
		return e.Args[*i], nil
	}

	for i := 0; i < len(e.Args); i++ {
		arg := e.Args[i]
		var err error
		switch {
		case arg == "-P":
			if i+1 >= len(e.Args) {
				return nil, fmt.Errorf("-P must be followed by a script name")
			}
			o.script = e.Args[i+1]
			o.scriptArg = e.Args[i+2:]
			return o, nil

		case strings.HasPrefix(arg, "-S"):
			o.source, err = next(&i, arg, "-S")
		case strings.HasPrefix(arg, "-B"):
			o.binary, err = next(&i, arg, "-B")
		case strings.HasPrefix(arg, "-G"):
			o.generator, err = next(&i, arg, "-G")
		case strings.HasPrefix(arg, "-U"):
			var v string
			v, err = next(&i, arg, "-U")
			o.unset = append(o.unset, v)
		case strings.HasPrefix(arg, "-D"):
			var v string
			if v, err = next(&i, arg, "-D"); err == nil {
				name, value := parseCacheAssignment(v)
				o.vars[name] = value
			}
		case arg == "--preset" || strings.HasPrefix(arg, "--preset="):
			o.preset, err = nextLong(&i, e.Args, arg, "--preset")
		case arg == "--toolchain" || strings.HasPrefix(arg, "--toolchain="):
			o.toolchain, err = nextLong(&i, e.Args, arg, "--toolchain")
		case arg == "--log-level" || strings.HasPrefix(arg, "--log-level="):
			o.flags.LogLevel, err = nextLong(&i, e.Args, arg, "--log-level")
		case arg == "-j" || arg == "--parallel":
			var v string
			if v, err = nextLong(&i, e.Args, arg, arg); err == nil {
				o.jobs, err = strconv.Atoi(v)
			}
		case arg == "--fresh":
			o.flags.Fresh = true
		case arg == "--warn-uninitialized":
			o.flags.WarnUninitialized = true
		case arg == "-N", arg == "--trace", arg == "--trace-expand", arg == "--debug-output",
			arg == "-Wdev", arg == "-Wno-dev", arg == "-Werror=dev", arg == "-Wno-error=dev",
			arg == "--no-warn-unused-cli", arg == "--check-system-vars", arg == "--debug-find":
			// Accepted and ignored: these change diagnostics, not results.
		case strings.HasPrefix(arg, "-"):
			return nil, fmt.Errorf("unknown option %q", arg)
		default:
			// A bare path is the source directory, or an existing build
			// directory being re-configured.
			if o.source == "" {
				o.source = arg
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

// nextLong reads the value of a long option in either "--x v" or "--x=v" form.
func nextLong(i *int, args []string, arg, name string) (string, error) {
	if v, ok := strings.CutPrefix(arg, name+"="); ok {
		return v, nil
	}
	if *i+1 >= len(args) {
		return "", fmt.Errorf("%s must be followed by a value", name)
	}
	*i++
	return args[*i], nil
}

// parseCacheAssignment splits "NAME:TYPE=VALUE" into its name and value. The
// type is accepted and discarded: it documents the entry for a GUI, and this
// implementation stores every command-line entry as a string.
func parseCacheAssignment(s string) (name, value string) {
	name, value, _ = strings.Cut(s, "=")
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	return name, value
}

// runConfigure runs the configure and generate phases.
func runConfigure(ctx context.Context, e Env, o *configureOptions) int {
	if o.source == "" {
		o.source = "."
	}
	if o.binary == "" {
		o.binary = o.source
	}

	c, err := cmake.New(cmake.Config{
		Source:    o.source,
		Binary:    o.binary,
		Generator: o.generator,
		Preset:    o.preset,
		Toolchain: o.toolchain,
		Vars:      o.vars,
		Env:       e.Env,
		Jobs:      o.jobs,
		Flags:     o.flags,
		FS:        cmake.RealFS(e.Dir),
		Runner:    run.OS(),
		Out:       e.Out,
		Err:       e.Err,
	})
	if err != nil {
		fmt.Fprintf(e.Err, "CMake Error: %v\n", err)
		return 1
	}

	gen, err := c.Generate(ctx)
	if err != nil {
		report(e.Err, err)
		return 1
	}
	fmt.Fprintln(e.Out, "-- Configuring done")
	fmt.Fprintln(e.Out, "-- Generating done")
	fmt.Fprintf(e.Out, "-- Build files have been written to: %s\n", parentDir(gen.BuildFile))
	return 0
}

// runScript runs `cmake -P script.cmake`: the language with no project, no
// cache, and no build directory.
func runScript(ctx context.Context, e Env, o *configureOptions) int {
	dir := e.Dir
	if dir == "" {
		dir = "."
	}
	state := eval.NewState(dir, dir, e.Env)
	state.Runner = run.OS()
	state.LogSink = func(mode, text string) {
		switch mode {
		case "":
			fmt.Fprintln(e.Out, text)
		case "STATUS":
			fmt.Fprintln(e.Out, "-- "+text)
		case "ERROR":
			fmt.Fprintf(e.Err, "CMake Error:\n  %s\n", text)
		case "AUTHOR_WARNING", "DEPRECATION", "WARNING":
			fmt.Fprintf(e.Err, "CMake Warning:\n  %s\n", text)
		default:
			fmt.Fprintln(e.Out, text)
		}
	}
	// -- arguments after the script are visible to it as CMAKE_ARGV<n>.
	state.SetVar("CMAKE_ARGC", strconv.Itoa(len(o.scriptArg)))
	for i, a := range o.scriptArg {
		state.SetVar("CMAKE_ARGV"+strconv.Itoa(i), a)
	}

	if err := eval.EvalScript(ctx, state, scriptFS{}, o.script); err != nil {
		report(e.Err, err)
		return 1
	}
	if len(state.Errors) > 0 {
		return 1
	}
	return 0
}

// runBuild drives an already-generated build tree.
func runBuild(ctx context.Context, e Env, args []string) int {
	cfg := build.Config{Generator: "Ninja", Out: e.Out, Err: e.Err, Env: e.Env}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--target" || arg == "-t":
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				cfg.Targets = append(cfg.Targets, args[i+1])
				i++
			}
		case arg == "--config":
			if i+1 < len(args) {
				cfg.Config = args[i+1]
				i++
			}
		case arg == "-j" || arg == "--parallel":
			if i+1 < len(args) {
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					fmt.Fprintf(e.Err, "CMake Error: --parallel expects a number, got %q\n", args[i+1])
					return 1
				}
				cfg.Jobs = n
				i++
			}
		case arg == "--clean-first":
			cfg.CleanFirst = true
		case arg == "--verbose" || arg == "-v":
			cfg.Verbose = true
		case arg == "--":
			i = len(args) // the rest is for the build tool, which is us
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(e.Err, "CMake Error: unknown --build option %q\n", arg)
			return 1
		default:
			if cfg.BinaryDir == "" {
				cfg.BinaryDir = arg
			}
		}
	}
	if cfg.BinaryDir == "" {
		fmt.Fprintln(e.Err, "CMake Error: --build requires a build directory")
		return 1
	}

	cfg.Runner = run.OS()
	res, err := build.Build(ctx, cfg)
	if err != nil {
		fmt.Fprintf(e.Err, "%v\n", err)
		return 1
	}
	if res != nil && res.Failed > 0 {
		return 1
	}
	return 0
}

// runInstall reports that installing is not implemented.
//
// install() rules are collected during configure and are readable from the
// configure state, but nothing writes them into the build tree, so there is
// nothing here to act on. This used to shell out to a `cmake` binary found on
// PATH, which quietly handed the job to a different implementation of CMake
// and failed confusingly when none was installed. Saying so is better.
func runInstall(ctx context.Context, e Env, args []string) int {
	fmt.Fprintln(e.Err, "CMake Error: --install is not implemented by this cmake")
	return 1
}

// report prints an error the way CMake does, without the Go error decoration.
func report(w io.Writer, err error) {
	if fe, ok := err.(*eval.FatalError); ok {
		fmt.Fprintln(w, fe.Error())
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
