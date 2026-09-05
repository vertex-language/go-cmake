package cmake_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CMAKE_<LANG>_FLAGS and its per-configuration companions are where a project
// says what kind of build it wants: the warning level, the language standard,
// and -- in CMAKE_C_FLAGS_RELEASE -- the optimisation and NDEBUG that make a
// Release build a Release build.
//
// A build that ignores them still compiles, still links, and still runs. It
// just produces an unoptimised binary with its assertions live and calls it
// Release, which is the kind of wrong that is only noticed by whoever wonders
// why the numbers are bad.
//
// So these tests do not read the generated build file. They compile a program
// whose output says what it was compiled with.

// probeProgram is a program that reports the flags it was built under.
const probeProgram = `
#include <stdio.h>
int main(void) {
#ifdef NDEBUG
  printf("ndebug=yes\n");
#else
  printf("ndebug=no\n");
#endif
#ifdef FROM_FLAGS
  printf("from_flags=yes\n");
#else
  printf("from_flags=no\n");
#endif
#ifdef FROM_CONFIG_FLAGS
  printf("from_config=yes\n");
#else
  printf("from_config=no\n");
#endif
  return 0;
}
`

// flagProject writes a project and returns what its program prints.
func buildAndRun(t *testing.T, lists string, args ...string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "CMakeLists.txt", lists)
	write(t, dir, "main.c", probeProgram)
	build := filepath.Join(dir, "b")
	if code, out, errOut := runCLI(t, dir, append([]string{"-S", dir, "-B", build}, args...)...); code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, dir, "--build", build); code != 0 {
		t.Fatalf("build failed:\n%s%s", out, errOut)
	}
	return runProgram(t, filepath.Join(build, "app"+exeSuffix()))
}

const plainProject = `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_executable(app main.c)
`

// TestReleaseDefinesNDEBUG is the one that matters: a Release build has to turn
// assertions off, and it does that through CMAKE_C_FLAGS_RELEASE.
func TestReleaseDefinesNDEBUG(t *testing.T) {
	if got := buildAndRun(t, plainProject, "-DCMAKE_BUILD_TYPE=Release"); !strings.Contains(got, "ndebug=yes") {
		t.Errorf("a Release build did not define NDEBUG; the program printed:\n%s", got)
	}
	if got := buildAndRun(t, plainProject, "-DCMAKE_BUILD_TYPE=Debug"); !strings.Contains(got, "ndebug=no") {
		t.Errorf("a Debug build defined NDEBUG; the program printed:\n%s", got)
	}
	// With no configuration named there is no per-configuration flag to apply,
	// so NDEBUG is not defined -- which is also what CMake does.
	if got := buildAndRun(t, plainProject); !strings.Contains(got, "ndebug=no") {
		t.Errorf("an unconfigured build defined NDEBUG; the program printed:\n%s", got)
	}
}

// TestProjectCanAppendToTheFlags covers the idiom: a project reads the variable
// it was given and adds to it. Both halves have to survive.
func TestProjectCanAppendToTheFlags(t *testing.T) {
	got := buildAndRun(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -DFROM_FLAGS=1")
set(CMAKE_C_FLAGS_RELEASE "${CMAKE_C_FLAGS_RELEASE} -DFROM_CONFIG_FLAGS=1")
add_executable(app main.c)
`, "-DCMAKE_BUILD_TYPE=Release")
	if !strings.Contains(got, "from_flags=yes") {
		t.Errorf("what the project added to CMAKE_C_FLAGS did not reach the compiler:\n%s", got)
	}
	if !strings.Contains(got, "from_config=yes") {
		t.Errorf("what the project added to CMAKE_C_FLAGS_RELEASE did not reach the compiler:\n%s", got)
	}
	if !strings.Contains(got, "ndebug=yes") {
		t.Errorf("appending to the flags dropped what was already in them:\n%s", got)
	}
}

// TestFlagsFromTheCommandLineReplaceTheDefaults covers the other direction: -D
// on the command line is the last word, because it is how somebody builds a
// project the way they need it without editing it.
func TestFlagsFromTheCommandLineReplaceTheDefaults(t *testing.T) {
	got := buildAndRun(t, plainProject,
		"-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_C_FLAGS_RELEASE=-DFROM_CONFIG_FLAGS=1")
	if !strings.Contains(got, "from_config=yes") {
		t.Errorf("the flags given on the command line did not reach the compiler:\n%s", got)
	}
	if !strings.Contains(got, "ndebug=no") {
		t.Errorf("the command line was supposed to replace the default, not join it:\n%s", got)
	}
}

// TestCompileCommandsCarryTheSameFlags covers what an editor sees. clangd parses
// the file with whatever compile_commands.json says, so a command there that
// omits the definitions the build uses makes the editor report errors the
// compiler does not.
func TestCompileCommandsCarryTheSameFlags(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "CMakeLists.txt", plainProject)
	write(t, dir, "main.c", probeProgram)
	build := filepath.Join(dir, "b")
	if code, out, errOut := runCLI(t, dir, "-S", dir, "-B", build,
		"-DCMAKE_BUILD_TYPE=Release", "-DCMAKE_EXPORT_COMPILE_COMMANDS=ON"); code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(build, "compile_commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Command   string   `json:"command"`
		Arguments []string `json:"arguments"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("compile_commands.json is empty")
	}
	text := entries[0].Command + " " + strings.Join(entries[0].Arguments, " ")
	if !strings.Contains(text, "NDEBUG") {
		t.Errorf("the compile command an editor reads has no NDEBUG, but the build does:\n%s", text)
	}
}
