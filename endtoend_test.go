package cmake_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cmake "github.com/vertex-language/go-cmake"
	"github.com/vertex-language/go-cmake/toolchain"
)

// These tests do the thing the whole package exists to do: read a
// CMakeLists.txt, work out what to compile, compile it, link it, and run the
// result. Everything else is a means to this.
//
// They need a C compiler. Without one there is nothing to assert, so they skip.

func requireCompiler(t *testing.T) {
	t.Helper()
	tc := toolchain.Detect([]string{"C"}, toolchain.OSEnv())
	if _, ok := tc.Compiler("C"); !ok {
		t.Skip("no C compiler found; skipping end-to-end build test")
	}
	if tc.Kind() == toolchain.MSVC && tc.Archiver == "" {
		t.Skip("MSVC found but lib.exe is not on PATH; skipping end-to-end build test")
	}
}

// project writes a source tree and returns its directory.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// buildProject runs all three phases and returns the build directory.
func buildProject(t *testing.T, source string) (string, *cmake.BuildResult) {
	t.Helper()
	binary := filepath.Join(source, "_build")
	var out strings.Builder
	c, err := cmake.New(cmake.Config{
		Source: source,
		Binary: binary,
		FS:     cmake.RealFS(""),
		Runner: cmake.RealRunner(),
		Out:    &out,
		Err:    &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Build(context.Background())
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out.String())
	}
	if res.Failed > 0 {
		t.Fatalf("build reported %d failures\n%s", res.Failed, out.String())
	}
	t.Logf("build output:\n%s", out.String())
	return binary, res
}

// runExe runs a built executable and returns its stdout.
func runExe(t *testing.T, path string) string {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected executable at %s: %v", path, err)
	}
	cmd := exec.Command(path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running %s: %v\n%s", path, err, out)
	}
	return strings.TrimSpace(string(out))
}

func exeName(base string) string {
	tc := toolchain.Detect([]string{"C"}, toolchain.OSEnv())
	return base + tc.ExeSuffix
}

func TestEndToEndExecutable(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Hello LANGUAGES C)
add_executable(hello main.c)
`,
		"main.c": `#include <stdio.h>
int main(void) { printf("hello from go-cmake\n"); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	got := runExe(t, filepath.Join(binary, exeName("hello")))
	if got != "hello from go-cmake" {
		t.Errorf("program printed %q", got)
	}
}

func TestEndToEndStaticLibrary(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Lib LANGUAGES C)
add_library(mathlib STATIC lib/add.c)
target_include_directories(mathlib PUBLIC include)
add_executable(app app.c)
target_link_libraries(app PRIVATE mathlib)
`,
		"include/add.h": "int add(int a, int b);\n",
		"lib/add.c":     "#include \"add.h\"\nint add(int a, int b) { return a + b; }\n",
		"app.c": `#include <stdio.h>
#include "add.h"
int main(void) { printf("%d\n", add(19, 23)); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "42" {
		t.Errorf("program printed %q, want 42", got)
	}
}

func TestEndToEndUsageRequirements(t *testing.T) {
	requireCompiler(t)
	// The point of this one is propagation: app never mentions the include
	// directory or the definition, and gets both through mathlib's interface.
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Usage LANGUAGES C)
add_library(base STATIC base.c)
target_include_directories(base PUBLIC inc)
target_compile_definitions(base PUBLIC SCALE=3 PRIVATE INTERNAL_ONLY=1)
add_library(middle STATIC middle.c)
target_link_libraries(middle PUBLIC base)
add_executable(app app.c)
target_link_libraries(app PRIVATE middle)
`,
		"inc/api.h": "int scaled(int v);\nint middled(int v);\n",
		"base.c": `#include "api.h"
#ifndef SCALE
#error SCALE must be visible here
#endif
#ifndef INTERNAL_ONLY
#error INTERNAL_ONLY must be visible in base itself
#endif
int scaled(int v) { return v * SCALE; }
`,
		"middle.c": `#include "api.h"
