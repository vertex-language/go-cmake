package ninja

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Driver builds a parsed Ninja graph in this process.
//
// A build tool has exactly one hard problem: deciding what not to run. The
// scheduling below is the whole of it — an edge runs when one of its outputs is
// missing or older than one of its inputs, or when the command that would
// produce it differs from the command that did. The last of those is why the
// build log exists: a change to a compile flag alters no file's timestamp, and
// without the log the build would quietly keep stale objects.
type Driver struct {
	File    *File
	Targets []string // build these; empty means the file's defaults
	Jobs    int
	Verbose bool
	DryRun  bool

	FS      FS
	Runner  CommandRunner
	Log     *Log
	LogPath string

	Out io.Writer
	Err io.Writer
}

// CommandRunner executes one shell command line.
type CommandRunner interface {
	Run(ctx context.Context, cmd string, dir string, stdout, stderr io.Writer) error
}

// Result summarises a build.
type Result struct {
	Built    int
	UpToDate int
	Failed   int
	Errors   []string
}

// shellRunner runs commands through the platform's shell, which is what ninja
// does: a command line in a build file is shell syntax, not an argv.
type shellRunner struct{}

// ShellRunner returns the default command runner.
func ShellRunner() CommandRunner { return shellRunner{} }

func (shellRunner) Run(ctx context.Context, cmd, dir string, stdout, stderr io.Writer) error {
	c := shellCommand(ctx, cmd)
	c.Dir = dir
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Run()
}

// node is one file in the graph.
type node struct {
	path    string
	edge    *plan // the edge that produces it, if any
	mtime   time.Time
	exists  bool
	statted bool
}

// plan is an edge with its scheduling state.
type plan struct {
	edge      *Edge
	command   string
	desc      string
	depfile   string
	deps      string
	restat    bool
	generator bool

	dirty      bool
	visited    bool
	done       bool
	waiting    int     // number of prerequisite edges not yet finished
	dependents []*plan // edges waiting on this one
}

// runs reports whether an edge executes a command. A phony edge exists only to
// name a group of other edges, so it must not appear in the progress count or
// in the number of things a build reports having done.
func (p *plan) runs() bool {
	return p.edge.Rule != "phony" && strings.TrimSpace(p.command) != ""
}

// Build runs the graph.
func (d *Driver) Build(ctx context.Context) (*Result, error) {
	if d.FS == nil {
		d.FS = OSFS()
	}
	if d.Runner == nil {
		d.Runner = ShellRunner()
	}
	if d.Out == nil {
		d.Out = os.Stdout
	}
	if d.Err == nil {
		d.Err = os.Stderr
	}
	jobs := d.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
	}

	g, err := d.prepare()
	if err != nil {
		return nil, err
	}

	roots, err := d.rootTargets(g)
	if err != nil {
		return nil, err
	}

	// Collect the edges needed for the requested targets, and decide which of
	// them are out of date. Both are done in one post-order walk, because an
	// edge is dirty if any edge it depends on is dirty, which is only knowable
	// after its dependencies have been examined.
	res := &Result{}
	var required []*plan
	for _, r := range roots {
		if err := g.visit(r, &required); err != nil {
			return nil, err
		}
	}

	var todo []*plan
	for _, p := range required {
		if p.dirty {
			todo = append(todo, p)
		} else if p.runs() {
			res.UpToDate++
		}
	}
	if len(todo) == 0 {
		fmt.Fprintln(d.Out, "ninja: no work to do.")
		return res, nil
	}

	if err := d.run(ctx, g, todo, jobs, res); err != nil {
		return res, err
	}
	if d.Log != nil && d.LogPath != "" && !d.DryRun {
		d.writeLog()
	}
	return res, nil
}

// graph is the node and edge index for one build.
type graph struct {
	driver *Driver
	nodes  map[string]*node
	plans  []*plan
}

