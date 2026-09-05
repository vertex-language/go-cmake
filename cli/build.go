package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vertex-language/go-cmake/build"
	"github.com/vertex-language/go-cmake/preset"
	"github.com/vertex-language/go-cmake/run"
)

// runBuild drives an already-generated build tree.
func runBuild(ctx context.Context, e Env, args []string) int {
	cfg := build.Config{Generator: "Ninja", Out: e.Out, Err: e.Err, Env: e.Env}

	// CMAKE_BUILD_PARALLEL_LEVEL is the default when no -j is given. Honouring
	// it matters because CI images set it once rather than editing every
	// invocation, and ignoring it silently serialises those builds.
	if n, ok := envInt(e.Env, "CMAKE_BUILD_PARALLEL_LEVEL"); ok {
		cfg.Jobs = n
	}

	var presetName, presetKind string
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Everything after `--` is for the native build tool. This driver is
		// the native build tool and takes no options of its own, so the rest is
		// consumed rather than passed on.
		if arg == "--" {
			break
		}

		switch {
		case arg == "--target" || arg == "-t":
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				cfg.Targets = append(cfg.Targets, args[i+1])
				i++
			}
		case strings.HasPrefix(arg, "--target="):
			cfg.Targets = append(cfg.Targets, strings.TrimPrefix(arg, "--target="))
		case arg == "--config":
			if v, ok := valueFor(&i, args, arg); ok {
				cfg.Config = v
			} else {
				return missingValue(e, arg)
			}
		case strings.HasPrefix(arg, "--config="):
			cfg.Config = strings.TrimPrefix(arg, "--config=")

		case arg == "-j" || arg == "--parallel":
			// The job count is optional: bare -j means "the build tool's
			// default", which for this driver is one job per CPU.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				n, err := strconv.Atoi(args[i+1])
				if err != nil {
					fmt.Fprintf(e.Err, "CMake Error: --parallel expects a number, got %q\n", args[i+1])
					return 1
				}
				cfg.Jobs = n
				i++
			} else {
				cfg.Jobs = 0
			}
		case strings.HasPrefix(arg, "-j"), strings.HasPrefix(arg, "--parallel="):
			v := strings.TrimPrefix(strings.TrimPrefix(arg, "--parallel="), "-j")
			n, err := strconv.Atoi(v)
			if err != nil {
				fmt.Fprintf(e.Err, "CMake Error: --parallel expects a number, got %q\n", v)
				return 1
			}
			cfg.Jobs = n

		case arg == "--preset" || strings.HasPrefix(arg, "--preset="):
			v, ok := attachedOrNext(&i, args, arg, "--preset")
			if !ok {
				return missingValue(e, arg)
			}
			presetName = v
		case arg == "--list-presets" || strings.HasPrefix(arg, "--list-presets="):
			presetKind = strings.TrimPrefix(arg, "--list-presets")
			presetKind = strings.TrimPrefix(presetKind, "=")
			if presetKind == "" {
				presetKind = "build"
			}

		case arg == "--clean-first":
			cfg.CleanFirst = true
		case arg == "--verbose" || arg == "-v":
			cfg.Verbose = true

		case arg == "--presets-file" || strings.HasPrefix(arg, "--presets-file="):
			fmt.Fprintln(e.Err, "CMake Error: --presets-file is not supported: an alternate presets file is not implemented")
			return 1
		case strings.HasPrefix(arg, "--resolve-package-references"):
			fmt.Fprintln(e.Err, "CMake Error: --resolve-package-references is not supported: package reference restore is not implemented")
			return 1

		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(e.Err, "CMake Error: unknown --build option %q\n", arg)
			return 1
		default:
			if cfg.BinaryDir == "" {
				cfg.BinaryDir = arg
			}
		}
	}

	if presetKind != "" {
		return listBuildPresets(e, presetKind)
	}
	if presetName != "" {
		if code := applyBuildPreset(e, presetName, &cfg); code != 0 {
			return code
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

// applyBuildPreset fills in the settings a build preset names, leaving anything
// the command line already set alone: an explicit flag beats a preset.
func applyBuildPreset(e Env, name string, cfg *build.Config) int {
	dir := e.Dir
	if dir == "" {
		dir = "."
	}
	file, err := preset.Load(dir)
	if err != nil || file == nil {
		fmt.Fprintf(e.Err, "CMake Error: could not read presets from %s\n", dir)
		return 1
	}
	res, err := file.ResolveBuild(name)
	if err != nil {
		fmt.Fprintf(e.Err, "CMake Error: %v\n", err)
		return 1
	}
	if cfg.BinaryDir == "" {
		cfg.BinaryDir = res.BinaryDir
	}
	if len(cfg.Targets) == 0 {
		cfg.Targets = res.Targets
	}
	if cfg.Config == "" {
		cfg.Config = res.Config
	}
	if cfg.Jobs == 0 {
		cfg.Jobs = res.Jobs
	}
	cfg.Verbose = cfg.Verbose || res.Verbose
	cfg.CleanFirst = cfg.CleanFirst || res.CleanFirst
	return 0
}

func listBuildPresets(e Env, kind string) int {
	o := &configureOptions{listPreset: kind, source: e.Dir}
	return listPresets(e, o)
}

// runInstall reports that installing is not implemented.
//
// install() rules are collected during configure and readable from the
// configure state, but nothing writes them into the build tree, so there is
// nothing here to act on. The options are still parsed, so that a wrong one is
// reported as a wrong option rather than swallowed by a blanket refusal.
func runInstall(_ context.Context, e Env, args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--prefix", arg == "--component", arg == "--config",
			arg == "--default-directory-permissions":
			if _, ok := valueFor(&i, args, arg); !ok {
				return missingValue(e, arg)
			}
		case strings.HasPrefix(arg, "--prefix="), strings.HasPrefix(arg, "--component="),
			strings.HasPrefix(arg, "--config="),
			strings.HasPrefix(arg, "--default-directory-permissions="):
		case arg == "--strip", arg == "-v", arg == "--verbose":
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(e.Err, "CMake Error: unknown --install option %q\n", arg)
			return 1
		}
	}
	fmt.Fprintln(e.Err, "CMake Error: --install is not implemented by this cmake.")
	fmt.Fprintln(e.Err, "  install() rules are recorded during configure and can be read from the")
	fmt.Fprintln(e.Err, "  configure state, but they are not written into the build tree.")
	return 1
}

// valueFor takes the argument after a flag, advancing the index.
func valueFor(i *int, args []string, _ string) (string, bool) {
	if *i+1 >= len(args) {
		return "", false
	}
	*i++
	return args[*i], true
}

// attachedOrNext reads a value written either as "--x=v" or "--x v".
func attachedOrNext(i *int, args []string, arg, name string) (string, bool) {
	if v, ok := strings.CutPrefix(arg, name+"="); ok {
		return v, true
	}
	return valueFor(i, args, arg)
}

func missingValue(e Env, arg string) int {
	fmt.Fprintf(e.Err, "CMake Error: %s must be followed by a value\n", arg)
	return 1
}

// envInt reads a numeric environment variable from the injected environment.
func envInt(env []string, name string) (int, bool) {
	prefix := name + "="
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, prefix); ok {
			if n, err := strconv.Atoi(v); err == nil {
				return n, true
			}
			return 0, false
		}
	}
	return 0, false
}
