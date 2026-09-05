package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A build directory that forgets is the fastest way to waste a developer's
// afternoon: `cmake -B build -DWITH_SSL=ON` once, `cmake -B build` later, and
// the second configure quietly produces a different build from the first. These
// tests are about that, not about the file format.

const cacheProject = `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES NONE)
option(WITH_SSL "use ssl" OFF)
message(STATUS "WITH_SSL=[${WITH_SSL}] EXTRA=[${EXTRA}]")
`

func TestCacheRemembersCommandLineDefinitions(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), cacheProject)
	build := filepath.Join(dir, "b")

	if _, out, errOut := run(t, dir, "-S", dir, "-B", build, "-DWITH_SSL=ON", "-DEXTRA=hello"); true {
		if !strings.Contains(out, "WITH_SSL=[ON] EXTRA=[hello]") {
			t.Fatalf("first configure did not take the -D values:\n%s%s", out, errOut)
		}
	}

	// The second run says nothing about either value; both must survive.
	_, out, errOut := run(t, dir, "-S", dir, "-B", build)
	if !strings.Contains(out, "WITH_SSL=[ON] EXTRA=[hello]") {
		t.Errorf("re-configure forgot the cache:\n%s%s", out, errOut)
	}

	if _, err := os.Stat(filepath.Join(build, "CMakeCache.txt")); err != nil {
		t.Errorf("no CMakeCache.txt was written: %v", err)
	}
}

func TestFreshDiscardsTheCache(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), cacheProject)
	build := filepath.Join(dir, "b")

	run(t, dir, "-S", dir, "-B", build, "-DWITH_SSL=ON", "-DEXTRA=hello")
	_, out, _ := run(t, dir, "-S", dir, "-B", build, "--fresh")

	// WITH_SSL falls back to the option's own default, and EXTRA is gone.
	if !strings.Contains(out, "WITH_SSL=[OFF] EXTRA=[]") {
		t.Errorf("--fresh did not discard the cache:\n%s", out)
	}
}

func TestUnsetRemovesOnlyMatchingEntries(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), cacheProject)
	build := filepath.Join(dir, "b")

	run(t, dir, "-S", dir, "-B", build, "-DWITH_SSL=ON", "-DEXTRA=hello")
	_, out, _ := run(t, dir, "-S", dir, "-B", build, "-U", "EXTRA")

	if !strings.Contains(out, "WITH_SSL=[ON] EXTRA=[]") {
		t.Errorf("-U removed the wrong entries:\n%s", out)
	}
}

func TestUnsetAcceptsAGlob(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES NONE)
message(STATUS "a=[${MY_A}] b=[${MY_B}] other=[${OTHER}]")
`)
	build := filepath.Join(dir, "b")

	run(t, dir, "-S", dir, "-B", build, "-DMY_A=1", "-DMY_B=2", "-DOTHER=3")
	_, out, _ := run(t, dir, "-S", dir, "-B", build, "-U", "MY_*")

	if !strings.Contains(out, "a=[] b=[] other=[3]") {
		t.Errorf("-U MY_* did not match by glob:\n%s", out)
	}
}

// TestCacheFileIsReadableAndReReadable checks the round trip through the file
// rather than through the run: an entry's type and help text have to survive,
// because -LH prints them and a person may edit them.
func TestCacheFileRoundTripsTypesAndHelp(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"), `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES NONE)
option(A_FLAG "the help text for a flag" ON)
set(A_PATH "/somewhere" CACHE PATH "the help text for a path")
set(HIDDEN "x" CACHE STRING "advanced one")
mark_as_advanced(HIDDEN)
`)
	build := filepath.Join(dir, "b")
	run(t, dir, "-S", dir, "-B", build)

	data, err := os.ReadFile(filepath.Join(build, "CMakeCache.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"A_FLAG:BOOL=ON",
		"the help text for a flag",
		"A_PATH:PATH=/somewhere",
		"HIDDEN-ADVANCED:INTERNAL=1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("CMakeCache.txt is missing %q:\n%s", want, text)
		}
	}

	// Re-reading must keep the advanced mark, or -L would start showing an
	// entry the project asked to hide.
	_, out, _ := run(t, dir, "-S", dir, "-B", build, "-N", "-L")
	if strings.Contains(out, "HIDDEN:") {
		t.Errorf("-L showed an advanced entry after a round trip:\n%s", out)
	}
	if !strings.Contains(out, "A_FLAG:BOOL=ON") {
		t.Errorf("-L lost a normal entry after a round trip:\n%s", out)
	}
}
