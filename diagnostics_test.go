package cmake_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/cli"
)

// A diagnostic is the part of a build tool people actually read. It goes into
// CI logs, gets grepped by scripts, and is pasted into bug reports, so a
// message that wraps at a different column or names a file a different way is
// a difference somebody has to reconcile by hand.
//
// These tests are stricter than the ones in eval: no normalising at all. The
// same script goes to both programs and the bytes have to be equal.

// bothRunScript runs one script through cmake and through this program, from
// the same working directory, and returns the two outputs.
func bothRunScript(t *testing.T, script string) (real, ours string) {
	t.Helper()
	binary, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("no cmake on PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.cmake"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "-P", "s.cmake")
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()

	// One buffer for both streams, because that is what a terminal is: a
	// message that goes to stderr and a program's output that goes to stdout
	// interleave there, and comparing them separately would compare an order
	// neither program produces.
	var combined strings.Builder
	cli.Main(context.Background(), cli.Env{
		Args: []string{"-P", "s.cmake"}, Dir: dir, Env: os.Environ(),
		Out: &combined, Err: &combined,
	})
	return strings.ReplaceAll(string(out), "\r\n", "\n"), combined.String()
}

func checkIdentical(t *testing.T, name, script string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		want, got := bothRunScript(t, script)
		if got != want {
			t.Errorf("output differs\n--- real cmake ---\n%q\n--- go-cmake ---\n%q", want, got)
		}
	})
}

func TestDiagnosticsAreIdentical(t *testing.T) {
	checkIdentical(t, "a warning wraps at the same column", `
message(WARNING "a rather long warning that has to wrap somewhere around the seventy-fifth column to look the same as the one the real tool prints")
`)

	checkIdentical(t, "and so does an error, with its sentence spacing", `
message(FATAL_ERROR "this one ends it. With a second sentence after the period, long enough that it has to be broken across two lines.")
`)

	checkIdentical(t, "an author warning carries its footer", `
message(AUTHOR_WARNING "meant for whoever wrote this project")
`)

	checkIdentical(t, "a deprecation warning carries a different one", `
message(DEPRECATION "this went away")
`)

	checkIdentical(t, "a policy warning carries a third", `
exec_program(echo)
`)

	checkIdentical(t, "the banner names the script as it was given", `
message(WARNING "here")
`)

	checkIdentical(t, "an indented line keeps its own spacing", `
message(FATAL_ERROR "head:
  one
  two
tail")
`)

	checkIdentical(t, "a command's own complaint is laid out the same way", `
include(definitely_not_there)
`)

	checkIdentical(t, "so is a bad condition", `
set(V 1)
if(NOT NOT V)
endif()
`)

	checkIdentical(t, "and a policy that can no longer be set to OLD", `
cmake_policy(SET CMP0054 OLD)
`)

	checkIdentical(t, "status output is not wrapped at all", `
message(STATUS "a status line that runs well past the column an error would be wrapped at, because a status line is not a diagnostic")
`)
}
