package cli

import (
	"fmt"
	"strconv"
	"strings"

	cmake "github.com/vertex-language/go-cmake"
)

// The configure command line is a table rather than a switch.
//
// CMake accepts about sixty options in configure mode, and only a dozen of them
// change what gets built. The rest select diagnostics. A switch statement makes
// those two groups look alike and makes the third group -- options CMake has
// that this implementation does not -- indistinguishable from a typo. Getting
// that wrong is what breaks a developer's workflow: a script that passes
// `-Wno-dev` to silence a warning should not die, and one that passes
// `--system-information` expecting a report should not be told the flag does
// not exist.
//
// So each option says which group it is in, and there is a test that walks this
// table against the options the real cmake reports.

// arity says how an option takes its value.
type arity int

const (
	noValue       arity = iota // --fresh
	attachedValue              // -Sdir or -S dir, --preset=x or --preset x
	optionalValue              // --parallel, or --parallel 8
)

// option is one entry of the configure command line.
type option struct {
	// name is the option as written, without any value.
	name string

	arity arity

	// apply records the option. A nil apply means the option is accepted and
	// ignored; why is stated in note.
	apply func(o *configureOptions, value string) error

	// note explains an option that is accepted without being honoured, so that
	// the reason survives next to the decision rather than in a commit message.
	note string

	// refuse is set for options CMake has that this implementation does not.
	// They fail with their own message rather than as an unknown option,
	// because "not implemented" and "no such flag" send a reader to different
	// places.
	refuse string
}

// configureOptions is the parsed form of a configure command line.
type configureOptions struct {
	source     string
	binary     string
	generator  string
	preset     string
	toolchain  string
	vars       map[string]string
	unset      []string
	initCache  []string
	jobs       int
	flags      cmake.Flags
	script     string
	scriptArg  []string
	listCache  bool   // -L
	listAll    bool   // the A in -LA
	listHelp   bool   // the H in -LH
	listRegex  string // -LR <regex>
	listPreset string // --list-presets[=<type>], "" when not asked for
	viewOnly   bool   // -N
}

