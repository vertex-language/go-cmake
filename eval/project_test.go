package eval_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/run"
)

// Project-mode differential tests configure a whole source tree rather than
// running a single script. They use project(... NONE) so that no compiler is
// detected: the point is to compare what the CMake language decided, not what
// toolchain happens to be installed.

// tree writes a set of files under a fresh temporary directory.
func tree(t *testing.T, files map[string]string) string {
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

// configureReal runs the reference implementation over a source tree.
func configureReal(t *testing.T, source string) string {
	t.Helper()
	build := filepath.Join(source, "_real")
	cmd := exec.Command(realCMake(t), "-S", source, "-B", build)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("real cmake exited with %v", err)
	}
	return normaliseProject(string(out), source)
}

// configureOurs runs this implementation over the same tree.
func configureOurs(t *testing.T, source string) string {
	t.Helper()
	state := eval.NewState(filepath.ToSlash(source), filepath.ToSlash(filepath.Join(source, "_ours")), os.Environ())
	state.Runner = run.OS()
	var sb strings.Builder
	state.LogSink = func(mode, text string) {
		switch mode {
		case "":
			sb.WriteString(text + "\n")
		case "STATUS":
			sb.WriteString("-- " + text + "\n")
		case "AUTHOR_WARNING":
			sb.WriteString("CMake Warning (author)\n  " + text + "\n")
		case "ERROR":
			sb.WriteString("CMake Error\n  " + text + "\n")
		default:
			sb.WriteString(mode + ": " + text + "\n")
		}
	}
	if err := eval.EvalProject(context.Background(), state, diskFS{}); err != nil {
		sb.WriteString(err.Error() + "\n")
	}
	return normaliseProject(sb.String(), source)
}

// normaliseProject drops the lines real CMake prints about its own progress and
// rewrites absolute paths so the two runs are comparable.
func normaliseProject(s, source string) string {
	slashSource := filepath.ToSlash(source)
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		line = strings.ReplaceAll(line, filepath.ToSlash(filepath.Join(source, "_real")), "<BUILD>")
		line = strings.ReplaceAll(line, filepath.ToSlash(filepath.Join(source, "_ours")), "<BUILD>")
		line = strings.ReplaceAll(line, slashSource, "<SOURCE>")
		line = strings.ReplaceAll(line, source, "<SOURCE>")
		switch {
		case line == "":
		case strings.HasPrefix(line, "-- Building for:"),
			strings.HasPrefix(line, "-- Selecting Windows SDK"),
			strings.HasPrefix(line, "-- Configuring incomplete"),
			strings.HasPrefix(line, "-- Configuring done"),
			strings.HasPrefix(line, "-- Generating done"),
			strings.HasPrefix(line, "-- Build files have been written"),
			strings.HasPrefix(line, "-- The C"), strings.HasPrefix(line, "-- The CXX"),
			strings.HasPrefix(line, "-- Detecting"), strings.HasPrefix(line, "-- Check"),
			strings.HasPrefix(line, "CMake Warning (dev)"),
			strings.HasPrefix(line, "This warning is for project developers"),
			strings.HasPrefix(line, "Call Stack"),
			strings.HasPrefix(line, "  CMakeLists.txt"):
		case strings.HasPrefix(line, "CMake Error at ") || strings.HasPrefix(line, "CMake Error:"):
			out = append(out, "CMake Error")
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// checkProject configures one tree with both implementations and compares.
func checkProject(t *testing.T, name string, files map[string]string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		dir := tree(t, files)
		want := configureReal(t, dir)
		got := configureOurs(t, dir)
		if got != want {
			t.Errorf("output mismatch\n--- real cmake ---\n%s\n--- go-cmake ---\n%s", want, got)
		}
	})
}

func TestProjectBasics(t *testing.T) {
	checkProject(t, "project-variables", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo VERSION 2.5.1.7 DESCRIPTION "a demo" HOMEPAGE_URL "https://example.invalid" LANGUAGES NONE)
message(STATUS "name=${PROJECT_NAME} cmake-name=${CMAKE_PROJECT_NAME}")
message(STATUS "version=${PROJECT_VERSION} major=${PROJECT_VERSION_MAJOR} minor=${PROJECT_VERSION_MINOR} patch=${PROJECT_VERSION_PATCH} tweak=${PROJECT_VERSION_TWEAK}")
message(STATUS "desc=${PROJECT_DESCRIPTION} url=${PROJECT_HOMEPAGE_URL}")
message(STATUS "named=${Demo_VERSION} src=${Demo_SOURCE_DIR}")
message(STATUS "source=${CMAKE_SOURCE_DIR} current=${CMAKE_CURRENT_SOURCE_DIR}")
message(STATUS "binary-is-current=${CMAKE_CURRENT_BINARY_DIR}")
`,
	})

	checkProject(t, "list-file-vars", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES NONE)
message(STATUS "file=${CMAKE_CURRENT_LIST_FILE}")
message(STATUS "dir=${CMAKE_CURRENT_LIST_DIR}")
include(sub/mod.cmake)
message(STATUS "after=${CMAKE_CURRENT_LIST_DIR}")
`,
		"sub/mod.cmake": `
message(STATUS "in-module file=${CMAKE_CURRENT_LIST_FILE}")
message(STATUS "in-module dir=${CMAKE_CURRENT_LIST_DIR}")
message(STATUS "in-module current-source=${CMAKE_CURRENT_SOURCE_DIR}")
`,
	})

	checkProject(t, "subdirectory-scoping", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES NONE)
