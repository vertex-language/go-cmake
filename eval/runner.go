package eval

import (
	"context"
	"errors"
	"os/exec"
)

// osRunner runs commands as subprocesses of this process.
type osRunner struct{}

// OSRunner returns the process runner that actually forks. It is offered as a
// convenience for callers that want the ordinary behaviour; a caller that
// wants to intercept, record, or refuse process execution supplies its own
// [Runner] instead, and one that supplies none gets an error from
// execute_process rather than a silent success.
func OSRunner() Runner { return osRunner{} }

func (osRunner) Run(ctx context.Context, cmd Command) (int, error) {
	if len(cmd.Argv) == 0 {
		return -1, errors.New("empty command")
	}
	c := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	err := c.Run()
	if err == nil {
		return 0, nil
	}
	// A command that ran and failed reports its exit status; only a command
	// that could not be started at all is an error, because execute_process
	// puts the first in RESULT_VARIABLE and the second in the caller's face.
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}
