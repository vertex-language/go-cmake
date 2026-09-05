package cmake_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/cli"
)

// A test runner that reports the wrong thing is worse than none, so these check
// the verdict rather than the plumbing: that a passing test passes, that
// WILL_FAIL inverts, that a skip is not a failure, and -- the one that decides
// whether a CI job goes red -- that the exit status follows.

const testingProject = `
cmake_minimum_required(VERSION 3.16)
project(D LANGUAGES C)
enable_testing()
add_executable(prog main.c)

add_test(NAME passing COMMAND prog 0)
add_test(NAME failing COMMAND prog 1)

add_test(NAME expected_failure COMMAND prog 1)
set_tests_properties(expected_failure PROPERTIES WILL_FAIL TRUE)

add_test(NAME labelled COMMAND prog 0)
set_tests_properties(labelled PROPERTIES LABELS "unit;fast")

add_test(NAME skipping COMMAND prog 77)
set_tests_properties(skipping PROPERTIES SKIP_RETURN_CODE 77)

add_test(NAME turned_off COMMAND prog 1)
set_tests_properties(turned_off PROPERTIES DISABLED TRUE)

add_subdirectory(sub)
`

const testingProgram = `
#include <stdlib.h>
#include <stdio.h>
int main(int argc, char **argv) {
  int code = argc > 1 ? atoi(argv[1]) : 0;
  printf("prog exiting with %d\n", code);
  return code;
}
`

// testingTree writes and builds the project the ctest cases share.
func testingTree(t *testing.T) (source, build string) {
	t.Helper()
	source = t.TempDir()
	write := func(name, content string) {
		full := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("CMakeLists.txt", testingProject)
	write("main.c", testingProgram)
	write("sub/CMakeLists.txt", "add_test(NAME nested COMMAND prog 0)\n")

	build = filepath.Join(source, "b")
	if code, out, errOut := runCLI(t, source, "-S", source, "-B", build); code != 0 {
		t.Fatalf("configure failed: %s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, source, "--build", build); code != 0 {
		t.Fatalf("build failed: %s%s", out, errOut)
	}
	return source, build
}

// runCTest drives the ctest command line.
func runCTest(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	var out, errBuf strings.Builder
	code := cli.CTestMain(t.Context(), cli.Env{
		Args: args, Dir: dir, Env: os.Environ(), Out: &out, Err: &errBuf,
	})
	return code, out.String() + errBuf.String()
}

func TestCTestJudgesEachTestCorrectly(t *testing.T) {
	source, build := testingTree(t)
	code, out := runCTest(t, source, "--test-dir", build)

	if code == 0 {
		t.Errorf("a run with a failing test exited 0:\n%s", out)
	}
	for _, want := range []string{
		"Test #1: passing",
		"Test #2: failing",
		"Test #3: expected_failure",
		"Test #7: nested",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// The verdicts are what matter: a WILL_FAIL test that exits non-zero passed,
	// and the one that genuinely failed did not.
	assertVerdict(t, out, "passing", "Passed")
	assertVerdict(t, out, "failing", "***Failed")
	assertVerdict(t, out, "expected_failure", "Passed")
	assertVerdict(t, out, "skipping", "***Skipped")
	assertVerdict(t, out, "turned_off", "***Not Run")

	// A disabled test is not counted at all; a skipped one counts as not failed.
	if !strings.Contains(out, "1 tests failed out of 6") {
		t.Errorf("the totals do not exclude the disabled test:\n%s", out)
	}
}

func assertVerdict(t *testing.T, output, test, want string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, " "+test+" ") {
			if !strings.Contains(line, want) {
				t.Errorf("%s: want %q, got: %s", test, want, strings.TrimSpace(line))
			}
			return
		}
	}
	t.Errorf("no result line for %s in:\n%s", test, output)
}

func TestCTestPassesWhenNothingFails(t *testing.T) {
	source, build := testingTree(t)
	code, out := runCTest(t, source, "--test-dir", build, "-E", "^failing$")
	if code != 0 {
		t.Errorf("a run with no failures exited %d:\n%s", code, out)
	}
}

func TestCTestFiltersByNameAndLabel(t *testing.T) {
	source, build := testingTree(t)

	_, byName := runCTest(t, source, "--test-dir", build, "-N", "-R", "^lab")
	if !strings.Contains(byName, "labelled") || strings.Contains(byName, "passing") {
		t.Errorf("-R selected the wrong tests:\n%s", byName)
	}

	_, byLabel := runCTest(t, source, "--test-dir", build, "-N", "-L", "unit")
	if !strings.Contains(byLabel, "labelled") || strings.Contains(byLabel, "passing") {
		t.Errorf("-L selected the wrong tests:\n%s", byLabel)
	}

	_, excluded := runCTest(t, source, "--test-dir", build, "-N", "-LE", "unit")
	if strings.Contains(excluded, "labelled") {
		t.Errorf("-LE did not exclude the labelled test:\n%s", excluded)
	}
}

// TestCTestRunsSubdirectoryTests covers the chain from the top of the build
// tree down: a test declared in a subdirectory has to be reachable from the
// root test file, or it silently never runs.
func TestCTestRunsSubdirectoryTests(t *testing.T) {
	source, build := testingTree(t)
	_, out := runCTest(t, source, "--test-dir", build, "-R", "nested")
	if !strings.Contains(out, "nested") {
		t.Errorf("a subdirectory's test was not found:\n%s", out)
	}
	if !strings.Contains(out, "1 tests failed out of 1") && !strings.Contains(out, "100% tests passed") {
		t.Errorf("the subdirectory test did not run:\n%s", out)
	}
}

// TestCTestFromASubdirectoryRunsOnlyThatSubtree is the reason the test files
// are written per directory rather than as one list.
func TestCTestFromASubdirectoryRunsOnlyThatSubtree(t *testing.T) {
	source, build := testingTree(t)
	_, out := runCTest(t, source, "--test-dir", filepath.Join(build, "sub"), "-N")
	if !strings.Contains(out, "nested") {
		t.Errorf("the subdirectory's own test was missing:\n%s", out)
	}
	if strings.Contains(out, "passing") {
		t.Errorf("a parent directory's test leaked into the subtree run:\n%s", out)
	}
}

func TestCTestOutputOnFailureShowsTheOutput(t *testing.T) {
	source, build := testingTree(t)
	_, quiet := runCTest(t, source, "--test-dir", build, "-R", "^failing$")
	if strings.Contains(quiet, "prog exiting with 1") {
		t.Errorf("a failing test's output was printed without being asked for:\n%s", quiet)
	}
	_, loud := runCTest(t, source, "--test-dir", build, "-R", "^failing$", "--output-on-failure")
	if !strings.Contains(loud, "prog exiting with 1") {
		t.Errorf("--output-on-failure printed nothing:\n%s", loud)
	}
}

func TestCTestWithoutATestFileSaysWhy(t *testing.T) {
	dir := t.TempDir()
	code, out := runCTest(t, dir, "--test-dir", dir)
	if code == 0 {
		t.Error("testing a directory with no tests succeeded")
	}
	if !strings.Contains(out, "CTestTestfile.cmake") {
		t.Errorf("the error does not name what is missing: %s", out)
	}
}