set(FROM_PARENT visible)
add_subdirectory(sub)
message(STATUS "parent sees child var: [${FROM_CHILD}]")
message(STATUS "parent sees propagated: [${PROPAGATED}]")
`,
		"sub/CMakeLists.txt": `
message(STATUS "child source=${CMAKE_CURRENT_SOURCE_DIR}")
message(STATUS "child binary=${CMAKE_CURRENT_BINARY_DIR}")
message(STATUS "child sees parent var: [${FROM_PARENT}]")
set(FROM_CHILD leaked)
set(PROPAGATED yes PARENT_SCOPE)
message(STATUS "child project dir=${PROJECT_SOURCE_DIR}")
`,
	})

	checkProject(t, "nested-subdirectories", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Top LANGUAGES NONE)
add_subdirectory(a)
`,
		"a/CMakeLists.txt": `
message(STATUS "a: ${CMAKE_CURRENT_BINARY_DIR}")
add_subdirectory(b)
`,
		"a/b/CMakeLists.txt": `
message(STATUS "b: ${CMAKE_CURRENT_BINARY_DIR}")
message(STATUS "b top: ${CMAKE_SOURCE_DIR}")
`,
	})

	checkProject(t, "subproject", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Outer VERSION 1.0 LANGUAGES NONE)
add_subdirectory(inner)
message(STATUS "back outside: PROJECT_NAME=${PROJECT_NAME} version=${PROJECT_VERSION}")
message(STATUS "CMAKE_PROJECT_NAME=${CMAKE_PROJECT_NAME}")
`,
		"inner/CMakeLists.txt": `
project(Inner VERSION 9.9 LANGUAGES NONE)
message(STATUS "inside: PROJECT_NAME=${PROJECT_NAME} version=${PROJECT_VERSION}")
message(STATUS "inside: CMAKE_PROJECT_NAME=${CMAKE_PROJECT_NAME}")
message(STATUS "inside: Outer_SOURCE_DIR set=${Outer_SOURCE_DIR}")
`,
	})
}

func TestProjectTargets(t *testing.T) {
	checkProject(t, "target-properties", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
add_library(mylib STATIC a.c b.c)
add_executable(myexe main.c)
target_link_libraries(myexe PRIVATE mylib)
target_include_directories(mylib PUBLIC inc PRIVATE priv)
target_compile_definitions(mylib PUBLIC PUB=1 PRIVATE PRIV=2)

get_target_property(t mylib TYPE)
message(STATUS "type=${t}")
get_target_property(s mylib SOURCES)
message(STATUS "sources=${s}")
get_target_property(i mylib INTERFACE_INCLUDE_DIRECTORIES)
message(STATUS "iface-inc=${i}")
get_target_property(d mylib INTERFACE_COMPILE_DEFINITIONS)
message(STATUS "iface-def=${d}")
get_target_property(cd mylib COMPILE_DEFINITIONS)
message(STATUS "own-def=${cd}")
get_target_property(l myexe LINK_LIBRARIES)
message(STATUS "links=${l}")
get_target_property(missing mylib NO_SUCH_PROPERTY)
message(STATUS "missing=${missing}")
`,
		"a.c": "", "b.c": "", "main.c": "",
	})

	checkProject(t, "set-target-properties", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
add_library(l STATIC a.c)
set_target_properties(l PROPERTIES OUTPUT_NAME custom VERSION 1.2.3 CUSTOM_THING hello)
get_target_property(o l OUTPUT_NAME)
get_target_property(v l VERSION)
get_target_property(c l CUSTOM_THING)
message(STATUS "${o} ${v} ${c}")
set_property(TARGET l PROPERTY COMPILE_DEFINITIONS A)
set_property(TARGET l APPEND PROPERTY COMPILE_DEFINITIONS B)
get_target_property(d l COMPILE_DEFINITIONS)
message(STATUS "defs=${d}")
`,
		"a.c": "",
	})

	checkProject(t, "alias-and-target-predicate", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
add_library(real STATIC a.c)
add_library(Demo::real ALIAS real)
if(TARGET real)
  message(STATUS "real is a target")
endif()
if(TARGET Demo::real)
  message(STATUS "alias is a target")
endif()
if(TARGET nope)
  message(STATUS "nope is a target")
else()
  message(STATUS "nope is not a target")
endif()
get_target_property(s Demo::real SOURCES)
message(STATUS "through alias: ${s}")
`,
		"a.c": "",
	})

	checkProject(t, "duplicate-target", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
add_library(dup STATIC a.c)
add_library(dup STATIC b.c)
message(STATUS "unreachable")
`,
		"a.c": "", "b.c": "",
	})

	checkProject(t, "build-shared-libs", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
set(BUILD_SHARED_LIBS ON)
add_library(defaulted a.c)
get_target_property(t defaulted TYPE)
message(STATUS "type=${t}")
add_library(explicit STATIC a.c)
get_target_property(t2 explicit TYPE)
message(STATUS "type2=${t2}")
`,
		"a.c": "",
	})

	checkProject(t, "interface-library", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
add_library(iface INTERFACE)
target_include_directories(iface INTERFACE inc)
target_compile_definitions(iface INTERFACE IFACE=1)
get_target_property(t iface TYPE)
get_target_property(i iface INTERFACE_INCLUDE_DIRECTORIES)
get_target_property(d iface INTERFACE_COMPILE_DEFINITIONS)
message(STATUS "${t} | ${i} | ${d}")
`,
	})

	checkProject(t, "add-custom-target", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES NONE)
add_custom_target(gen ALL COMMAND ${CMAKE_COMMAND} -E echo generating COMMENT "making things")
if(TARGET gen)
  message(STATUS "gen exists")
endif()
get_target_property(t gen TYPE)
message(STATUS "type=${t}")
`,
	})
}

