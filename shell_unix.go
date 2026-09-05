//go:build !windows

package cmake

import (
	"context"
	"os/exec"
)

// shellCommand runs one shell command line.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", line)
}