// configureTable is every option cmake accepts before the source directory.
var configureTable = []option{
	// Options that decide what is built.
	{name: "-S", arity: attachedValue, apply: func(o *configureOptions, v string) error { o.source = v; return nil }},
	{name: "-B", arity: attachedValue, apply: func(o *configureOptions, v string) error { o.binary = v; return nil }},
	{name: "-G", arity: attachedValue, apply: func(o *configureOptions, v string) error { o.generator = v; return nil }},
	{name: "-C", arity: attachedValue, apply: func(o *configureOptions, v string) error {
		o.initCache = append(o.initCache, v)
		return nil
	}},
	{name: "-D", arity: attachedValue, apply: func(o *configureOptions, v string) error {
		name, value := parseCacheAssignment(v)
		o.vars[name] = value
		return nil
	}},
	{name: "-U", arity: attachedValue, apply: func(o *configureOptions, v string) error {
		o.unset = append(o.unset, v)
		return nil
	}},
	{name: "--preset", arity: attachedValue, apply: func(o *configureOptions, v string) error { o.preset = v; return nil }},
	{name: "--toolchain", arity: attachedValue, apply: func(o *configureOptions, v string) error { o.toolchain = v; return nil }},
	{name: "--install-prefix", arity: attachedValue, apply: func(o *configureOptions, v string) error {
		o.vars["CMAKE_INSTALL_PREFIX"] = v
		return nil
	}},
	{name: "--log-level", arity: attachedValue, apply: func(o *configureOptions, v string) error {
		o.flags.LogLevel = v
		return nil
	}},
	// --loglevel is the older spelling, still accepted by cmake and still in
	// scripts written against it.
	{name: "--loglevel", arity: attachedValue, apply: func(o *configureOptions, v string) error {
		o.flags.LogLevel = v
		return nil
	}},
	{name: "-j", arity: optionalValue, apply: setJobs},
	{name: "--parallel", arity: optionalValue, apply: setJobs},
	{name: "--list-presets", arity: optionalValue, apply: func(o *configureOptions, v string) error {
		if v == "" {
			v = "configure"
		}
		o.listPreset = v
		return nil
	}},
	{name: "-N", apply: func(o *configureOptions, _ string) error { o.viewOnly = true; return nil }},

	// A toolset and a platform are Visual Studio and Xcode concepts. This
	// implementation generates Ninja, which has neither, so recording them
	// would be recording something that can never be read.
	{name: "-T", arity: attachedValue, note: "only the Visual Studio and Xcode generators have toolsets"},
	{name: "-A", arity: attachedValue, note: "only the Visual Studio and Xcode generators have platforms"},

	{name: "--fresh", apply: func(o *configureOptions, _ string) error {
		o.flags.Fresh = true
		return nil
	}},

	// Diagnostics. None of these change what is built.
	{name: "--warn-uninitialized", note: "diagnostics only"},
	{name: "--check-system-vars", note: "diagnostics only"},
	{name: "--no-warn-unused-cli", note: "diagnostics only"},
	{name: "--debug-output", note: "diagnostics only"},
	{name: "--debug-find", note: "diagnostics only"},
	{name: "--debug-find-pkg", arity: attachedValue, note: "diagnostics only"},
	{name: "--debug-find-var", arity: attachedValue, note: "diagnostics only"},
	{name: "--debug-trycompile", note: "try_compile is not implemented, so there is no tree to keep"},
	{name: "--log-context", note: "diagnostics only"},
	{name: "--trace", note: "diagnostics only"},
	{name: "--trace-expand", note: "diagnostics only"},
	{name: "--trace-format", arity: attachedValue, note: "diagnostics only"},
	{name: "--trace-source", arity: attachedValue, note: "diagnostics only"},
	{name: "--trace-redirect", arity: attachedValue, note: "diagnostics only"},
	{name: "--profiling-format", arity: attachedValue, note: "diagnostics only"},
	{name: "--profiling-output", arity: attachedValue, note: "diagnostics only"},
	{name: "--graphviz", arity: attachedValue, note: "the dependency graph is available from Generate as a value"},
	{name: "--compile-no-warning-as-error", note: "COMPILE_WARNING_AS_ERROR is not applied"},
	{name: "--link-no-warning-as-error", note: "LINK_WARNING_AS_ERROR is not applied"},

	// Options cmake has that this implementation does not.
	{name: "--workflow", arity: optionalValue, refuse: "workflow presets are not implemented"},
	{name: "--open", arity: attachedValue, refuse: "there is no IDE project to open; this implementation generates Ninja"},
	{name: "--find-package", refuse: "the legacy find-package mode is not implemented"},
	{name: "--system-information", arity: optionalValue, refuse: "--system-information is not implemented"},
	{name: "--print-config-dir", refuse: "there is no user-wide config directory"},
	{name: "--project-file", arity: attachedValue, refuse: "an alternate project file name is not implemented"},
	{name: "--presets-file", arity: attachedValue, refuse: "an alternate presets file is not implemented"},
	{name: "--resolve-package-references", arity: attachedValue, refuse: "package reference restore is not implemented"},
}

func setJobs(o *configureOptions, v string) error {
	if v == "" {
		// `--parallel` with no number means the build tool's default, which for
		// this driver is one job per CPU. Zero says so.
		o.jobs = 0
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%q is not a number of jobs", v)
	}
	o.jobs = n
	return nil
}

// lookupOption finds the table entry an argument names, and returns the value
// attached to it if the argument carried one.
func lookupOption(arg string) (opt *option, value string, attached, ok bool) {
	for i := range configureTable {
		o := &configureTable[i]
		if arg == o.name {
			return o, "", false, true
		}
		if o.arity == noValue {
			continue
		}
		// A long option attaches its value with '=', a short one by adjacency.
		if strings.HasPrefix(o.name, "--") {
			if v, found := strings.CutPrefix(arg, o.name+"="); found {
				return o, v, true, true
			}
			continue
		}
		if len(arg) > len(o.name) && strings.HasPrefix(arg, o.name) {
			return o, arg[len(o.name):], true, true
		}
	}
	return nil, "", false, false
}

