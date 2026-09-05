package cmake_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A feature probe that answers without asking is worse than one that is
// missing. A project told its compiler lacks something it has will disable the
// feature, and nothing downstream points at why. So these tests check the
// answers, not that a probe ran -- and they check both directions, because a
// probe that always says yes passes a one-sided test.

// probeProject configures a project full of checks and returns what it printed.
func probeProject(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	source := "cmake_minimum_required(VERSION 3.16)\nproject(P LANGUAGES C)\n" + body
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	return out + errOut
}

func TestTryCompileAnswersBothWays(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("good.c", "#include <stdio.h>\nint main(void) { return 0; }\n")
	write("bad.c", "this is not C\n")
	write("CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
try_compile(GOOD ${CMAKE_CURRENT_BINARY_DIR} SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/good.c)
try_compile(BAD ${CMAKE_CURRENT_BINARY_DIR} SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/bad.c)
message(STATUS "good=${GOOD} bad=${BAD}")
`)
	code, out, errOut := runCLI(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	if !strings.Contains(out, "good=TRUE bad=FALSE") {
		t.Errorf("try_compile did not distinguish the two:\n%s", out)
	}
	// The old warning about an unanswered probe must be gone.
	if strings.Contains(errOut, "configured without try_compile") {
		t.Errorf("try_compile still reports itself unsupported:\n%s", errOut)
	}
}

// TestTryRunReportsTheProgramsExitStatus is the whole point of try_run: the
// answer is in what the built program did, not in whether it built.
func TestTryRunReportsTheProgramsExitStatus(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seven.c"),
		[]byte("int main(void) { return 7; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
try_run(RAN BUILT ${CMAKE_CURRENT_BINARY_DIR} SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/seven.c)
message(STATUS "built=${BUILT} ran=${RAN}")
`), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, dir, "-S", dir, "-B", filepath.Join(dir, "b"))
	if code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	if !strings.Contains(out, "built=TRUE ran=7") {
		t.Errorf("try_run did not report the exit status:\n%s", out)
	}
}

func TestCheckIncludeFile(t *testing.T) {
	out := probeProject(t, `
include(CheckIncludeFile)
check_include_file(stdio.h HAVE_STDIO)
check_include_file(no_such_header_anywhere.h HAVE_NONSENSE)
message(STATUS "RESULT stdio=[${HAVE_STDIO}] nonsense=[${HAVE_NONSENSE}]")
`)
	assertContains(t, out, "RESULT stdio=[1] nonsense=[]")
}

func TestCheckSymbolExistsSeesFunctionsAndNotInventedOnes(t *testing.T) {
	out := probeProject(t, `
include(CheckSymbolExists)
check_symbol_exists(printf stdio.h HAVE_PRINTF)
check_symbol_exists(no_such_symbol_at_all stdio.h HAVE_NOTHING)
message(STATUS "RESULT printf=[${HAVE_PRINTF}] nothing=[${HAVE_NOTHING}]")
`)
	assertContains(t, out, "RESULT printf=[1] nothing=[]")
}

// TestCheckTypeSizeComputesTheSize covers the probe that has no run step: the
// size is found by compiling an array whose dimension is negative unless the
// guess is right, which is what makes it work when cross-compiling.
func TestCheckTypeSizeComputesTheSize(t *testing.T) {
	out := probeProject(t, `
include(CheckTypeSize)
check_type_size(int SIZEOF_INT)
check_type_size("long long" SIZEOF_LL)
check_type_size(struct_that_does_not_exist SIZEOF_NOTHING)
message(STATUS "RESULT int=[${SIZEOF_INT}] ll=[${SIZEOF_LL}] none=[${SIZEOF_NOTHING}]")
`)
	assertContains(t, out, "RESULT int=[4] ll=[8] none=[]")
}

func TestCheckSourceCompiles(t *testing.T) {
	out := probeProject(t, `
include(CheckCSourceCompiles)
check_c_source_compiles("int main(void){return 0;}" TRIVIAL_OK)
check_c_source_compiles("not valid c at all" TRIVIAL_BAD)
message(STATUS "RESULT ok=[${TRIVIAL_OK}] bad=[${TRIVIAL_BAD}]")
`)
	assertContains(t, out, "RESULT ok=[1] bad=[]")
}

func TestCheckFunctionExists(t *testing.T) {
	out := probeProject(t, `
include(CheckFunctionExists)
check_function_exists(malloc HAVE_MALLOC)
check_function_exists(a_function_nobody_wrote HAVE_INVENTED)
message(STATUS "RESULT malloc=[${HAVE_MALLOC}] invented=[${HAVE_INVENTED}]")
`)
	assertContains(t, out, "RESULT malloc=[1] invented=[]")
}

// TestCheckResultsArePersisted covers the caching: a probe is expensive, and
// its answer is a property of the compiler rather than of the run.
func TestCheckResultsArePersisted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
include(CheckIncludeFile)
check_include_file(stdio.h HAVE_STDIO)
message(STATUS "RESULT stdio=[${HAVE_STDIO}]")
`), 0644); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(dir, "b")

	_, first, _ := runCLI(t, dir, "-S", dir, "-B", build)
	if !strings.Contains(first, "Performing Test HAVE_STDIO") {
		t.Errorf("the first configure did not run the probe:\n%s", first)
	}

	_, second, _ := runCLI(t, dir, "-S", dir, "-B", build)
	if strings.Contains(second, "Performing Test HAVE_STDIO") {
		t.Errorf("the second configure re-ran a probe it had already answered:\n%s", second)
	}
	if !strings.Contains(second, "RESULT stdio=[1]") {
		t.Errorf("the cached answer did not come back:\n%s", second)
	}
}

func assertContains(t *testing.T, output, want string) {
	t.Helper()
	if !strings.Contains(output, want) {
		t.Errorf("want %q in output:\n%s", want, output)
	}
}
