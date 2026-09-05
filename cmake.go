package cmake

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vertex-language/go-cmake/build"
	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/generate"
	"github.com/vertex-language/go-cmake/preset"
	"github.com/vertex-language/go-cmake/run"
	"github.com/vertex-language/go-cmake/toolchain"
)

// Flags holds the command-line switches that change what this package does.
//
// It is short because most of CMake's switches change only diagnostics, and a
// field that is never read is a promise the package does not keep: a caller
// setting it would get silence. The ones the command line accepts and ignores
// are listed in cli, not here.
type Flags struct {
	// LogLevel selects the message() modes that reach Out. "VERBOSE" also
	// makes the build print each command it runs.
	LogLevel string
}

type Config struct {
	Source    string
	Binary    string
	Generator string
	Toolchain string
	Preset    string
	Vars      map[string]string
	Env       []string
	Jobs      int
	Flags     Flags

	FS     FS
	Runner run.Runner
	Out    io.Writer
	Err    io.Writer
}

type FS interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	MkdirAll(name string, perm fs.FileMode) error
	Glob(pattern string) ([]string, error)
	Remove(name string) error
	Stat(name string) (fs.FileInfo, error)
}

type ConfigureResult struct {
	Cache       map[string]string
	TargetNames []string
	SourceDir   string
	BinaryDir   string

	// State is the full configure result: every variable, target, install rule,
	// and test the project declared. Stopping here is a supported use.
	State *eval.State
}

type GenerateResult struct {
	BuildFile string
	Generator string

	// State and Graph are the configure result and the resolved target graph.
	// A caller that wants compile_commands.json, a dependency diagram, or a
	// lint pass is done after Generate and needs these rather than the files.
	State *eval.State
	Graph *generate.Graph
}

type BuildResult struct {
	Built    int
	UpToDate int
	Failed   int
}

type CMake struct {
	cfg Config
	tc  *toolchain.Toolchain
}

func New(cfg Config) (*CMake, error) {
	if cfg.Preset != "" {
		// apply preset
		pf, err := preset.Load(cfg.Source)
		if err == nil && pf != nil {
			if res, err := pf.Resolve(cfg.Preset); err == nil {
				if cfg.Generator == "" {
					cfg.Generator = res.Generator
				}
				if cfg.Binary == "" {
					cfg.Binary = res.BinaryDir
				}
				if cfg.Toolchain == "" {
					cfg.Toolchain = res.ToolchainFile
				}
				if cfg.Vars == nil {
					cfg.Vars = make(map[string]string)
				}
				for k, v := range res.CacheVars {
					if _, ok := cfg.Vars[k]; !ok {
						cfg.Vars[k] = v
					}
				}
			}
		}
	}
	return &CMake{cfg: cfg}, nil
}

// evalFS adapts the package's FS to the narrower one eval needs. The two
// differ because eval never chooses a file mode: permissions are a property of
// the caller's filesystem policy, not of the CMake language.
type evalFS struct {
	FS
}

func (e evalFS) WriteFile(name string, data []byte) error {
	return e.FS.WriteFile(name, data, 0644)
}

func (e evalFS) MkdirAll(name string) error {
	return e.FS.MkdirAll(name, 0755)
}

// Configure runs the configure phase: it evaluates the CMakeLists.txt tree and
// returns the state it declared.
func (c *CMake) Configure(ctx context.Context) (*ConfigureResult, error) {
	state, err := c.configure(ctx)
	if err != nil {
		return nil, err
	}
	res := &ConfigureResult{
		Cache:       map[string]string{},
		TargetNames: append([]string{}, state.TargetOrder...),
		SourceDir:   c.cfg.Source,
		BinaryDir:   c.cfg.Binary,
		State:       state,
	}
	for _, name := range state.Cache.Names() {
		if entry, ok := state.Cache.Get(name); ok {
			res.Cache[name] = entry.Value
		}
	}
	return res, nil
}

// configure builds the State, seeding it with the command-line -D assignments
// before the first CMakeLists.txt line runs, which is what makes them override
// the project's own defaults.
func (c *CMake) configure(ctx context.Context) (*eval.State, error) {
	source, err := filepath.Abs(c.cfg.Source)
	if err != nil {
		return nil, err
	}
	binary := c.cfg.Binary
	if binary == "" {
		binary = source
	}
	if binary, err = filepath.Abs(binary); err != nil {
		return nil, err
	}

	state := eval.NewState(filepath.ToSlash(source), filepath.ToSlash(binary), c.cfg.Env)
	state.Runner = c.cfg.Runner
	if c.cfg.Out != nil {
		out := c.cfg.Out
		state.LogSink = func(mode, text string) {
			switch mode {
			case "":
				fmt.Fprintln(out, text)
			case "STATUS":
				fmt.Fprintln(out, "-- "+text)
			default:
				fmt.Fprintf(out, "%s: %s\n", mode, text)
			}
		}
	}
	for k, v := range c.cfg.Vars {
		state.Cache.Set(k, v, eval.CacheString, "", true)
	}
	if c.cfg.Generator != "" {
		state.Cache.Set("CMAKE_GENERATOR", c.cfg.Generator, eval.CacheInternal, "", true)
	}
	if c.cfg.Toolchain != "" {
		state.Cache.Set("CMAKE_TOOLCHAIN_FILE", c.cfg.Toolchain, eval.CacheFilepath, "", true)
	}

	// CMake detects compilers inside project(); this package detects them
	// before evaluation and seeds the results, so that a CMakeLists.txt reading
	// CMAKE_C_COMPILER_ID sees exactly what the generator will use.
	c.tc = toolchain.Detect([]string{"C", "CXX"}, c.toolchainEnv())
	for k, v := range c.tc.Variables() {
		state.Cache.Set(k, v, eval.CacheInternal, "", false)
	}
	state.Cache.Set("CMAKE_COMMAND", selfPath(), eval.CacheInternal, "", true)

	if err := c.cfg.FS.MkdirAll(binary, 0755); err != nil {
		return nil, err
	}
	if err := eval.EvalProject(ctx, state, evalFS{c.cfg.FS}); err != nil {
		return nil, err
	}
	return state, nil
}

