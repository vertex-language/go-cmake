package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/cli"
)

// run invokes the command line the way a shell would and returns its status
// and output. Nothing here shells out: Env carries every effect, which is the
// point of it being a struct.
func run(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf strings.Builder
	code := cli.Main(context.Background(), cli.Env{
		Args: args,
		Dir:  dir,
		Env:  os.Environ(),
		In:   strings.NewReader(""),
		Out:  &out,
		Err:  &errBuf,
	})
	return code, out.String(), errBuf.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := run(t, ".", "--version")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(out, "cmake version ") {
		t.Errorf("output = %q", out)
	}
}

func TestScriptMode(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "s.cmake")
	os.WriteFile(script, []byte(`
set(NAME world)
message(STATUS "hello ${NAME}")
math(EXPR N "6 * 7")
message(STATUS "answer=${N}")
`), 0644)

	code, out, errOut := run(t, dir, "-P", script)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "-- hello world") || !strings.Contains(out, "-- answer=42") {
		t.Errorf("output = %q", out)
	}
}

func TestScriptModeArguments(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "s.cmake")
	os.WriteFile(script, []byte(`message(STATUS "argc=${CMAKE_ARGC} first=${CMAKE_ARGV0}")`), 0644)

	code, out, _ := run(t, dir, "-P", script, "alpha", "beta")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "argc=2") || !strings.Contains(out, "first=alpha") {
		t.Errorf("output = %q", out)
	}
}

func TestScriptModeFatalError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "s.cmake")
	os.WriteFile(script, []byte("message(FATAL_ERROR \"stop\")\n"), 0644)

	code, _, errOut := run(t, dir, "-P", script)
	if code == 0 {
		t.Error("a fatal error must not exit zero")
	}
	if !strings.Contains(errOut, "stop") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestToolModeEcho(t *testing.T) {
	code, out, _ := run(t, ".", "-E", "echo", "one", "two")
	if code != 0 || strings.TrimSpace(out) != "one two" {
		t.Errorf("exit %d out %q", code, out)
	}
}

func TestToolModeFileOperations(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "sub", "b.txt")
	if err := os.WriteFile(src, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	if code, _, e := run(t, dir, "-E", "copy", src, dst); code != 0 {
		t.Fatalf("copy failed: %s", e)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "content" {
		t.Fatalf("copy produced %q, %v", data, err)
	}

	if code, _, _ := run(t, dir, "-E", "compare_files", src, dst); code != 0 {
		t.Error("compare_files reported a difference between identical files")
	}

	// copy_if_different must leave an identical destination alone, because
	// rewriting it would advance its timestamp and trigger a rebuild.
	before, _ := os.Stat(dst)
	if code, _, _ := run(t, dir, "-E", "copy_if_different", src, dst); code != 0 {
		t.Error("copy_if_different failed")
	}
	after, _ := os.Stat(dst)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("copy_if_different rewrote an identical file")
	}

	stamp := filepath.Join(dir, "stamp")
	if code, _, e := run(t, dir, "-E", "touch", stamp); code != 0 {
		t.Fatalf("touch failed: %s", e)
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Error("touch did not create the file")
	}

	if code, _, _ := run(t, dir, "-E", "remove", stamp); code != 0 {
		t.Error("remove failed")
	}
	if _, err := os.Stat(stamp); !os.IsNotExist(err) {
		t.Error("remove did not remove the file")
	}
}

func TestToolModeMakeAndRemoveDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	if code, _, e := run(t, dir, "-E", "make_directory", nested); code != 0 {
		t.Fatalf("make_directory failed: %s", e)
	}
	if fi, err := os.Stat(nested); err != nil || !fi.IsDir() {
		t.Fatal("make_directory did not create the tree")
	}
	if code, _, _ := run(t, dir, "-E", "remove_directory", filepath.Join(dir, "a")); code != 0 {
		t.Error("remove_directory failed")
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Error("remove_directory left the tree behind")
	}
}

func TestToolModeHash(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x")
	os.WriteFile(f, []byte("abc"), 0644)
	code, out, _ := run(t, dir, "-E", "sha256sum", f)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if !strings.HasPrefix(out, want) {
		t.Errorf("sha256sum = %q, want it to start with %s", out, want)
	}
}

func TestToolModeUnknownCommand(t *testing.T) {
	code, _, errOut := run(t, ".", "-E", "no_such_command")
	if code == 0 {
		t.Error("an unimplemented -E command must not exit zero")
	}
	if !strings.Contains(errOut, "no_such_command") {
		t.Errorf("stderr should name the command, got %q", errOut)
	}
}

func TestUnknownOption(t *testing.T) {
	code, _, errOut := run(t, ".", "--not-a-real-option")
	if code == 0 {
		t.Error("an unknown option must not exit zero")
	}
	if !strings.Contains(errOut, "not-a-real-option") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestConfigureWritesBuildFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(CliDemo LANGUAGES NONE)
message(STATUS "configuring ${PROJECT_NAME}")
`), 0644)

	build := filepath.Join(dir, "out")
	code, out, errOut := run(t, dir, "-S", dir, "-B", build)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "configuring CliDemo") {
		t.Errorf("stdout = %q", out)
	}
	if !strings.Contains(out, "Build files have been written to") {
		t.Errorf("configure did not report where it wrote: %q", out)
	}
	if _, err := os.Stat(filepath.Join(build, "build.ninja")); err != nil {
		t.Errorf("no build.ninja was written: %v", err)
	}
}

func TestConfigureReportsErrorAndExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(Bad LANGUAGES NONE)
message(FATAL_ERROR "deliberate")
`), 0644)

	code, _, errOut := run(t, dir, "-S", dir, "-B", filepath.Join(dir, "out"))
	if code == 0 {
		t.Error("a configure failure must not exit zero")
	}
	if !strings.Contains(errOut, "deliberate") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestCacheVariableFromCommandLine(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(Vars LANGUAGES NONE)
message(STATUS "value=${MY_SETTING}")
option(MY_OPTION "doc" OFF)
message(STATUS "option=${MY_OPTION}")
`), 0644)

	code, out, errOut := run(t, dir,
		"-S", dir, "-B", filepath.Join(dir, "out"),
		"-DMY_SETTING:STRING=chosen", "-DMY_OPTION=ON")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "value=chosen") {
		t.Errorf("-D did not reach the project: %q", out)
	}
	// A -D setting must win over the project's own option() default; that is
	// the entire purpose of the cache.
	if !strings.Contains(out, "option=ON") {
		t.Errorf("-D did not override option(): %q", out)
	}
}
