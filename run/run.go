// Package run is the single point at which this module starts a process.
//
// Four phases need to run something — configure runs execute_process, generate
// runs the compiler to detect its identity, build runs compile and link lines,
// and the command line runs `cmake -E chdir prog` — and every one of them wants
// the same thing: hand over a command, get back what happened. Before this
// package each of them declared its own Command struct and Runner interface,
// and the layers in between existed only to convert one into another.
//
// Substituting a Runner is how a caller takes over process execution without
// taking over CMake: a Runner can recognise compiler invocations and turn them
// into cacheable actions, record inputs and outputs, print without executing,
// or refuse. Everything this module decides about a command — the executable,
// the arguments, the working directory, the environment — is already decided
// when the Runner receives it. What is left is how it runs.
package run

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

// Command is one process invocation.
type Command struct {
	// Argv is the executable and its arguments.
	Argv []string

	// Line is set when the command is a shell command line rather than an
	// argument vector, which is what a build file's commands are. It exists
	// because cmd.exe does not parse its argument the way the C runtime quotes
	// it: a compiler path containing a space cannot be passed through an argv
	// to `cmd /c` without being mangled. A Runner that executes the command
	// must prefer Line when it is set; Argv is a best-effort rendering for a
	// Runner that only wants to inspect or log it.
	Line string

	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner executes one command.
type Runner interface {
	// Run executes cmd and reports its exit status.
	//
	// A command that ran and failed reports a non-zero code and a nil error.
	// Only a command that could not be started at all returns an error. The
	// distinction matters: execute_process puts an exit status in
	// RESULT_VARIABLE and carries on, while a missing executable is a
	// configure-time failure, and a build step's non-zero exit must fail the
	// build rather than be reported as a broken runner.
	Run(ctx context.Context, cmd Command) (exitCode int, err error)
}

// OS returns the Runner that actually forks.
func OS() Runner { return osRunner{} }

type osRunner struct{}

func (osRunner) Run(ctx context.Context, cmd Command) (int, error) {
	c, err := cmd.build(ctx)
	if err != nil {
		return -1, err
	}
	if err := c.Run(); err != nil {
		if code, ok := ExitCode(err); ok {
			return code, nil
		}
		return -1, err
	}
	return 0, nil
}

// build turns a Command into an exec.Cmd, preferring a shell line over an argv.
func (cmd Command) build(ctx context.Context) (*exec.Cmd, error) {
	var c *exec.Cmd
	if cmd.Line != "" {
		c = shellCommand(ctx, cmd.Line)
	} else {
		if len(cmd.Argv) == 0 {
			return nil, errors.New("run: empty command")
		}
		c = exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	}
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	return c, nil
}

// ExitCode extracts a process exit status from an error returned by os/exec,
// so that a caller wrapping its own runner can honour the same contract.
func ExitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}
