package cli

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	cmake "github.com/vertex-language/go-cmake"
	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/preset"
	"github.com/vertex-language/go-cmake/run"
)

// runConfigure runs the configure phase and, unless -N was given, generate.
func runConfigure(ctx context.Context, e Env, o *configureOptions) int {
	if o.listPreset != "" {
		return listPresets(e, o)
	}
	if o.source == "" {
		o.source = "."
	}
	// The binary directory is deliberately left empty when a preset was named:
	// the preset's binaryDir has to win over the default, and defaulting here
	// would make it look like the user asked for the source directory.
	if o.binary == "" && o.preset == "" {
		o.binary = o.source
	}

	c, err := cmake.New(cmake.Config{
		Source:       o.source,
		Binary:       o.binary,
		Generator:    o.generator,
		Preset:       o.preset,
		Toolchain:    o.toolchain,
		Vars:         o.vars,
		Unset:        o.unset,
		InitialCache: o.initCache,
		Env:          e.Env,
		Jobs:         o.jobs,
		Flags:        o.flags,
		FS:           cmake.RealFS(e.Dir),
		Runner:       run.OS(),
		Downloader:   cmake.HTTPDownloader(),
		Out:          e.Out,
		Err:          e.Err,
	})
	if err != nil {
		fmt.Fprintf(e.Err, "CMake Error: %v\n", err)
		return 1
	}

	// -N is view mode: configure, report, and write nothing. It is how a script
	// inspects a project's cache without producing build files, so generate
	// must not run.
	if o.viewOnly {
		res, err := c.Configure(ctx)
		if err != nil {
			report(e.Err, err)
			return 1
		}
		if o.listCache {
			printCache(e, o, res.State)
		}
		return exitFor(res.State)
	}

	gen, err := c.Generate(ctx)
	if err != nil {
		report(e.Err, err)
		return 1
	}
	fmt.Fprintln(e.Out, "-- Configuring done")
	fmt.Fprintln(e.Out, "-- Generating done")
	fmt.Fprintf(e.Out, "-- Build files have been written to: %s\n", parentDir(gen.BuildFile))

	if o.listCache {
		printCache(e, o, gen.State)
	}

	// A project that probed its compiler with try_compile got FALSE, which may
	// have switched off a feature it could in fact have used. The build files
	// are still written -- they are the best answer available -- but a silent
	// success here would let that go unnoticed until someone wondered why an
	// optimisation never turned on.
	if used := unsupported(gen.State); len(used) > 0 {
		fmt.Fprintf(e.Err, "CMake Warning: this build was configured without %s;\n"+
			"  any feature test relying on it reported failure.\n", strings.Join(used, " and "))
	}
	return exitFor(gen.State)
}

// exitFor reports the status a configure run should exit with. A send-error
// does not stop the run, so it has to be remembered until the end.
func exitFor(state *eval.State) int {
	if len(state.Errors) > 0 {
		return 1
	}
	return 0
}

// unsupported lists, once each, the commands configure could not honour.
func unsupported(state *eval.State) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range state.Unsupported {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// printCache implements -L and its variants.
//
// The default hides two kinds of entry: those marked advanced, which a project
// declares as not being knobs for its users, and those of type INTERNAL, which
// are CMake's own storage and were never meant to be read. -LA shows the first
// group; nothing shows the second, because printing them would suggest they
// could be set.
func printCache(e Env, o *configureOptions, state *eval.State) {
	var match *regexp.Regexp
	if o.listRegex != "" {
		re, err := regexp.Compile(o.listRegex)
		if err != nil {
			fmt.Fprintf(e.Err, "CMake Error: %q is not a valid regular expression: %v\n", o.listRegex, err)
			return
		}
		match = re
	}

	fmt.Fprintln(e.Out, "-- Cache values")
	for _, name := range state.Cache.Names() {
		entry, ok := state.Cache.Get(name)
		if !ok || entry.Type == eval.CacheInternal {
			continue
		}
		if entry.Advanced && !o.listAll {
			continue
		}
		if match != nil && !match.MatchString(name) {
			continue
		}
		if o.listHelp {
			doc := entry.DocStr
			if doc == "" {
				doc = "No help, variable specified on the command line."
			}
			fmt.Fprintf(e.Out, "// %s\n", doc)
		}
		fmt.Fprintf(e.Out, "%s:%s=%s\n", name, eval.CacheTypeName(entry.Type), entry.Value)
	}
}

// listPresets implements --list-presets.
func listPresets(e Env, o *configureOptions) int {
	kind, ok := preset.ParseKind(o.listPreset)
	if !ok {
		fmt.Fprintf(e.Err, "CMake Error: %q is not a kind of preset\n", o.listPreset)
		return 1
	}
	dir := o.source
	if dir == "" {
		dir = e.Dir
	}
	if dir == "" {
		dir = "."
	}
	file, err := preset.Load(dir)
	if err != nil || file == nil {
		fmt.Fprintf(e.Err, "CMake Error: could not read presets from %s\n", dir)
		return 1
	}
	presets := file.List(kind)
	if len(presets) == 0 {
		fmt.Fprintf(e.Out, "No %s presets available.\n", o.listPreset)
		return 0
	}
	fmt.Fprintf(e.Out, "Available %s presets:\n\n", o.listPreset)
	// cmake pads the names to the longest, so the descriptions line up.
	width := 0
	for _, p := range presets {
		if n := len(p.Name) + 2; n > width {
			width = n
		}
	}
	for _, p := range presets {
		if p.DisplayName != "" {
			fmt.Fprintf(e.Out, "  %-*q - %s\n", width, p.Name, p.DisplayName)
		} else {
			fmt.Fprintf(e.Out, "  %q\n", p.Name)
		}
	}
	return 0
}