func parseConfigure(e Env) (*configureOptions, error) {
	// The generator is deliberately left empty: only -G sets it, so that a
	// preset's generator can still apply and the facade's default is the one
	// answer to "what if nobody said".
	o := &configureOptions{vars: map[string]string{}}

	for i := 0; i < len(e.Args); i++ {
		arg := e.Args[i]

		// -P ends the configure command line: everything after the script name
		// belongs to the script.
		if arg == "-P" {
			if i+1 >= len(e.Args) {
				return nil, fmt.Errorf("-P must be followed by a script name")
			}
			o.script = e.Args[i+1]
			o.scriptArg = e.Args[i+2:]
			return o, nil
		}

		// -L, -LA, -LH, -LAH list the cache; -LR takes a regex.
		if handled, err := o.parseListCache(&i, e.Args, arg); handled {
			if err != nil {
				return nil, err
			}
			continue
		}

		// -W<category> and its three negations select warnings by name. The
		// category is open-ended, so this is a prefix rule rather than a list.
		if arg == "-W" || arg == "-Wno-" || arg == "-Werror=" || arg == "-Wno-error=" {
			return nil, fmt.Errorf("-W must be followed with [no-]<name>")
		}
		if isWarningOption(arg) {
			continue
		}

		opt, value, attached, found := lookupOption(arg)
		if !found {
			if strings.HasPrefix(arg, "-") {
				return nil, fmt.Errorf("unknown option %q", arg)
			}
			// A bare path is the source directory, or an existing build
			// directory being re-configured.
			if o.source == "" {
				o.source = arg
			}
			continue
		}
		if opt.refuse != "" {
			return nil, fmt.Errorf("%s is not supported: %s", opt.name, opt.refuse)
		}

		if !attached {
			switch opt.arity {
			case attachedValue:
				if i+1 >= len(e.Args) {
					return nil, fmt.Errorf("%s must be followed by a value", opt.name)
				}
				i++
				value = e.Args[i]
			case optionalValue:
				// A following argument belongs to the option only if it does not
				// look like one itself.
				if i+1 < len(e.Args) && !strings.HasPrefix(e.Args[i+1], "-") {
					i++
					value = e.Args[i]
				}
			}
		}
		if opt.apply != nil {
			if err := opt.apply(o, value); err != nil {
				return nil, fmt.Errorf("%s: %w", opt.name, err)
			}
		}
	}
	return o, nil
}

// parseListCache handles the -L family, whose letters are a set rather than a
// word: -LAH, -LHA, and -LA -H all mean the same thing to cmake.
func (o *configureOptions) parseListCache(i *int, args []string, arg string) (bool, error) {
	if !strings.HasPrefix(arg, "-L") {
		return false, nil
	}
	rest := arg[2:]
	regex := strings.HasPrefix(rest, "R")
	if regex {
		rest = rest[1:]
	}
	for _, c := range rest {
		switch c {
		case 'A':
			o.listAll = true
		case 'H':
			o.listHelp = true
		default:
			return false, nil // not an -L option after all
		}
	}
	o.listCache = true
	if regex {
		if *i+1 >= len(args) {
			return true, fmt.Errorf("%s must be followed by a regular expression", arg)
		}
		*i++
		o.listRegex = args[*i]
	}
	return true, nil
}

// isWarningOption reports whether an argument selects a warning category.
// Every form is accepted and ignored: this implementation emits the developer
// warnings CMake does not let you turn off anyway, and none of the categories
// change what is generated.
func isWarningOption(arg string) bool {
	for _, prefix := range []string{"-Wno-error=", "-Werror=", "-Wno-", "-W"} {
		if rest, ok := strings.CutPrefix(arg, prefix); ok && rest != "" {
			return true
		}
	}
	return false
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