#ifndef SCALE
#error SCALE must propagate through base's interface
#endif
#ifdef INTERNAL_ONLY
#error INTERNAL_ONLY is private to base and must not propagate
#endif
int middled(int v) { return scaled(v) + 1; }
`,
		"app.c": `#include <stdio.h>
#include "api.h"
#ifndef SCALE
#error SCALE must propagate transitively to app
#endif
int main(void) { printf("%d\n", middled(5)); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "16" {
		t.Errorf("program printed %q, want 16", got)
	}
}

func TestEndToEndSubdirectories(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Tree LANGUAGES C)
add_subdirectory(lib)
add_executable(app app.c)
target_link_libraries(app PRIVATE greet)
`,
		"lib/CMakeLists.txt": `
add_library(greet STATIC greet.c)
target_include_directories(greet PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
`,
		"lib/greet.h": "const char *greeting(void);\n",
		"lib/greet.c": "#include \"greet.h\"\nconst char *greeting(void) { return \"from a subdirectory\"; }\n",
		"app.c": `#include <stdio.h>
#include "greet.h"
int main(void) { printf("%s\n", greeting()); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "from a subdirectory" {
		t.Errorf("program printed %q", got)
	}
}

func TestEndToEndConfigureFile(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Configured VERSION 4.2.0 LANGUAGES C)
set(HAVE_FEATURE ON)
configure_file(config.h.in config.h)
add_executable(app app.c)
target_include_directories(app PRIVATE ${CMAKE_CURRENT_BINARY_DIR})
`,
		"config.h.in": `#define VERSION "@PROJECT_VERSION@"
#cmakedefine HAVE_FEATURE
#cmakedefine MISSING_FEATURE
`,
		"app.c": `#include <stdio.h>
#include "config.h"
int main(void) {
#ifndef HAVE_FEATURE
#error HAVE_FEATURE should be defined
#endif
#ifdef MISSING_FEATURE
#error MISSING_FEATURE should not be defined
#endif
  printf("%s\n", VERSION);
  return 0;
}
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "4.2.0" {
		t.Errorf("program printed %q, want 4.2.0", got)
	}
}

