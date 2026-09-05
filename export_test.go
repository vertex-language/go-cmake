package cmake_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A library that builds is not yet a library anyone can depend on. What makes
// it one is the targets file install(EXPORT) and export() write, and the only
// way to know that file is right is to use it: configure a second project that
// knows nothing about the first except that file, link against it, and run the
// result.
//
// That is what these tests do. Reading the generated file and checking it
// contains the right lines would pass just as well if the file were unusable.

// libraryProject writes a project that exports a library both ways.
func libraryProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(Demo VERSION 1.2.3 LANGUAGES C)

add_library(util STATIC src/util.c)
target_include_directories(util PUBLIC
  $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/inc>
  $<INSTALL_INTERFACE:include>)
target_compile_definitions(util PUBLIC UTIL_ANSWER=42)

install(TARGETS util EXPORT DemoTargets
        ARCHIVE DESTINATION lib
        RUNTIME DESTINATION bin
        INCLUDES DESTINATION include)
install(FILES inc/util.h DESTINATION include)
install(EXPORT DemoTargets NAMESPACE Demo:: DESTINATION lib/cmake/Demo)
export(EXPORT DemoTargets NAMESPACE Demo:: FILE DemoTargets.cmake)
`)
	write(t, dir, "inc/util.h", "int util_answer(void);\n")
	write(t, dir, "src/util.c", "int util_answer(void){ return UTIL_ANSWER; }\n")
	return dir
}

// consumerProject writes a project that knows only the targets file.
func consumerProject(t *testing.T, targetsFile string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "CMakeLists.txt", `
cmake_minimum_required(VERSION 3.16)
project(App LANGUAGES C)
include("`+filepath.ToSlash(targetsFile)+`")
add_executable(app main.c)
target_link_libraries(app PRIVATE Demo::util)
`)
	write(t, dir, "main.c", `
