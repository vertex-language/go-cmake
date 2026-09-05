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

	// Fresh discards the build directory's existing CMakeCache.txt before
	// configuring, so the run starts from the command line and the project
	// alone.
	Fresh bool
}

type Config struct {
	Source    string
	Binary    string
	Generator string
	Toolchain string
	Preset    string
	Vars      map[string]string

	// Unset holds the globbing expressions given to -U. Matching entries are
	// removed from the loaded cache before the project runs, which is how a
	// stale find_package result is made to search again.
	Unset []string

	// InitialCache holds the scripts named by -C, evaluated in order before the
	// project is read. A script's set(... CACHE) calls therefore land before the
	// project's own defaults, which is the entire point: it is how a CI job
	// pre-answers questions the project is about to ask.
	InitialCache []string

	Env   []string
	Jobs  int
	Flags Flags

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

// New builds a CMake from a Config, applying a preset if one was named.
func New(cfg Config) (*CMake, error) {
	if cfg.Preset != "" {
		if err := applyPreset(&cfg); err != nil {
			return nil, err
		}
	}
	return &CMake{cfg: cfg}, nil
}

// applyPreset folds a named configure preset into the Config.
//
// Every field the preset sets loses to one the caller set explicitly, which is
// what makes `cmake --preset release -DFOO=bar` mean what it looks like. A
// failure here is returned rather than ignored: a mistyped preset name that
// quietly configured with defaults would produce a build nobody asked for, and
// the first sign of it would be a puzzling test failure much later.
func applyPreset(cfg *Config) error {
	pf, err := preset.Load(cfg.Source)
	if err != nil {
		return err
	}
	if pf == nil {
		return fmt.Errorf("no presets file found in %s", cfg.Source)
	}
	res, err := pf.Resolve(cfg.Preset)
	if err != nil {
		return err
	}
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
	for k, v := range res.Environment {
		cfg.Env = append(cfg.Env, k+"="+v)
	}
	return nil
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
	state.LogSink = c.logSink()
	// A -D assignment wins over a remembered value, so it goes in first and
	// the loaded cache fills in only what the command line did not mention.
	for k, v := range c.cfg.Vars {
		state.Cache.Set(k, v, eval.CacheString, "", true)
	}
	if err := c.loadCache(state, binary); err != nil {
		return nil, err
	}
	for _, pattern := range c.cfg.Unset {
		state.Cache.RemoveMatching(pattern)
	}
	if c.cfg.Generator != "" {
		state.Cache.Set("CMAKE_GENERATOR", c.cfg.Generator, eval.CacheInternal, "", true)
	}
	if c.cfg.Toolchain != "" {
		state.Cache.Set("CMAKE_TOOLCHAIN_FILE", c.cfg.Toolchain, eval.CacheFilepath, "", true)
	}
	for _, script := range c.cfg.InitialCache {
		if err := eval.EvalCacheFile(ctx, state, evalFS{c.cfg.FS}, script); err != nil {
			return nil, err
		}
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
	if err := c.saveCache(state, binary); err != nil {
		return nil, err
	}
	return state, nil
}

// cacheFileName is where a build directory remembers what it was configured
// with. The name is CMake's, so that a person who knows where to look finds it
// where they expect.
const cacheFileName = "CMakeCache.txt"

// loadCache merges a previous run's cache into this one.
func (c *CMake) loadCache(state *eval.State, binary string) error {
	if c.cfg.Flags.Fresh {
		// --fresh means the build directory forgets. Removing the file rather
		// than skipping the read matters: a later run must not find it either.
		_ = c.cfg.FS.Remove(binary + "/" + cacheFileName)
		return nil
	}
	data, err := c.cfg.FS.ReadFile(binary + "/" + cacheFileName)
	if err != nil {
		return nil // no previous configure; nothing to remember
	}
	previous, err := eval.ReadCache(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reading %s: %w", cacheFileName, err)
	}
	state.Cache.Merge(previous)
	return nil
}

// saveCache writes what this configure decided, so the next one can start from
// it.
func (c *CMake) saveCache(state *eval.State, binary string) error {
	var buf bytes.Buffer
	if err := eval.WriteCache(&buf, state.Cache, binary); err != nil {
		return err
	}
	return c.cfg.FS.WriteFile(binary+"/"+cacheFileName, buf.Bytes(), 0644)
}

// logSink renders message() output the way CMake prints it. A nil Out means the
// caller wants silence, which is different from wanting the default.
func (c *CMake) logSink() func(mode, text string) {
	if c.cfg.Out == nil {
		return func(string, string) {}
	}
	out := c.cfg.Out
	return func(mode, text string) {
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

	// The install rules become a script rather than an action, so that the
	// prefix and component can be chosen when the install runs rather than now.
	installer := &generate.Install{
		Graph:     graph,
		Toolchain: c.toolchain(state),
		SourceDir: state.SourceDir,
		BinaryDir: state.BinaryDir,
	}
	var script bytes.Buffer
	if err := installer.Write(&script); err != nil {
		return nil, err
	}
	if err := c.cfg.FS.WriteFile(state.BinaryDir+"/"+InstallScriptName, script.Bytes(), 0644); err != nil {
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

// InstallScriptName is the generated script that `cmake --install` runs. It
// carries CMake's own name so that a build tree produced here is recognisable,
// and readable, to anyone who knows where to look.
const InstallScriptName = "cmake_install.cmake"

// InstallOptions are the choices install defers to install time.
type InstallOptions struct {
	BinaryDir string
	Prefix    string
	Component string
	Config    string
}

// Install runs a build tree's generated install script.
//
// Nothing is recomputed. The script was written at generate time and holds the
// full list of what goes where; what a caller supplies here is only the part
// that could not be known then -- the prefix, the component, the configuration.
// That is why installing does not need the source tree, and why a build
// directory can be installed long after the project that produced it moved.
func (c *CMake) Install(ctx context.Context, opts InstallOptions) error {
	binary, err := filepath.Abs(opts.BinaryDir)
	if err != nil {
		return err
	}
	binary = filepath.ToSlash(binary)
	script := binary + "/" + InstallScriptName
	if _, err := c.cfg.FS.Stat(script); err != nil {
		return fmt.Errorf("%s does not contain %s; run cmake there first",
			opts.BinaryDir, InstallScriptName)
	}

	state := eval.NewState(binary, binary, c.cfg.Env)
	state.Runner = c.cfg.Runner
	state.LogSink = c.logSink()
	if opts.Prefix != "" {
		state.SetVar("CMAKE_INSTALL_PREFIX", filepath.ToSlash(opts.Prefix))
	}
	if opts.Component != "" {
		state.SetVar("CMAKE_INSTALL_COMPONENT", opts.Component)
	}
	if opts.Config != "" {
		state.SetVar("CMAKE_INSTALL_CONFIG_NAME", opts.Config)
	}

	if err := eval.EvalCacheFile(ctx, state, evalFS{c.cfg.FS}, script); err != nil {
		return err
	}
	if len(state.Errors) > 0 {
		return fmt.Errorf("install failed")
	}
	return nil
}
