// Package generate projects a configured [eval.State] into a build system.
//
// This is CMake's second phase. Configure decided what the project declares;
// generate decides what the compiler will actually be told. The two are
// separate because a target's compile line is not knowable when the target is
// declared: it depends on every library the target links, transitively, and
// those may be declared later or in another directory.
package generate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vertex-language/go-cmake/eval"
)

// Resolved is one target with its full, transitively-closed build settings.
type Resolved struct {
	Target *eval.TargetState

	// Compile settings for this target's own sources.
	IncludeDirs []string
	Defines     []string
	CompileOpts []string

	// Link settings: LinkLibs is in link order, dependencies after dependents,
	// which is what a single-pass linker requires.
	LinkOpts []string
	LinkDirs []string
	LinkLibs []string

	// Deps names the targets this one must be built after.
	Deps []string
}

// Graph is every target of a project with its settings resolved.
type Graph struct {
	State   *eval.State
	Targets map[string]*Resolved
	Order   []string // declaration order, for stable output
}

// Resolve computes the transitive build settings for every target.
func Resolve(state *eval.State) (*Graph, error) {
	g := &Graph{State: state, Targets: map[string]*Resolved{}}

	// Aliases are not targets in their own right; every reference to one is
	// answered by the target it names.
	for _, name := range state.TargetOrder {
		t := state.Targets[name]
		if t.Type == "ALIAS" {
			continue
		}
		g.Order = append(g.Order, name)
	}

	// Cycle detection has to happen before the closure walk, because the walk
	// is a depth-first traversal that would otherwise not terminate.
	if err := g.checkCycles(); err != nil {
		return nil, err
	}

	for _, name := range g.Order {
		r, err := g.resolveTarget(name)
		if err != nil {
			return nil, err
		}
		g.Targets[name] = r
	}
	return g, nil
}

// target resolves a name through any alias to the target it denotes.
func (g *Graph) target(name string) (*eval.TargetState, bool) {
	return g.State.Target(name)
}

