package cli

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"

	"github.com/vertex-language/go-cmake/ctest"
	"github.com/vertex-language/go-cmake/run"
)

// CTestMain runs the `ctest` command line and returns the process exit status.
//
// It is a separate entry point rather than a mode of [Main] because ctest is a
// separate program: it takes a build tree, not a source tree, and its options
// mean different things from cmake's. `-R` selects tests here and is not an
// option there at all.
func CTestMain(ctx context.Context, e Env) int {
	cfg := ctest.Config{
		Runner: run.OS(),
		Env:    e.Env,
		Out:    e.Out,
		Err:    e.Err,
	}
	dir := e.Dir
	if dir == "" {
		dir = "."
	}

	for i := 0; i < len(e.Args); i++ {
		arg := e.Args[i]
		value := func(name string) (string, bool) {
			if v, ok := strings.CutPrefix(arg, name+"="); ok {
				return v, true
			}
			if i+1 < len(e.Args) {
				i++
				return e.Args[i], true
			}
			return "", false
		}

		switch name, _, _ := strings.Cut(arg, "="); {
		case name == "--test-dir":
			v, ok := value("--test-dir")
			if !ok {
				return ctestMissing(e, arg)
			}
			dir = v
		case name == "-R" || name == "--tests-regex":
			v, ok := value(name)
			if !ok {
				return ctestMissing(e, arg)
			}
			cfg.Include = v
		case name == "-E" || name == "--exclude-regex":
			v, ok := value(name)
			if !ok {
				return ctestMissing(e, arg)
			}
			cfg.Exclude = v
		case name == "-L" || name == "--label-regex":
			v, ok := value(name)
			if !ok {
				return ctestMissing(e, arg)
			}
			cfg.IncludeLabel = v
		case name == "-LE" || name == "--label-exclude":
			v, ok := value(name)
			if !ok {
				return ctestMissing(e, arg)
			}
			cfg.ExcludeLabel = v
		case name == "-j" || name == "--parallel":
			// The job count is optional, as it is for cmake --build.
			if i+1 < len(e.Args) && !strings.HasPrefix(e.Args[i+1], "-") {
				i++
				n, err := strconv.Atoi(e.Args[i])
				if err != nil {
					fmt.Fprintf(e.Err, "ctest: --parallel expects a number, got %q\n", e.Args[i])
					return 1
				}
				cfg.Jobs = n
			} else {
				cfg.Jobs = runtime.NumCPU()
			}
		case strings.HasPrefix(arg, "-j"):
			n, err := strconv.Atoi(arg[2:])
			if err != nil {
				fmt.Fprintf(e.Err, "ctest: --parallel expects a number, got %q\n", arg[2:])
				return 1
			}
			cfg.Jobs = n
		case name == "--repeat-until-fail" || name == "--repeat":
			if _, ok := value(name); !ok {
				return ctestMissing(e, arg)
			}
			// Accepted; a test runs once here.
		case arg == "--output-on-failure":
			cfg.OutputOnFailure = true
		case arg == "--stop-on-failure":
			cfg.StopOnFailure = true
		case arg == "-N" || arg == "--show-only":
			cfg.ShowOnly = true
		case strings.HasPrefix(arg, "--show-only="):
			cfg.ShowOnly = true
		case arg == "-V" || arg == "--verbose" || arg == "-VV" || arg == "--extra-verbose":
			cfg.OutputOnFailure = true
		case arg == "-Q" || arg == "--quiet":
			cfg.Out = nil
		case name == "-C" || name == "--build-config":
			if _, ok := value(name); !ok {
				return ctestMissing(e, arg)
			}
			// Single-configuration build trees have one configuration.
		case arg == "--help" || arg == "-h":
			ctestUsage(e.Out)
			return 0
		case arg == "--version":
			fmt.Fprintf(e.Out, "ctest version %s\n", Version)
			return 0
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(e.Err, "ctest: unknown option %q\n", arg)
			return 1
		default:
			// A bare path is the build directory, as `ctest <dir>` means.
			dir = arg
		}
	}

	cfg.BinaryDir = dir
	summary, err := ctest.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(e.Err, "ctest: %v\n", err)
		return 1
	}
	if summary.Failed > 0 {
		return 1
	}
	return 0
}

func ctestMissing(e Env, arg string) int {
	fmt.Fprintf(e.Err, "ctest: %s must be followed by a value\n", arg)
	return 1
}

func ctestUsage(w io.Writer) {
	fmt.Fprint(w, `Usage: ctest [options] [<build-dir>]

Options:
  --test-dir <dir>       build directory to test
  -R, --tests-regex <r>  run only tests whose name matches
  -E, --exclude-regex <r>
                         skip tests whose name matches
  -L, --label-regex <r>  run only tests with a matching label
  -LE, --label-exclude <r>
                         skip tests with a matching label
  -j, --parallel [<n>]   run tests concurrently
  -N, --show-only        list the tests without running them
  --output-on-failure    print a failing test's output
  --stop-on-failure      stop at the first failure
  -V, --verbose          print test output
  -Q, --quiet            print nothing
  --version              print the version
`)
}
