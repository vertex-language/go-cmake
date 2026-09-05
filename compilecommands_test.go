package cmake_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compile_commands.json is read by clangd, ccls, and every editor that offers
// completion for C or C++. A wrong entry does not break the build -- it breaks
// the editor, which reports errors in code that compiles fine and sends people
// looking in the wrong place. So these check the content, not that a file
// appeared.

type compileEntry struct {
	Directory string `json:"directory"`
	Command   string `json:"command"`
	File      string `json:"file"`
	Output    string `json:"output"`
}

// databaseFor configures a project and returns the parsed database.
func databaseFor(t *testing.T, extra string) (dir string, entries []compileEntry) {
	t.Helper()
	dir = t.TempDir()
	write := func(name, content string) {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)
add_library(util STATIC src/util.c inc/util.h)
target_include_directories(util PUBLIC inc)
target_compile_definitions(util PRIVATE BUILDING_UTIL=1)
add_executable(app src/main.c)
target_link_libraries(app PRIVATE util)
`+extra)
	write("inc/util.h", "int answer(void);\n")
	write("src/util.c", "#include \"util.h\"\nint answer(void){return 42;}\n")
	write("src/main.c", "int main(void){return 0;}\n")

	build := filepath.Join(dir, "b")
	if code, out, errOut := runCLI(t, dir, "-S", dir, "-B", build); code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	data, err := os.ReadFile(filepath.Join(build, "compile_commands.json"))
	if err != nil {
		t.Fatalf("no compile_commands.json was written: %v", err)
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("the database is not valid JSON: %v\n%s", err, data)
	}
	return dir, entries
}

func TestCompileCommandsCoversEveryTranslationUnit(t *testing.T) {
	_, entries := databaseFor(t, "")

	if len(entries) != 2 {
		t.Fatalf("want one entry per compiled source, got %d: %+v", len(entries), entries)
	}
	// The header listed as a source of util must not appear: it has no compile
	// command, and an entry for it would make a language server try to build one.
	for _, e := range entries {
		if strings.HasSuffix(e.File, ".h") {
			t.Errorf("a header got an entry: %s", e.File)
		}
	}
}

func TestCompileCommandsEntriesResolve(t *testing.T) {
	_, entries := databaseFor(t, "")

	for _, e := range entries {
		if _, err := os.Stat(e.File); err != nil {
			t.Errorf("entry names a file that is not there: %s", e.File)
		}
		if info, err := os.Stat(e.Directory); err != nil || !info.IsDir() {
			t.Errorf("entry names a directory that is not there: %s", e.Directory)
		}
		if e.Output == "" {
			t.Errorf("entry for %s has no output", e.File)
		}
		if !strings.Contains(e.Command, e.File) {
			t.Errorf("the command for %s does not mention the file:\n%s", e.File, e.Command)
		}
	}
}

// TestCompileCommandsCarryTheTargetsSettings is the point of the file: an
// editor has to be told the same include paths and definitions the compiler
// gets, or it reports missing headers the build finds.
func TestCompileCommandsCarryTheTargetsSettings(t *testing.T) {
	dir, entries := databaseFor(t, "")

	var utilCommand string
	for _, e := range entries {
		if strings.HasSuffix(filepath.ToSlash(e.File), "src/util.c") {
			utilCommand = e.Command
		}
	}
	if utilCommand == "" {
		t.Fatalf("no entry for src/util.c: %+v", entries)
	}
	if !strings.Contains(filepath.ToSlash(utilCommand), filepath.ToSlash(filepath.Join(dir, "inc"))) {
		t.Errorf("the public include directory is missing:\n%s", utilCommand)
	}
	if !strings.Contains(utilCommand, "BUILDING_UTIL=1") {
		t.Errorf("the private definition is missing:\n%s", utilCommand)
	}
}

// TestCompileCommandsIsOptIn covers the default. Writing the file unasked would
// be harmless, but CMake does not, and a build tree that differs from CMake's
// for no reason is a surprise waiting to happen.
func TestCompileCommandsIsOptIn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(`
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_executable(app main.c)
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.c"), []byte("int main(void){return 0;}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	build := filepath.Join(dir, "b")
	runCLI(t, dir, "-S", dir, "-B", build)
	if _, err := os.Stat(filepath.Join(build, "compile_commands.json")); err == nil {
		t.Error("a database was written without being asked for")
	}

	// And the command line can ask for it, which is how an editor gets one from
	// a project that never mentions it.
	build2 := filepath.Join(dir, "b2")
	runCLI(t, dir, "-S", dir, "-B", build2, "-DCMAKE_EXPORT_COMPILE_COMMANDS=ON")
	if _, err := os.Stat(filepath.Join(build2, "compile_commands.json")); err != nil {
		t.Errorf("-DCMAKE_EXPORT_COMPILE_COMMANDS=ON wrote nothing: %v", err)
	}
}