// prepare indexes the parsed file: one plan per edge, one node per file, and
// the map from an output to the edge that produces it.
func (d *Driver) prepare() (*graph, error) {
	g := &graph{driver: d, nodes: map[string]*node{}}
	for _, e := range d.File.Edges {
		rule, ok := d.File.Rules[e.Rule]
		if !ok {
			return nil, fmt.Errorf("ninja: unknown rule %q", e.Rule)
		}
		p := &plan{edge: e}
		lookup := d.bindings(e, rule)
		p.command = expandVars(rule.Vars["command"], lookup)
		p.desc = expandVars(rule.Vars["description"], lookup)
		p.depfile = expandVars(rule.Vars["depfile"], lookup)
		p.deps = rule.Vars["deps"]
		p.restat = rule.Vars["restat"] != ""
		p.generator = rule.Vars["generator"] != ""
		g.plans = append(g.plans, p)

		for _, out := range e.AllOutputs() {
			n := g.node(out)
			if n.edge != nil {
				return nil, fmt.Errorf("ninja: multiple rules generate %q", out)
			}
			n.edge = p
		}
	}
	return g, nil
}

// bindings resolves a variable for an edge: the edge's own bindings first, then
// $in and $out, then the rule's, then the file's.
func (d *Driver) bindings(e *Edge, rule *Rule) func(string) (string, bool) {
	return func(name string) (string, bool) {
		switch name {
		case "in":
			return joinPaths(e.Inputs, " "), true
		case "in_newline":
			return joinPaths(e.Inputs, "\n"), true
		case "out":
			return joinPaths(e.Outputs, " "), true
		}
		if v, ok := e.Vars[name]; ok {
			return v, true
		}
		if v, ok := rule.Vars[name]; ok {
			// A rule's own binding may itself reference $in and the edge's
			// variables, so it is expanded rather than returned raw.
			return expandVars(v, d.bindings(e, &Rule{Name: rule.Name, Vars: map[string]string{}})), true
		}
		if v, ok := d.File.Vars[name]; ok {
			return v, true
		}
		return "", false
	}
}

// joinPaths renders a path list for a command line, quoting what needs it.
func joinPaths(paths []string, sep string) string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if strings.ContainsAny(p, " \t") {
			out[i] = `"` + p + `"`
		} else {
			out[i] = p
		}
	}
	return strings.Join(out, sep)
}

func (g *graph) node(path string) *node {
	n, ok := g.nodes[path]
	if !ok {
		n = &node{path: path}
		g.nodes[path] = n
	}
	return n
}

// stat fills in a node's timestamp, once.
func (g *graph) stat(n *node) {
	if n.statted {
		return
	}
	n.statted = true
	fi, err := g.driver.FS.Stat(n.path)
	if err == nil {
		n.mtime = fi.ModTime()
		n.exists = true
	}
}

// rootTargets resolves the requested targets, or the file's defaults, or every
// output with nothing depending on it.
func (d *Driver) rootTargets(g *graph) ([]*node, error) {
	names := d.Targets
	if len(names) == 0 {
		names = d.File.Defaults
	}
	if len(names) == 0 {
		// No default: build everything that is not consumed by another edge.
		consumed := map[string]bool{}
		for _, e := range d.File.Edges {
			for _, in := range e.AllInputs() {
				consumed[in] = true
			}
			for _, in := range e.OrderOnly {
				consumed[in] = true
			}
		}
		var roots []string
		for path, n := range g.nodes {
			if n.edge != nil && !consumed[path] {
				roots = append(roots, path)
			}
		}
		sort.Strings(roots)
		names = roots
	}

	var out []*node
	for _, name := range names {
		n, ok := g.nodes[name]
		if !ok {
			return nil, fmt.Errorf("ninja: unknown target %q", name)
		}
		out = append(out, n)
	}
	return out, nil
}