#include <stdio.h>
#include "util.h"
int main(void){ printf("answer=%d\n", util_answer()); return 0; }
`)
	return dir
}

// TestExportBuildTreeIsUsable covers export(): a second project points at the
// first project's build directory and uses it without an install step. This is
// how a superbuild works, and how anyone developing two projects together
// works.
func TestExportBuildTreeIsUsable(t *testing.T) {
	lib := libraryProject(t)
	libBuild := filepath.Join(lib, "b")
	if code, out, errOut := runCLI(t, lib, "-S", lib, "-B", libBuild); code != 0 {
		t.Fatalf("configuring the library failed:\n%s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, lib, "--build", libBuild); code != 0 {
		t.Fatalf("building the library failed:\n%s%s", out, errOut)
	}

	targets := filepath.Join(libBuild, "DemoTargets.cmake")
	if _, err := os.Stat(targets); err != nil {
		t.Fatalf("export() wrote no targets file: %v", err)
	}

	app := consumerProject(t, targets)
	appBuild := filepath.Join(app, "b")
	if code, out, errOut := runCLI(t, app, "-S", app, "-B", appBuild); code != 0 {
		t.Fatalf("configuring the consumer failed:\n%s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, app, "--build", appBuild); code != 0 {
		t.Fatalf("building the consumer failed:\n%s%s", out, errOut)
	}
	// 42 comes from the definition the exported target carries, and the header
	// comes from the include directory it carries: a program that prints it has
	// inherited both.
	if got := runProgram(t, filepath.Join(appBuild, "app"+exeSuffix())); !strings.Contains(got, "answer=42") {
		t.Errorf("the program printed %q", got)
	}
}

// TestExportInstallTreeIsUsable covers install(EXPORT), which is the one that
// matters: it is what a package manager unpacks and what find_package finds.
// The consumer here sees only the install prefix -- the build tree is gone as
// far as it is concerned.
func TestExportInstallTreeIsUsable(t *testing.T) {
	lib := libraryProject(t)
	libBuild := filepath.Join(lib, "b")
	prefix := filepath.Join(t.TempDir(), "prefix")
	if code, out, errOut := runCLI(t, lib, "-S", lib, "-B", libBuild,
		"-DCMAKE_INSTALL_PREFIX="+filepath.ToSlash(prefix)); code != 0 {
		t.Fatalf("configuring the library failed:\n%s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, lib, "--build", libBuild); code != 0 {
		t.Fatalf("building the library failed:\n%s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, lib, "--install", libBuild); code != 0 {
		t.Fatalf("installing failed:\n%s%s", out, errOut)
	}

	targets := filepath.Join(prefix, "lib", "cmake", "Demo", "DemoTargets.cmake")
	if _, err := os.Stat(targets); err != nil {
		t.Fatalf("install(EXPORT) installed no targets file: %v", err)
	}

	app := consumerProject(t, targets)
	appBuild := filepath.Join(app, "b")
	if code, out, errOut := runCLI(t, app, "-S", app, "-B", appBuild); code != 0 {
		t.Fatalf("configuring the consumer failed:\n%s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, app, "--build", appBuild); code != 0 {
		t.Fatalf("building the consumer failed:\n%s%s", out, errOut)
	}
	if got := runProgram(t, filepath.Join(appBuild, "app"+exeSuffix())); !strings.Contains(got, "answer=42") {
		t.Errorf("the program printed %q", got)
	}
}

// TestExportedFileIsRelocatable is the property that separates an installed
// package from a build directory: nothing inside may name where the package was
// built or first installed, because it will be unpacked somewhere else.
func TestExportedFileIsRelocatable(t *testing.T) {
	lib := libraryProject(t)
	libBuild := filepath.Join(lib, "b")
	prefix := filepath.Join(t.TempDir(), "prefix")
	if code, out, errOut := runCLI(t, lib, "-S", lib, "-B", libBuild,
		"-DCMAKE_INSTALL_PREFIX="+filepath.ToSlash(prefix)); code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}
	if code, _, _ := runCLI(t, lib, "--build", libBuild); code != 0 {
		t.Fatal("build failed")
	}
	if code, out, errOut := runCLI(t, lib, "--install", libBuild); code != 0 {
		t.Fatalf("install failed:\n%s%s", out, errOut)
	}

	data, err := os.ReadFile(filepath.Join(prefix, "lib", "cmake", "Demo", "DemoTargets.cmake"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{filepath.ToSlash(libBuild), filepath.ToSlash(lib), filepath.ToSlash(prefix)} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the installed targets file names %s, so moving the package would break it", forbidden)
		}
	}
	if !strings.Contains(text, "_IMPORT_PREFIX") {
		t.Error("the installed targets file does not compute a prefix, so it cannot be relocatable")
	}

	// The build-tree file is the opposite: it names the build tree, because
	// that is where the library it describes actually is.
	build, err := os.ReadFile(filepath.Join(libBuild, "DemoTargets.cmake"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), filepath.ToSlash(libBuild)) {
		t.Error("the build-tree targets file does not name the build tree")
	}
	if !strings.Contains(string(build), filepath.ToSlash(lib)+"/inc") {
		t.Error("the build-tree targets file lost the BUILD_INTERFACE include directory")
	}
}

// TestExportedInterfacesPickTheRightSide covers the pair of expressions that
// exist for exactly this: $<BUILD_INTERFACE:> belongs to the build tree file
// and $<INSTALL_INTERFACE:> to the installed one, and putting either in the
// wrong file gives a consumer a directory that does not exist.
func TestExportedInterfacesPickTheRightSide(t *testing.T) {
	lib := libraryProject(t)
	libBuild := filepath.Join(lib, "b")
	prefix := filepath.Join(t.TempDir(), "prefix")
	if code, out, errOut := runCLI(t, lib, "-S", lib, "-B", libBuild,
		"-DCMAKE_INSTALL_PREFIX="+filepath.ToSlash(prefix)); code != 0 {
		t.Fatalf("configure failed:\n%s%s", out, errOut)
	}

	build := readFile(t, filepath.Join(libBuild, "DemoTargets.cmake"))
	staged := readFile(t, filepath.Join(libBuild, "CMakeFiles", "Export", "DemoTargets", "DemoTargets.cmake"))

	if !strings.Contains(build, filepath.ToSlash(lib)+"/inc") {
		t.Error("the build-tree file lost its BUILD_INTERFACE directory")
	}
	if strings.Contains(build, "${_IMPORT_PREFIX}/include") {
		t.Error("the build-tree file took the INSTALL_INTERFACE side")
	}
	if !strings.Contains(staged, "${_IMPORT_PREFIX}/include") {
		t.Error("the install-tree file lost its INSTALL_INTERFACE directory")
	}
	if strings.Contains(staged, filepath.ToSlash(lib)+"/inc") {
		t.Error("the install-tree file took the BUILD_INTERFACE side")
	}
	// The definition is neither, so both files carry it.
	for name, text := range map[string]string{"build-tree": build, "install-tree": staged} {
		if !strings.Contains(text, "UTIL_ANSWER=42") {
			t.Errorf("the %s file lost the interface definition", name)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestExportInteropsWithRealCMake is the test that matters most for an export:
// the file exists so that some other tool can read it, and the other tool is
// usually the real cmake. A file only this program can read would satisfy every
// test above and still be useless.
//
// Both directions are checked. Real cmake configures against a package this
// program built and installed, and this program configures against one real
// cmake built and installed -- which is the harder half, because cmake's file
// states locations per configuration and globs in a second file to find them.
func TestExportInteropsWithRealCMake(t *testing.T) {
	realCMakeBinary(t)

	t.Run("real cmake consumes ours", func(t *testing.T) {
		lib := libraryProject(t)
		prefix := filepath.Join(t.TempDir(), "prefix")
		installLibrary(t, lib, prefix, false)
		app := consumerProject(t, filepath.Join(prefix, "lib", "cmake", "Demo", "DemoTargets.cmake"))
		out := configureBuildRunWithRealCMake(t, app)
		if !strings.Contains(out, "answer=42") {
			t.Errorf("real cmake could not use the package this program installed:\n%s", out)
		}
	})

	t.Run("we consume real cmake's", func(t *testing.T) {
		lib := libraryProject(t)
		prefix := filepath.Join(t.TempDir(), "prefix")
		installLibrary(t, lib, prefix, true)
		app := consumerProject(t, filepath.Join(prefix, "lib", "cmake", "Demo", "DemoTargets.cmake"))
		build := filepath.Join(app, "b")
		if code, out, errOut := runCLI(t, app, "-S", app, "-B", build); code != 0 {
			t.Fatalf("configure failed:\n%s%s", out, errOut)
		}
		if code, out, errOut := runCLI(t, app, "--build", build); code != 0 {
			t.Fatalf("build failed:\n%s%s", out, errOut)
		}
		if got := runProgram(t, filepath.Join(build, "app"+exeSuffix())); !strings.Contains(got, "answer=42") {
			t.Errorf("the program printed %q", got)
		}
	})
}

// realCMakeBinary locates the reference implementation, skipping without one.
func realCMakeBinary(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("cmake")
	if err != nil {
		t.Skip("no cmake on PATH")
	}
	return p
}

// installLibrary puts the library under prefix, using whichever implementation
// was asked for.
func installLibrary(t *testing.T, source, prefix string, useReal bool) {
	t.Helper()
	build := filepath.Join(source, "b")
	if !useReal {
		if code, out, errOut := runCLI(t, source, "-S", source, "-B", build,
			"-DCMAKE_INSTALL_PREFIX="+filepath.ToSlash(prefix)); code != 0 {
			t.Fatalf("configure failed:\n%s%s", out, errOut)
		}
		if code, out, errOut := runCLI(t, source, "--build", build); code != 0 {
			t.Fatalf("build failed:\n%s%s", out, errOut)
		}
		if code, out, errOut := runCLI(t, source, "--install", build); code != 0 {
			t.Fatalf("install failed:\n%s%s", out, errOut)
		}
		return
	}
	binary := realCMakeBinary(t)
	for _, argv := range [][]string{
		{"-S", source, "-B", build, "-DCMAKE_INSTALL_PREFIX=" + filepath.ToSlash(prefix)},
		{"--build", build, "--config", "Release"},
		{"--install", build, "--config", "Release"},
	} {
		cmd := exec.Command(binary, argv...)
		cmd.Dir = source
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("real cmake %v failed: %v\n%s", argv, err, out)
		}
	}
}

// configureBuildRunWithRealCMake drives the reference implementation over a
// project and runs what it produced.
func configureBuildRunWithRealCMake(t *testing.T, source string) string {
	t.Helper()
	binary := realCMakeBinary(t)
	build := filepath.Join(source, "br")
	for _, argv := range [][]string{
		{"-S", source, "-B", build},
		{"--build", build, "--config", "Debug"},
	} {
		cmd := exec.Command(binary, argv...)
		cmd.Dir = source
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("real cmake %v failed: %v\n%s", argv, err, out)
		}
	}
	// The generator real cmake picks decides where the program lands, so it is
	// found rather than predicted.
	var found string
	_ = filepath.Walk(build, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "app"+exeSuffix() {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("real cmake built no program")
	}
	return runProgram(t, found)
}