// checkCycles reports a dependency cycle among targets. CMake permits cycles
// among static libraries — the linker is given the group repeatedly — but not
// among anything else, and a cycle through a shared library or an executable is
// a genuine mistake rather than a link-order subtlety.
func (g *Graph) checkCycles() error {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	var stack []string

	var visit func(string) error
	visit = func(name string) error {
		t, ok := g.target(name)
		if !ok {
			return nil // an external library, not a target
		}
		switch colour[t.Name] {
		case black:
			return nil
		case grey:
			cycle := append(append([]string{}, stack...), t.Name)
			if allStatic(g, cycle) {
				// A cycle of static libraries is legal: the archives are simply
				// listed to the linker more than once.
				return nil
			}
			return fmt.Errorf("The inter-target dependency graph contains the following strongly connected component (cycle):\n\n  %s", strings.Join(cycle, " -> "))
		}
		colour[t.Name] = grey
		stack = append(stack, t.Name)
		for _, dep := range append(append([]string{}, t.LinkLibs...), t.IfaceLinkLibs...) {
			if err := visit(dep); err != nil {
				return err
			}
		}
		for _, dep := range t.Depends {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		colour[t.Name] = black
		return nil
	}

	for _, name := range g.Order {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func allStatic(g *Graph, names []string) bool {
	for _, n := range names {
		t, ok := g.target(n)
		if !ok || t.Type != "STATIC" {
			return false
		}
	}
	return true
}

// resolveTarget computes one target's settings, walking its link closure.
func (g *Graph) resolveTarget(name string) (*Resolved, error) {
	t, ok := g.target(name)
	if !ok {
		return nil, fmt.Errorf("unknown target %q", name)
	}
	dir := g.State.FindDirectory(t.SourceDir)

	r := &Resolved{Target: t}

	// The target's own settings come first: a flag the target set for itself
	// should not be overridden by one it inherited.
	r.IncludeDirs = append(r.IncludeDirs, t.IncludeDirs...)
	r.Defines = append(r.Defines, t.Defines...)
	r.CompileOpts = append(r.CompileOpts, t.CompileOpts...)
	r.LinkOpts = append(r.LinkOpts, t.LinkOpts...)
	r.LinkDirs = append(r.LinkDirs, t.LinkDirs...)

	// The directory's compile definitions are not copied into targets at
	// declaration time, so they are added here.
	if dir != nil {
		r.Defines = append(r.Defines, dir.Definitions...)
	}

	// Walk the link closure. The order matters: a dependency must appear after
	// everything that uses it, so the list is built by a post-order walk and
	// then reversed.
	seen := map[string]bool{}
	var order []string
	var walk func(names []string)
	walk = func(names []string) {
		for _, dep := range names {
			depTarget, isTarget := g.target(dep)
			if !isTarget {
				// A plain library name or path: nothing to inherit from it, but
				// it still has to reach the link line.
				if !seen["\x00"+dep] {
					seen["\x00"+dep] = true
					order = append(order, dep)
				}
				continue
			}
			if seen[depTarget.Name] {
				continue
			}
			seen[depTarget.Name] = true

			// A consumer inherits the dependency's interface, never its
			// private settings. This is the whole point of the PUBLIC /
			// PRIVATE / INTERFACE distinction.
			r.IncludeDirs = appendNew(r.IncludeDirs, depTarget.IfaceIncludeDirs...)
			r.Defines = appendNew(r.Defines, depTarget.IfaceDefines...)
			r.CompileOpts = appendNew(r.CompileOpts, depTarget.IfaceCompileOpts...)
			r.LinkOpts = appendNew(r.LinkOpts, depTarget.IfaceLinkOpts...)
			r.LinkDirs = appendNew(r.LinkDirs, depTarget.IfaceLinkDirs...)

			walk(depTarget.IfaceLinkLibs)

			// An INTERFACE library has nothing to link; it exists only to carry
			// requirements to its consumers.
			if depTarget.Type != "INTERFACE" {
				order = append(order, depTarget.Name)
			}
			r.Deps = appendNew(r.Deps, depTarget.Name)
		}
	}
	walk(t.LinkLibs)

	// Reverse into link order.
	for i := len(order) - 1; i >= 0; i-- {
		r.LinkLibs = append(r.LinkLibs, order[i])
	}

	for _, d := range t.Depends {
		if _, ok := g.target(d); ok {
			r.Deps = appendNew(r.Deps, d)
		}
	}

	r.IncludeDirs = dedupe(r.IncludeDirs)
	r.Defines = dedupe(r.Defines)
	r.LinkDirs = dedupe(r.LinkDirs)
	return r, nil
}

// appendNew appends values that are not already present, preserving order.
func appendNew(dst []string, src ...string) []string {
	for _, v := range src {
		found := false
		for _, existing := range dst {
			if existing == v {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}

// dedupe removes later duplicates, keeping the first occurrence. Include order
// is significant — the first directory holding a header wins — so this cannot
// sort.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// Names returns the resolved target names in declaration order.
func (g *Graph) Names() []string { return g.Order }

// Sorted returns the resolved target names in alphabetical order, for output
// that should not depend on the order the project happened to declare things.
func (g *Graph) Sorted() []string {
	out := append([]string{}, g.Order...)
	sort.Strings(out)
	return out
}

// ImportedFile is the file an imported target contributes, and whether it is
// the one to link against or the one to load.
//
// A targets file written by CMake -- ours or anyone's -- states the location
// per configuration: IMPORTED_LOCATION_RELEASE beside an IMPORTED_CONFIGURATIONS
// list, with the unsuffixed IMPORTED_LOCATION as the fallback for a
// configuration the package was not built for. Reading only the unsuffixed one
// works with files this package writes and with almost nothing else, which is
// the wrong half of the world to be compatible with.
//
// On Windows a shared library is two files: the DLL is loaded and the import
// library is linked. IMPORTED_IMPLIB names the second, and a link line that
// used the DLL instead would fail.
func ImportedFile(t *eval.TargetState, config string, forLinking bool) (string, bool) {
	suffixes := []string{}
	if config != "" {
		suffixes = append(suffixes, "_"+strings.ToUpper(config))
	}
	// A package built for one configuration and consumed from another still has
	// to link, so every configuration it does carry is a candidate.
	for _, c := range eval.SplitList(t.Properties["IMPORTED_CONFIGURATIONS"]) {
		s := "_" + strings.ToUpper(c)
		if len(suffixes) == 0 || suffixes[0] != s {
			suffixes = append(suffixes, s)
		}
	}
	suffixes = append(suffixes, "")

	names := []string{"IMPORTED_LOCATION"}
	if forLinking {
		names = []string{"IMPORTED_IMPLIB", "IMPORTED_LOCATION"}
	}
	for _, name := range names {
		for _, suffix := range suffixes {
			if v, ok := t.Properties[name+suffix]; ok && v != "" {
				return v, true
			}
		}
	}
	return "", false
}
