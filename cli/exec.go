package cli

import (
	"errors"
	"os/exec"

	cmake "github.com/vertex-language/go-cmake"
)

// The `-E chdir`, `-E env`, and `-E time` commands run another program. They
// go through the same Runner the rest of the package uses, so that a caller
// embedding the command line can intercept them like anything else.

func cmakeRunner() cmake.Runner { return cmake.RealRunner() }

func command(argv []string, dir string, e Env) cmake.Command {
	return cmake.Command{
		Argv:   argv,
		Dir:    dir,
		Env:    e.Env,
		Stdin:  e.In,
		Stdout: e.Out,
		Stderr: e.Err,
	}
}

// exitStatus extracts a process exit code from an error, so that `cmake -E
// chdir dir prog` returns what prog returned rather than a generic failure.
func exitStatus(err error) (int, bool) {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}