func TestProjectDirectoryProperties(t *testing.T) {
	checkProject(t, "include-directories-inherited", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
include_directories(top_inc)
add_compile_definitions(TOP_DEF)
add_subdirectory(sub)
add_library(atTop STATIC a.c)
get_target_property(i atTop INCLUDE_DIRECTORIES)
message(STATUS "top target inc=${i}")
`,
		"sub/CMakeLists.txt": `
include_directories(sub_inc)
add_library(inSub STATIC ${CMAKE_CURRENT_SOURCE_DIR}/../a.c)
get_target_property(i inSub INCLUDE_DIRECTORIES)
message(STATUS "sub target inc=${i}")
get_target_property(d inSub COMPILE_DEFINITIONS)
message(STATUS "sub target def=${d}")
`,
		"a.c": "",
	})

	checkProject(t, "directory-properties", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES NONE)
set_directory_properties(PROPERTIES MY_DIR_PROP value)
get_directory_property(v MY_DIR_PROP)
message(STATUS "v=${v}")
set_property(DIRECTORY PROPERTY OTHER one)
set_property(DIRECTORY APPEND PROPERTY OTHER two)
get_property(o DIRECTORY PROPERTY OTHER)
message(STATUS "o=${o}")
`,
	})
}

func TestProjectOptionsAndCache(t *testing.T) {
	checkProject(t, "option-defaults", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES NONE)
option(FEATURE_A "first" ON)
option(FEATURE_B "second")
message(STATUS "A=${FEATURE_A} B=${FEATURE_B}")
if(FEATURE_A)
  message(STATUS "A enabled")
endif()
if(NOT FEATURE_B)
  message(STATUS "B disabled")
endif()
`,
	})

	checkProject(t, "cache-persists-within-run", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES NONE)
set(C1 first CACHE STRING "doc")
add_subdirectory(sub)
message(STATUS "top sees: ${C1} and ${FROM_SUB_CACHE}")
`,
		"sub/CMakeLists.txt": `
message(STATUS "sub sees: ${C1}")
set(FROM_SUB_CACHE subvalue CACHE STRING "doc")
`,
	})
}

func TestProjectFunctionsAcrossFiles(t *testing.T) {
	checkProject(t, "function-defined-in-module", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES NONE)
include(helpers.cmake)
my_helper(one two)
add_subdirectory(sub)
`,
		"helpers.cmake": `
function(my_helper first)
  message(STATUS "helper: first=${first} rest=${ARGN} where=${CMAKE_CURRENT_SOURCE_DIR}")
endfunction()
`,
		"sub/CMakeLists.txt": `
my_helper(three four)
`,
	})

	checkProject(t, "macro-across-scopes", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo LANGUAGES C)
macro(declare name)
  add_library(${name} STATIC a.c)
  message(STATUS "declared ${name}")
endmacro()
declare(one)
declare(two)
if(TARGET one AND TARGET two)
  message(STATUS "both exist")
endif()
`,
		"a.c": "",
	})
}

func TestProjectConfigureFile(t *testing.T) {
	checkProject(t, "header-generation", map[string]string{
		"CMakeLists.txt": `
cmake_minimum_required(VERSION 3.16)
project(Demo VERSION 3.4.5 LANGUAGES NONE)
set(HAVE_THING ON)
configure_file(config.h.in config.h)
file(READ "${CMAKE_CURRENT_BINARY_DIR}/config.h" generated)
message(STATUS "---")
message(STATUS "${generated}")
message(STATUS "---")
`,
		"config.h.in": `#define VERSION "@PROJECT_VERSION@"
#define MAJOR @PROJECT_VERSION_MAJOR@
#cmakedefine HAVE_THING
#cmakedefine MISSING_THING
`,
	})
}
