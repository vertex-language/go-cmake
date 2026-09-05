package cmake_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	cmake "github.com/vertex-language/go-cmake"
	"github.com/vertex-language/go-cmake/cli"
)

// Installing is the third thing a project does, after configuring and building,
// and the first two are worth little without it. These tests run the whole way
// through -- configure, build, install -- and look at the files that land,
// because that is the only claim that matters.

// installProject writes a project with something of every installable kind.
func installProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
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
project(Demo VERSION 1.0 LANGUAGES C)
add_library(util STATIC util.c)
add_executable(app main.c)
target_link_libraries(app PRIVATE util)

install(TARGETS app util)
install(FILES include/util.h DESTINATION include)
install(FILES README.txt DESTINATION share/doc RENAME readme.txt)
install(DIRECTORY data DESTINATION share)
install(FILES nothing-here.txt DESTINATION share OPTIONAL)
install(FILES notes.txt DESTINATION share COMPONENT devel)
`)
	write("util.c", "int answer(void) { return 42; }\n")
	write("include/util.h", "#pragma once\nint answer(void);\n")
	write("main.c", "int answer(void);\nint main(void) { return answer() == 42 ? 0 : 1; }\n")
	write("README.txt", "the readme\n")
	write("notes.txt", "developer notes\n")
	write("data/a.txt", "one\n")
	write("data/sub/b.txt", "two\n")
	return dir
}

// buildAndInstall runs the whole pipeline and returns the install prefix.
func buildAndInstall(t *testing.T, dir string, installArgs ...string) (prefix string, output string) {
	t.Helper()
	build := filepath.Join(dir, "b")
	prefix = filepath.Join(dir, "out")

	if code, out, errOut := runCLI(t, dir, "-S", dir, "-B", build); code != 0 {
		t.Fatalf("configure failed: %s%s", out, errOut)
	}
	if code, out, errOut := runCLI(t, dir, "--build", build); code != 0 {
		t.Fatalf("build failed: %s%s", out, errOut)
	}
	args := append([]string{"--install", build, "--prefix", prefix}, installArgs...)
	code, out, errOut := runCLI(t, dir, args...)
	if code != 0 {
		t.Fatalf("install failed: %s%s", out, errOut)
	}
	return prefix, out + errOut
}

// runCLI drives the command line the way a shell would.
func runCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf strings.Builder
	code := cli.Main(context.Background(), cli.Env{
		Args: args, Dir: dir, Env: os.Environ(), Out: &out, Err: &errBuf,
	})
	return code, out.String(), errBuf.String()
}

// installedFiles lists what ended up under the prefix, in slash form.
func installedFiles(t *testing.T, prefix string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(prefix, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(prefix, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestInstallPlacesEveryKindOfRule(t *testing.T) {
	dir := installProject(t)
	prefix, _ := buildAndInstall(t, dir)

	want := []string{
		"bin/app" + exeSuffix(),
		"include/util.h",
		"lib/util" + staticSuffix(),
		"share/data/a.txt",
		"share/data/sub/b.txt",
		"share/doc/readme.txt",
		"share/notes.txt",
	}
	got := installedFiles(t, prefix)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("installed tree differs\nwant:\n  %s\ngot:\n  %s",
			strings.Join(want, "\n  "), strings.Join(got, "\n  "))
	}
}

// TestInstallIsIdempotent is what keeps an install from restamping every file
// and making everything downstream of it look new.
func TestInstallIsIdempotent(t *testing.T) {
	dir := installProject(t)
	prefix, first := buildAndInstall(t, dir)
	if !strings.Contains(first, "Installing:") {
		t.Fatalf("first install reported nothing installed:\n%s", first)
	}

	code, out, errOut := runCLI(t, dir, "--install", filepath.Join(dir, "b"), "--prefix", prefix)
	if code != 0 {
		t.Fatalf("second install failed: %s%s", out, errOut)
	}
	second := out + errOut
	if strings.Contains(second, "Installing:") {
		t.Errorf("second install rewrote unchanged files:\n%s", second)
	}
	if !strings.Contains(second, "Up-to-date:") {
		t.Errorf("second install said nothing about the files it skipped:\n%s", second)
	}
}

func TestInstallComponentSelectsOneGroup(t *testing.T) {
	dir := installProject(t)
	build := filepath.Join(dir, "b")
	prefix := filepath.Join(dir, "devel-only")

	runCLI(t, dir, "-S", dir, "-B", build)
	runCLI(t, dir, "--build", build)
	code, out, errOut := runCLI(t, dir, "--install", build, "--prefix", prefix, "--component", "devel")
	if code != 0 {
		t.Fatalf("install failed: %s%s", out, errOut)
	}

	got := installedFiles(t, prefix)
	if len(got) != 1 || got[0] != "share/notes.txt" {
		t.Errorf("--component devel installed %v, want only share/notes.txt", got)
	}
}

// TestInstallWritesAManifest covers what a packager reads to find out what an
// install produced.
func TestInstallWritesAManifest(t *testing.T) {
	dir := installProject(t)
	prefix, _ := buildAndInstall(t, dir)

	data, err := os.ReadFile(filepath.Join(dir, "b", "install_manifest.txt"))
	if err != nil {
		t.Fatalf("no manifest was written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(installedFiles(t, prefix)) {
		t.Errorf("manifest lists %d files but %d were installed:\n%s",
			len(lines), len(installedFiles(t, prefix)), data)
	}
	for _, line := range lines {
		if _, err := os.Stat(strings.TrimSpace(line)); err != nil {
			t.Errorf("manifest names a file that is not there: %s", line)
		}
	}
}

// TestInstallWithoutGenerateSaysSo covers the case a user hits by installing a
// directory that was never configured.
func TestInstallWithoutGenerateSaysSo(t *testing.T) {
	dir := t.TempDir()
	code, _, errOut := runCLI(t, dir, "--install", dir)
	if code == 0 {
		t.Fatal("installing an unconfigured directory succeeded")
	}
	if !strings.Contains(errOut, cmake.InstallScriptName) {
		t.Errorf("the error does not name the missing script: %s", errOut)
	}
}

// The installed names depend on the toolchain, so the expectations do too.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func staticSuffix() string {
	if runtime.GOOS == "windows" {
		return ".lib"
	}
	return ".a"
}