// visit walks a node's dependencies depth-first, appending the edges that must
// run, in an order where every edge follows the ones it depends on.
func (g *graph) visit(n *node, required *[]*plan) error {
	g.stat(n)
	p := n.edge
	if p == nil {
		// A source file: it must exist, or nothing can be built from it.
		if !n.exists {
			return fmt.Errorf("ninja: %q, needed by the build, missing and no known rule to make it", n.path)
		}
		return nil
	}
	if p.visited {
		return nil
	}
	p.visited = true

	// Depend on the inputs first so that their dirtiness is known.
	for _, in := range p.edge.AllInputs() {
		child := g.node(in)
		if err := g.visit(child, required); err != nil {
			return err
		}
		if child.edge != nil {
			child.edge.dependents = append(child.edge.dependents, p)
			p.waiting++
		}
	}
	for _, in := range p.edge.OrderOnly {
		child := g.node(in)
		if err := g.visit(child, required); err != nil {
			return err
		}
		if child.edge != nil {
			child.edge.dependents = append(child.edge.dependents, p)
			p.waiting++
		}
	}

	p.dirty = g.isDirty(p)
	*required = append(*required, p)
	return nil
}

// isDirty decides whether an edge must run.
func (g *graph) isDirty(p *plan) bool {
	// A phony edge produces nothing; it is dirty exactly when something it
	// depends on is.
	if p.edge.Rule == "phony" {
		return g.anyInputDirty(p)
	}

	var newest time.Time
	for _, in := range p.edge.AllInputs() {
		child := g.node(in)
		g.stat(child)
		if child.edge != nil && child.edge.dirty {
			return true
		}
		if child.mtime.After(newest) {
			newest = child.mtime
		}
	}
	// Order-only inputs are deliberately not consulted here: they constrain
	// when this edge may run, not whether it needs to.

	for _, out := range p.edge.AllOutputs() {
		n := g.node(out)
		g.stat(n)
		if !n.exists {
			return true
		}
		if n.mtime.Before(newest) {
			return true
		}
	}

	// The headers a source turned out to include are inputs too, but the build
	// file never named them: the compiler discovered them on the last run.
	if g.driver.Log != nil {
		for _, out := range p.edge.Outputs {
			for _, dep := range g.driver.Log.DepsFor(out) {
				child := g.node(dep)
				g.stat(child)
				if !child.exists || child.mtime.After(newest) {
					newest = child.mtime
				}
				if !child.exists {
					return true
				}
			}
		}
		for _, out := range p.edge.AllOutputs() {
			n := g.node(out)
			if n.exists && n.mtime.Before(newest) {
				return true
			}
		}
		// The command itself is an input. Without this check, editing a compile
		// flag changes no timestamp and the build produces nothing.
		for _, out := range p.edge.Outputs {
			if _, recorded := g.driver.Log.Command(out); recorded &&
				!g.driver.Log.CommandMatches(out, p.command) {
				return true
			}
		}
	}
	return false
}

func (g *graph) anyInputDirty(p *plan) bool {
	for _, in := range append(p.edge.AllInputs(), p.edge.OrderOnly...) {
		child := g.node(in)
		if child.edge != nil && child.edge.dirty {
			return true
		}
		g.stat(child)
		if !child.exists && child.edge == nil {
			return true
		}
	}
	return false
}

