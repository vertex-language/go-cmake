//go:build !windows

package ninja

import (
	"context"
	"os/exec"
)

// shellCommand builds a command that runs one shell command line.
func shellCommand(ctx context.Context, cmd string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
}
