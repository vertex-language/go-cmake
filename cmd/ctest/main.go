// Command ctest runs the tests a configured build tree declared.
package main

import (
	"context"
	"os"

	"github.com/vertex-language/go-cmake/cli"
)

func main() {
	os.Exit(cli.CTestMain(context.Background(), cli.Env{
		Args: os.Args[1:],
		Env:  os.Environ(),
		In:   os.Stdin,
		Out:  os.Stdout,
		Err:  os.Stderr,
	}))
}