// run executes the dirty edges, respecting dependencies and the job limit.
func (d *Driver) run(ctx context.Context, g *graph, todo []*plan, jobs int, res *Result) error {
	// Only edges that will actually run count towards the wait counts; an edge
	// waiting on a clean dependency would otherwise never become ready.
	pending := map[*plan]int{}
	inSet := map[*plan]bool{}
	for _, p := range todo {
		inSet[p] = true
	}
	for _, p := range todo {
		n := 0
		for _, in := range append(p.edge.AllInputs(), p.edge.OrderOnly...) {
			if child := g.nodes[in]; child != nil && child.edge != nil && inSet[child.edge] {
				n++
			}
		}
		pending[p] = n
	}

	var ready []*plan
	for _, p := range todo {
		if pending[p] == 0 {
			ready = append(ready, p)
		}
	}

	total := 0
	for _, p := range todo {
		if p.runs() {
			total++
		}
	}
	done := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, jobs)
	failed := false
	var firstErr error

	// finish releases the edges that were waiting on p and returns the ones
	// that have become ready.
	finish := func(p *plan) []*plan {
		var next []*plan
		for _, dep := range p.dependents {
			if !inSet[dep] {
				continue
			}
			pending[dep]--
			if pending[dep] == 0 {
				next = append(next, dep)
			}
		}
		return next
	}

	var launch func(p *plan)
	launch = func(p *plan) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			if failed || ctx.Err() != nil {
				mu.Unlock()
				return
			}
			n := 0
			if p.runs() {
				done++
				n = done
			}
			mu.Unlock()

			err := d.execute(ctx, p, n, total)

			mu.Lock()
			if err != nil {
				res.Failed++
				res.Errors = append(res.Errors, err.Error())
				if firstErr == nil {
					firstErr = err
				}
				failed = true
				mu.Unlock()
				return
			}
			if p.runs() {
				res.Built++
			}
			next := finish(p)
			mu.Unlock()

			for _, q := range next {
				launch(q)
			}
		}()
	}

	for _, p := range ready {
		launch(p)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// execute runs one edge's command.
func (d *Driver) execute(ctx context.Context, p *plan, index, total int) error {
	if p.edge.Rule == "phony" || strings.TrimSpace(p.command) == "" {
		return nil
	}

	label := p.desc
	if label == "" || d.Verbose {
		label = p.command
	}
	fmt.Fprintf(d.Out, "[%d/%d] %s\n", index, total, label)

	if d.DryRun {
		return nil
	}

	// A command writes into directories the build tool is expected to create.
	for _, out := range p.edge.AllOutputs() {
		if dir := filepath.Dir(out); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0755)
		}
	}

	var buf strings.Builder
	err := d.Runner.Run(ctx, p.command, "", &buf, &buf)
	out := buf.String()

	// MSVC reports the headers it opened on its own output rather than in a
	// depfile, so they are collected here and removed before the rest reaches
	// the user.
	if p.deps == "msvc" {
		if d.Log != nil {
			if includes := parseShowIncludes(out); len(includes) > 0 {
				for _, o := range p.edge.Outputs {
					d.Log.RecordDeps(o, includes)
				}
			}
		}
		out = filterShowIncludes(out)
	}
	if out != "" {
		// Compiler output belongs with the command that produced it, so it is
		// buffered and written as a unit rather than interleaved with the
		// output of the other jobs running at the same time.
		fmt.Fprint(d.Err, out)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", firstOutput(p.edge), err)
	}

	if p.depfile != "" {
		d.recordDepfile(p)
	}
	if d.Log != nil {
		now := time.Now()
		for _, out := range p.edge.Outputs {
			d.Log.RecordCommand(out, p.command, now)
		}
	}
	return nil
}

func firstOutput(e *Edge) string {
	if len(e.Outputs) > 0 {
		return e.Outputs[0]
	}
	return e.Rule
}

// recordDepfile reads the depfile a compiler wrote and stores the headers it
// names, so that the next build rebuilds when a header changes.
func (d *Driver) recordDepfile(p *plan) {
	data, err := d.FS.ReadFile(p.depfile)
	if err != nil {
		return
	}
	target, deps, err := ParseDepfile(data)
	if err != nil || len(deps) == 0 {
		return
	}
	if d.Log != nil {
		d.Log.RecordDeps(target, deps)
	}
}

func (d *Driver) writeLog() {
	f, err := os.Create(d.LogPath)
	if err != nil {
		return
	}
	defer f.Close()
	_ = d.Log.Write(f)
}