func TestEndToEndIncremental(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Incremental LANGUAGES C)
add_executable(app a.c b.c)
`,
		// Both files include a system header, so the dependencies recorded for
		// them include paths under "Program Files". A dependency log that
		// mangles those makes every object look stale on every run, and this is
		// the test that catches it.
		"a.c": "#include <stdio.h>\nint b(void);\nint main(void) { return b(); }\n",
		"b.c": "#include <stdio.h>\nint b(void) { return 0; }\n",
	})
	binary, first := buildProject(t, src)
	if first.Built == 0 {
		t.Fatal("first build compiled nothing")
	}

	// A second build with nothing changed must do no work. This is the property
	// that separates a build system from a script.
	_, second := buildProject(t, src)
	if second.Built != 0 {
		t.Errorf("second build recompiled %d edges; expected none", second.Built)
	}

	// Touching one source must rebuild that object and relink, but not the
	// other object.
	if err := os.WriteFile(filepath.Join(src, "b.c"), []byte("int b(void) { return 0; }\n/* changed */\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, third := buildProject(t, src)
	if third.Built == 0 {
		t.Error("build after a source change did nothing")
	}
	if third.Built > 2 {
		t.Errorf("build after one source change ran %d edges; expected the object and the link", third.Built)
	}
	_ = binary
}

func TestEndToEndHeaderDependency(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Headers LANGUAGES C)
add_executable(app main.c)
`,
		"value.h": "#define VALUE 1\n",
		"main.c": `#include <stdio.h>
#include "value.h"
int main(void) { printf("%d\n", VALUE); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "1" {
		t.Fatalf("program printed %q, want 1", got)
	}

	// The build file never names value.h. Rebuilding after it changes is only
	// possible because the compiler reported it and the build log kept it.
	if err := os.WriteFile(filepath.Join(src, "value.h"), []byte("#define VALUE 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "2" {
		t.Errorf("after editing the header the program printed %q, want 2", got)
	}
}

func TestEndToEndConfigureOnly(t *testing.T) {
	// Stopping after configure is a supported use and needs no compiler.
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Inspect VERSION 1.2.3 LANGUAGES NONE)
add_library(one INTERFACE)
add_library(two STATIC a.c)
add_executable(three b.c)
target_link_libraries(three PRIVATE two one)
`,
		"a.c": "", "b.c": "",
	})
	c, err := cmake.New(cmake.Config{
		Source: src,
		Binary: filepath.Join(src, "_build"),
		FS:     cmake.RealFS(""),
		Runner: cmake.RealRunner(),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Configure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := res.State.GetVar("PROJECT_VERSION"); got != "1.2.3" {
		t.Errorf("PROJECT_VERSION = %q", got)
	}
	want := []string{"one", "two", "three"}
	if strings.Join(res.TargetNames, ",") != strings.Join(want, ",") {
		t.Errorf("targets = %v, want %v", res.TargetNames, want)
	}
	if _, ok := res.State.Target("two"); !ok {
		t.Error("target two not found")
	}
}

func TestEndToEndGeneratorExpressions(t *testing.T) {
	requireCompiler(t)
	// Every expression here is one that a modern CMakeLists.txt uses and that a
	// configure-only implementation would leave in the compile line verbatim.
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Genex LANGUAGES C)
add_library(core STATIC core.c)
target_include_directories(core PUBLIC
  $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/inc>
  $<INSTALL_INTERFACE:include>)
target_compile_definitions(core PUBLIC
  $<$<BOOL:TRUE>:ENABLED=1>
  $<$<BOOL:FALSE>:DISABLED=1>
  $<IF:$<STREQUAL:a,a>,PICKED=1,NOT_PICKED=1>
  NAMED=$<UPPER_CASE:yes>)
add_executable(app app.c)
target_link_libraries(app PRIVATE core)
`,
		"inc/core.h": "int core_value(void);\n",
		"core.c":     "#include \"core.h\"\nint core_value(void) { return 7; }\n",
		"app.c": `#include <stdio.h>
#include "core.h"
#ifndef ENABLED
#error ENABLED should be defined
#endif
#ifdef DISABLED
#error DISABLED should not be defined
#endif
#ifndef PICKED
#error PICKED should be defined
#endif
#if NAMED != YES
#define STR2(x) #x
#define STR(x) STR2(x)
#pragma message("NAMED is " STR(NAMED))
#endif
int main(void) { printf("%d\n", core_value()); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "7" {
		t.Errorf("program printed %q, want 7", got)
	}
}

func TestEndToEndObjectLibrary(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Objects LANGUAGES C)
add_library(parts OBJECT one.c two.c)
add_executable(app app.c)
target_link_libraries(app PRIVATE parts)
`,
		"one.c": "int one(void) { return 1; }\n",
		"two.c": "int two(void) { return 2; }\n",
		"app.c": `#include <stdio.h>
int one(void); int two(void);
int main(void) { printf("%d\n", one() + two()); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "3" {
		t.Errorf("program printed %q, want 3", got)
	}
}

func TestEndToEndCustomCommand(t *testing.T) {
	requireCompiler(t)
	src := project(t, map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Generated LANGUAGES C)
add_custom_command(
  OUTPUT generated.c
  COMMAND ${CMAKE_COMMAND} -E copy ${CMAKE_CURRENT_SOURCE_DIR}/template.c ${CMAKE_CURRENT_BINARY_DIR}/generated.c
  DEPENDS ${CMAKE_CURRENT_SOURCE_DIR}/template.c
  COMMENT "Generating generated.c")
add_executable(app ${CMAKE_CURRENT_BINARY_DIR}/generated.c)
`,
		"template.c": `#include <stdio.h>
int main(void) { printf("generated\n"); return 0; }
`,
	})
	binary, _ := buildProject(t, src)
	if got := runExe(t, filepath.Join(binary, exeName("app"))); got != "generated" {
		t.Errorf("program printed %q", got)
	}
}
