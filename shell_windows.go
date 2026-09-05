//go:build windows

package cmake

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand runs one shell command line. See the note in
// ninja/shell_windows.go: cmd.exe does not parse a Go-quoted argument list, so
// the exact command line is handed to it through CmdLine.
func shellCommand(ctx context.Context, line string) *exec.Cmd {
	c := exec.CommandContext(ctx, "cmd")
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/s /c "` + line + `"`}
	return c
}
