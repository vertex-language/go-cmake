package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A probe that could not be attempted is not the same as one that answered no,
// and the difference matters: a project told its compiler lacks a feature will
// disable it, and nothing downstream says why. Configure reports the difference
// at the end, and this is the test that keeps the State field it reads from
// going back to being collected and never looked at.

// TestConfigureWarnsWhenAProbeCouldNotBeAttempted uses a language no toolchain
// here provides. That is the condition in the wild too -- a project probing
// Fortran on a machine with only a C compiler -- and it is the one case that
// cannot be answered either way.
func TestConfigureWarnsWhenAProbeCouldNotBeAttempted(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "probe.f90"), "program p\nend program p\n")
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES NONE)
try_compile(HAVE_IT ${CMAKE_CURRENT_BINARY_DIR}
            SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/probe.f90
            LANGUAGE Fortran)
if(HAVE_IT)
  message(STATUS "probe succeeded")
else()
  message(STATUS "probe failed")
endif()
`)

	code, out, errOut := run(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed with %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "try_compile") {
		t.Errorf("a probe went unanswered and configure said nothing:\n%s", errOut)
	}
	if !strings.Contains(out, "probe failed") {
		t.Errorf("an unanswerable probe did not report failure:\n%s", out)
	}
}

// TestConfigureIsSilentWhenAProbeCanBeAnswered is the other half. A probe that
// ran and said no is an answer, not a gap, and warning about it would make the
// warning meaningless.
func TestConfigureIsSilentWhenAProbeCanBeAnswered(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "bad.c"), "this is not C\n")
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
try_compile(HAVE_IT ${CMAKE_CURRENT_BINARY_DIR} SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/bad.c)
if(HAVE_IT)
  message(STATUS "probe succeeded")
else()
  message(STATUS "probe failed")
endif()
`)

	code, out, errOut := run(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed with %d: %s", code, errOut)
	}
	if !strings.Contains(out, "probe failed") {
		t.Errorf("a probe of invalid source did not report failure:\n%s", out)
	}
	if strings.Contains(errOut, "CMake Warning") {
		t.Errorf("configure warned about a probe it answered:\n%s", errOut)
	}
}

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
