package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"github.com/vertex-language/go-cmake/ninja"
)

type Config struct {
	BinaryDir  string
	Targets    []string
	Config     string
	Jobs       int
	CleanFirst bool
	Verbose    bool
	Generator  string
	Runner     Runner
	Env        []string
	Out        io.Writer
	Err        io.Writer
}

type Runner interface {
	Run(ctx context.Context, cmd Command) error
}

type Command struct {
	Argv   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Line is set when the command is a shell command line rather than an
	// argument vector, which is what a build file's commands are. It exists
	// because cmd.exe does not parse its argument the way the C runtime quotes
	// it: a compiler path containing a space cannot be passed through an argv
	// to `cmd /c` without being mangled. A Runner that executes the command
	// should prefer Line when it is set; Argv is a best-effort rendering for a
	// Runner that only wants to inspect or log it.
	Line string
}

type Result struct {
	Built    int
	UpToDate int
	Failed   int
}

// Build drives the generated build system.
//
// For the Ninja generator this runs the graph in this process: the build.ninja
// is parsed, scheduled, and executed here, so a build needs a compiler but not
// a ninja binary. Every other generator produces a file format this package
// does not write, and a build of one would have nothing to drive.
func Build(ctx context.Context, cfg Config) (*Result, error) {
	switch cfg.Generator {
	case "", "Ninja", "Ninja Multi-Config":
	default:
		return nil, fmt.Errorf("cannot build with generator %q: this package generates and drives Ninja build files", cfg.Generator)
	}

	buildFile := cfg.BinaryDir + "/build.ninja"
	file, err := ninja.Parse(ninja.OSFS(), buildFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", buildFile, err)
	}

	logPath := cfg.BinaryDir + "/.ninja_log"
	log := ninja.NewLog()
	if f, err := os.Open(logPath); err == nil {
		if parsed, err := ninja.ReadLog(f); err == nil {
			log = parsed
		}
		f.Close()
	}
	if f, err := os.Open(cfg.BinaryDir + "/.ninja_deps"); err == nil {
		if deps, err := ninja.ReadDepsLog(f); err == nil {
			for out, list := range deps.All() {
				log.RecordDeps(out, list)
			}
		}
		f.Close()
	}

	if cfg.CleanFirst {
		if err := clean(file); err != nil {
			return nil, err
		}
	}

	driver := &ninja.Driver{
		File:    file,
		Targets: cfg.Targets,
		Jobs:    cfg.Jobs,
		Verbose: cfg.Verbose,
		Log:     log,
		LogPath: logPath,
		Out:     orStdout(cfg.Out),
		Err:     orStderr(cfg.Err),
	}
	if cfg.Runner != nil {
		driver.Runner = &commandRunner{runner: cfg.Runner, env: cfg.Env}
	}

	res, err := driver.Build(ctx)
	if res == nil {
		return nil, err
	}
	writeDepsLog(cfg.BinaryDir, log)
	return &Result{Built: res.Built, UpToDate: res.UpToDate, Failed: res.Failed}, err
}

// clean removes every file the build would produce, which is what
// --clean-first asks for.
func clean(file *ninja.File) error {
	for _, e := range file.Edges {
		if e.Rule == "phony" {
			continue
		}
		for _, out := range e.AllOutputs() {
			if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func writeDepsLog(binaryDir string, log *ninja.Log) {
	if !log.HasDeps() {
		return
	}
	deps := ninja.NewDepsLog()
	log.EachDep(deps.Add)
	f, err := os.Create(binaryDir + "/.ninja_deps")
	if err != nil {
		return
	}
	defer f.Close()
	_ = deps.Write(f)
}

// commandRunner adapts the caller's Runner, which takes an argv, to the shell
// command line a build file holds. The command is handed to the platform shell
// because that is what the build file's syntax means: it may contain
// redirection, an && chain, or quoting that only a shell resolves.
type commandRunner struct {
	runner Runner
	env    []string
}

func (r *commandRunner) Run(ctx context.Context, cmd, dir string, stdout, stderr io.Writer) error {
	argv := []string{"/bin/sh", "-c", cmd}
	if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/c", cmd}
	}
	return r.runner.Run(ctx, Command{
		Argv:   argv,
		Line:   cmd,
		Dir:    dir,
		Env:    r.env,
		Stdout: stdout,
		Stderr: stderr,
	})
}

func orStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

func orStderr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}

type InstallConfig struct {
	BinaryDir string
	Prefix    string
	Component string
	Config    string
	Strip     bool
	Runner    Runner
	Env       []string
	Out, Err  io.Writer
}

func Install(ctx context.Context, cfg InstallConfig) error {
	args := []string{"--install", cfg.BinaryDir}
	if cfg.Prefix != "" {
		args = append(args, "--prefix", cfg.Prefix)
	}
	if cfg.Config != "" {
		args = append(args, "--config", cfg.Config)
	}
	c := Command{
		Argv:   append([]string{"cmake"}, args...),
		Dir:    ".",
		Env:    cfg.Env,
		Stdout: cfg.Out,
		Stderr: cfg.Err,
	}
	return cfg.Runner.Run(ctx, c)
}

type CTestConfig struct {
	BinaryDir       string
	Config          string
	Jobs            int
	OutputOnFailure bool
	Preset          string
	Include         string
	Exclude         string
	LabelInclude    string
	LabelExclude    string
	Runner          Runner
	Env             []string
	Out, Err        io.Writer
}

func CTest(ctx context.Context, cfg CTestConfig) error {
	c := Command{
		Argv:   []string{"ctest"},
		Dir:    cfg.BinaryDir,
		Env:    cfg.Env,
		Stdout: cfg.Out,
		Stderr: cfg.Err,
	}
	return cfg.Runner.Run(ctx, c)
}

type CPackConfig struct {
	BinaryDir   string
	Generators  []string
	Config      string
	PackageName string
	Version     string
	Variables   map[string]string
	Runner      Runner
	Env         []string
	Out, Err    io.Writer
}

func CPack(ctx context.Context, cfg CPackConfig) error {
	c := Command{
		Argv:   []string{"cpack"},
		Dir:    cfg.BinaryDir,
		Env:    cfg.Env,
		Stdout: cfg.Out,
		Stderr: cfg.Err,
	}
	return cfg.Runner.Run(ctx, c)
}

// OSRunner runs commands locally.
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, cmd Command) error {
	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	return c.Run()
}
