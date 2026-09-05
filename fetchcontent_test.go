package cmake_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// FetchContent is how a modern project states its dependencies, so these tests
// go the whole way: fetch, configure, build, and run the result. A dependency
// that is fetched but not actually usable is the failure worth catching, and
// only running the program proves it.
//
// The remote is a git repository in a temporary directory. That is a real
// clone over a real transport, without reaching the network -- which is what
// makes the test something that can run anywhere.

// gitAvailable skips when there is no git to clone with.
func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
}

// dependencyRepo creates a git repository holding a small CMake project.
func dependencyRepo(t *testing.T, answer string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dep")
	write(t, dir, "CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(TheDep LANGUAGES C)
add_library(dep STATIC dep.c)
target_include_directories(dep PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
`)
	write(t, dir, "dep.c", "int dep_answer(void){return "+answer+";}\n")
	write(t, dir, "dep.h", "int dep_answer(void);\n")

	for _, argv := range [][]string{
		{"git", "init", "-q", "."},
		{"git", "add", "-A"},
		{"git", "-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-qm", "init"},
		{"git", "tag", "v1.0"},
	} {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", argv, err, out)
		}
	}
	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// consumer writes a project that fetches the dependency and uses it.
func consumer(t *testing.T, declare string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(App LANGUAGES C)
include(FetchContent)
`+declare+`
FetchContent_MakeAvailable(TheDep)
message(STATUS "populated=${thedep_POPULATED} src=${thedep_SOURCE_DIR}")
add_executable(app main.c)
target_link_libraries(app PRIVATE dep)
`)
	write(t, dir, "main.c", `
#include <stdio.h>
#include "dep.h"
int main(void){ printf("answer=%d\n", dep_answer()); return 0; }
`)
	return dir
}

func TestFetchContentGitBuildsAndRuns(t *testing.T) {
	gitAvailable(t)
	repo := dependencyRepo(t, "42")
	dir := consumer(t, `FetchContent_Declare(TheDep GIT_REPOSITORY "file://`+filepath.ToSlash(repo)+`" GIT_TAG v1.0)`)

	build := filepath.Join(dir, "b")
	code, out, errOut := runCLI(t, dir, "-S", dir, "-B", build)
	if code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	if !strings.Contains(out, "populated=TRUE") {
		t.Errorf("the dependency was not populated:\n%s", out)
	}
	if code, out, errOut := runCLI(t, dir, "--build", build); code != 0 {
		t.Fatalf("build failed:\n%s%s", out, errOut)
	}

	// The only proof that a dependency is really available is that the program
	// linking it runs.
	if got := runProgram(t, filepath.Join(build, "app"+exeSuffix())); !strings.Contains(got, "answer=42") {
		t.Errorf("the built program printed %q", got)
	}
}

// TestFetchContentDoesNotRefetchAnUnchangedDependency covers the cost of
// getting this wrong: a clone on every configure turns a two-second no-op into
// a minute.
func TestFetchContentDoesNotRefetchAnUnchangedDependency(t *testing.T) {
	gitAvailable(t)
	repo := dependencyRepo(t, "42")
	dir := consumer(t, `FetchContent_Declare(TheDep GIT_REPOSITORY "file://`+filepath.ToSlash(repo)+`" GIT_TAG v1.0)`)
	build := filepath.Join(dir, "b")

	_, first, _ := runCLI(t, dir, "-S", dir, "-B", build)
	if !strings.Contains(first, "Populating TheDep") {
		t.Fatalf("the first configure did not populate:\n%s", first)
	}
	_, second, _ := runCLI(t, dir, "-S", dir, "-B", build)
	if strings.Contains(second, "Populating TheDep") {
		t.Errorf("the second configure fetched again:\n%s", second)
	}
	if !strings.Contains(second, "populated=TRUE") {
		t.Errorf("the second configure lost the dependency:\n%s", second)
	}
}

// TestFetchContentSourceDirOverrideWins is how anyone actually works on a
// dependency: point the build at a checkout and expect it to be used as-is.
func TestFetchContentSourceDirOverrideWins(t *testing.T) {
	gitAvailable(t)
	repo := dependencyRepo(t, "42")

	// A separate working tree whose answer differs, so which one was used is
	// visible in the program's output rather than inferred.
	local := filepath.Join(t.TempDir(), "local")
	write(t, local, "CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(TheDep LANGUAGES C)
add_library(dep STATIC dep.c)
target_include_directories(dep PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
`)
	write(t, local, "dep.c", "int dep_answer(void){return 99;}\n")
	write(t, local, "dep.h", "int dep_answer(void);\n")

	dir := consumer(t, `FetchContent_Declare(TheDep GIT_REPOSITORY "file://`+filepath.ToSlash(repo)+`" GIT_TAG v1.0)`)
	build := filepath.Join(dir, "b")

	code, out, errOut := runCLI(t, dir, "-S", dir, "-B", build,
		"-DFETCHCONTENT_SOURCE_DIR_THEDEP="+filepath.ToSlash(local))
	if code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	if strings.Contains(out, "Populating TheDep") {
		t.Errorf("a local checkout was overwritten by a fetch:\n%s", out)
	}
	if code, out, errOut := runCLI(t, dir, "--build", build); code != 0 {
		t.Fatalf("build failed:\n%s%s", out, errOut)
	}
	if got := runProgram(t, filepath.Join(build, "app"+exeSuffix())); !strings.Contains(got, "answer=99") {
		t.Errorf("the local checkout was not used; program printed %q", got)
	}
}

// TestFetchContentGetProperties covers the idiom a project uses to populate
// something itself rather than through MakeAvailable.
func TestFetchContentGetProperties(t *testing.T) {
	gitAvailable(t)
	repo := dependencyRepo(t, "42")
	dir := t.TempDir()
	write(t, dir, "CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(App LANGUAGES NONE)
include(FetchContent)
FetchContent_Declare(TheDep GIT_REPOSITORY "file://`+filepath.ToSlash(repo)+`" GIT_TAG v1.0)

FetchContent_GetProperties(TheDep)
message(STATUS "before=[${thedep_POPULATED}]")
FetchContent_Populate(TheDep)
FetchContent_GetProperties(TheDep POPULATED after)
message(STATUS "after=[${after}]")
`)
	code, out, errOut := runCLI(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	if !strings.Contains(out, "before=[]") {
		t.Errorf("a dependency was populated before it was asked for:\n%s", out)
	}
	if !strings.Contains(out, "after=[TRUE]") {
		t.Errorf("FetchContent_Populate did not report success:\n%s", out)
	}
}

// TestFetchContentWithoutADownloaderRefuses covers the library default: a
// caller that supplied no Downloader must not have one invented for it.
func TestFetchContentWithoutADownloaderRefuses(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(App LANGUAGES NONE)
file(DOWNLOAD https://example.invalid/x.txt ${CMAKE_CURRENT_BINARY_DIR}/x.txt STATUS st)
message(STATUS "st=[${st}]")
`)
	// Configure through the library rather than the command line, because the
	// command line is the program that decides to make requests.
	state := configureWithoutDownloader(t, dir)
	if !strings.Contains(state, "does not perform downloads") {
		t.Errorf("a download was attempted without a Downloader:\n%s", state)
	}
}

func runProgram(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(path).CombinedOutput()
	if err != nil {
		t.Fatalf("running %s: %v\n%s", path, err, out)
	}
	return string(out)
}
