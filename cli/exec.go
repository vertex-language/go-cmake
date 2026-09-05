package cli

import (
	"github.com/vertex-language/go-cmake/run"
)

// The `-E chdir`, `-E env`, and `-E time` commands run another program. They
// go through the same Runner the rest of the package uses, so that a caller
// embedding the command line can intercept them like anything else.

func command(argv []string, dir string, e Env) run.Command {
	return run.Command{
		Argv:   argv,
		Dir:    dir,
		Env:    e.Env,
		Stdin:  e.In,
		Stdout: e.Out,
		Stderr: e.Err,
	}
}
