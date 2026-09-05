package build

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/vertex-language/go-cmake/ninja"
	"github.com/vertex-language/go-cmake/run"
)

type Config struct {
	BinaryDir  string
	Targets    []string
	Config     string
	Jobs       int
	CleanFirst bool
	Verbose    bool
	Generator  string
	Runner     run.Runner
	Env        []string
	Out        io.Writer
	Err        io.Writer
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
	driver.Runner = cfg.Runner
	driver.Env = cfg.Env

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
