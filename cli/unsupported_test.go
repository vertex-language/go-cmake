package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// try_compile reports failure rather than a false success, which can quietly
// switch off a feature the compiler does in fact have. The warning at the end
// of configure is the only thing that tells anyone, so it is worth a test:
// without one, the State field it reads would go back to being collected and
// never looked at.
func TestConfigureWarnsAboutUnsupportedCommands(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES NONE)
try_compile(HAVE_IT ${CMAKE_CURRENT_BINARY_DIR} ${CMAKE_CURRENT_SOURCE_DIR}/probe.c)
if(HAVE_IT)
  message(STATUS "probe succeeded")
else()
  message(STATUS "probe failed")
endif()
`)
	mustWrite(t, filepath.Join(dir, "probe.c"), "int main(void){return 0;}\n")

	code, out, errOut := run(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed with %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "try_compile") {
		t.Errorf("configure used try_compile but said nothing about it:\n%s", errOut)
	}
	// The probe must report failure, not a made-up success.
	if !strings.Contains(out, "probe failed") {
		t.Errorf("try_compile did not report failure:\n%s", out)
	}
}

// TestConfigureIsSilentWhenNothingIsUnsupported is the other half: a project
// that asks for nothing unimplemented must not be warned at.
func TestConfigureIsSilentWhenNothingIsUnsupported(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES NONE)
message(STATUS "nothing unusual here")
`)

	code, _, errOut := run(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed with %d: %s", code, errOut)
	}
	if strings.Contains(errOut, "CMake Warning") {
		t.Errorf("warned about a project that asked for nothing unsupported:\n%s", errOut)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