// Generate runs the generate phase: it resolves each target's transitive build
// settings and writes the chosen generator's build files.
func (c *CMake) Generate(ctx context.Context) (*GenerateResult, error) {
	state, err := c.configure(ctx)
	if err != nil {
		return nil, err
	}
	return c.generateFrom(ctx, state)
}

func (c *CMake) generateFrom(ctx context.Context, state *eval.State) (*GenerateResult, error) {
	gen := c.cfg.Generator
	if gen == "" {
		gen = "Ninja"
	}
	if gen != "Ninja" {
		// Every other generator is a file format this package does not write.
		// Saying so is better than writing a build.ninja and calling it an
		// Xcode project.
		return nil, fmt.Errorf("generator %q is not implemented; this package generates Ninja build files", gen)
	}

	graph, err := generate.Resolve(state)
	if err != nil {
		return nil, &eval.FatalError{Msg: err.Error()}
	}

	n := &generate.Ninja{
		Graph:        graph,
		Toolchain:    c.toolchain(state),
		SourceDir:    state.SourceDir,
		BinaryDir:    state.BinaryDir,
		CMakeCommand: state.GetVar("CMAKE_COMMAND"),
	}
	var buf bytes.Buffer
	if _, err := n.WriteTo(&buf); err != nil {
		return nil, err
	}

	buildFile := state.BinaryDir + "/build.ninja"
	if err := c.cfg.FS.MkdirAll(state.BinaryDir, 0755); err != nil {
		return nil, err
	}
	if err := c.cfg.FS.WriteFile(buildFile, buf.Bytes(), 0644); err != nil {
		return nil, err
	}

	return &GenerateResult{
		BuildFile: buildFile,
		Generator: gen,
		State:     state,
		Graph:     graph,
	}, nil
}

// toolchain returns the toolchain detected during configure, or detects one
// now if configure did not need it.
func (c *CMake) toolchain(state *eval.State) *toolchain.Toolchain {
	if c.tc != nil {
		return c.tc
	}
	c.tc = toolchain.Detect(languagesOf(state), c.toolchainEnv())
	return c.tc
}

func (c *CMake) toolchainEnv() toolchain.Env {
	if len(c.cfg.Env) == 0 {
		return toolchain.OSEnv()
	}
	env := toolchain.Env{Vars: c.cfg.Env}
	for _, v := range c.cfg.Env {
		if strings.HasPrefix(v, "PATH=") || strings.HasPrefix(v, "Path=") {
			env.Path = filepath.SplitList(v[5:])
		}
	}
	return env
}

// selfPath is the path to this program, which generated build rules invoke for
// file operations. It falls back to the bare name when the executable cannot
// be located, which is the best a build file can do.
func selfPath() string {
	if p, err := os.Executable(); err == nil {
		return strings.ReplaceAll(p, "\\", "/")
	}
	return "cmake"
}

// languagesOf lists the languages the project enabled, in a stable order.
func languagesOf(state *eval.State) []string {
	var out []string
	for _, lang := range []string{"C", "CXX"} {
		if state.Languages[lang] {
			out = append(out, lang)
		}
	}
	for lang := range state.Languages {
		if lang != "C" && lang != "CXX" {
			out = append(out, lang)
		}
	}
	sort.Strings(out[min(len(out), 2):])
	return out
}

// Build runs all three phases and drives the generated build system.
func (c *CMake) Build(ctx context.Context) (*BuildResult, error) {
	state, err := c.configure(ctx)
	if err != nil {
		return nil, err
	}
	gen, err := c.generateFrom(ctx, state)
	if err != nil {
		return nil, err
	}

	bcfg := build.Config{
		BinaryDir: state.BinaryDir,
		Jobs:      c.cfg.Jobs,
		Verbose:   c.cfg.Flags.LogLevel == "VERBOSE",
		Generator: gen.Generator,
		Env:       c.cfg.Env,
		Out:       c.cfg.Out,
		Err:       c.cfg.Err,
	}
	if c.cfg.Runner != nil {
		bcfg.Runner = c.cfg.Runner
	}

	res, err := build.Build(ctx, bcfg)
	if res == nil {
		return nil, err
	}
	return &BuildResult{Built: res.Built, UpToDate: res.UpToDate, Failed: res.Failed}, err
}
