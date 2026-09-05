//go:build windows

package ninja

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand builds a command that runs one shell command line.
//
// On Windows this cannot go through exec.Command's argument list. Go quotes
// each argument according to the C runtime's rules, and cmd.exe does not follow
// those rules: it strips the first and last quote of everything after /c and
// treats a backslash-escaped quote as two characters. A compiler path
// containing a space — which the default Visual Studio install location does —
// therefore arrives at cmd.exe mangled. Setting CmdLine hands cmd.exe the exact
// bytes instead, wrapped in the extra pair of quotes that /s tells it to strip.
func shellCommand(ctx context.Context, cmd string) *exec.Cmd {
	c := exec.CommandContext(ctx, "cmd")
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/s /c "` + cmd + `"`}
	return c
}
