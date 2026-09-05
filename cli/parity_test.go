package cli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// A developer's workflow breaks on the options a cmake does not have, not on
// the features it lacks. A script that passes `-Wno-dev` to quieten a warning
// dies on "unknown option" long before anything it cares about is reached, and
// the error names the wrong problem.
//
// So these tests hold the command line to the one cmake actually publishes:
// every option in `cmake --help` must be either accepted or refused by name,
// and none may be reported as unknown.

var (
	cmakeOnce sync.Once
	cmakePath string
)

func realCMake(t *testing.T) string {
	t.Helper()
	cmakeOnce.Do(func() {
		if p, err := exec.LookPath("cmake"); err == nil {
			cmakePath = p
		}
	})
	if cmakePath == "" {
		t.Skip("no cmake on PATH; skipping parity test")
	}
	return cmakePath
}

// optionPattern finds the option names at the start of a cmake help line.
// Several are listed together on one line -- "-h,-H,--help,-help,-usage,/?" --
// so the whole leading run is captured and split afterwards.
var optionPattern = regexp.MustCompile(`(?m)^  (-[^ =]+(?:,[^ =]+)*)`)

// optionNames splits one help line's leading run into individual options and
// strips the placeholder each carries, so "-S <path-to-source>" yields "-S" and
// "--version[=json-v1]" yields "--version".
func optionNames(run string) []string {
	var out []string
	for _, name := range strings.Split(run, ",") {
		if i := strings.IndexAny(name, "[<="); i > 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		// Slash-prefixed spellings are a Windows convention cmake also accepts;
		// they are not options in the sense being checked here.
		if name == "" || !strings.HasPrefix(name, "-") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// publishedOptions reads the option names out of `cmake --help`.
func publishedOptions(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command(realCMake(t), "--help").Output()
	if err != nil {
		t.Skipf("cannot run cmake --help: %v", err)
	}
	seen := map[string]bool{}
	var names []string
	for _, m := range optionPattern.FindAllStringSubmatch(string(out), -1) {
		for _, name := range optionNames(m[1]) {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	if len(names) < 20 {
		t.Fatalf("only found %d options in cmake --help; the parser is wrong", len(names))
	}
	return names
}

// optionsNeedingValues are the ones that consume the next argument, so a
// probe has to supply one or the missing value is what gets reported.
var optionsNeedingValues = map[string]string{
	"-S": ".", "-B": "b", "-G": "Ninja", "-C": "c.cmake", "-D": "X=1", "-U": "X",
	"-T": "v143", "-A": "x64", "-P": "s.cmake",
	"--toolchain": "t.cmake", "--install-prefix": "/tmp/p", "--preset": "p",
	"--log-level": "STATUS", "--loglevel": "STATUS", "--graphviz": "g.dot",
	"--trace-format": "human", "--trace-source": "f", "--trace-redirect": "f",
	"--profiling-format": "google-trace", "--profiling-output": "f",
	"--debug-find-pkg": "Foo", "--debug-find-var": "FOO",
	"--project-file": "p.cmake", "--presets-file": "p.json",
	"--open": ".", "--resolve-package-references": "on",
	"--help-manual": "cmake-language", "--help-command": "set",
	"--help-module": "FindZLIB", "--help-policy": "CMP0054",
	"--help-property": "SOURCES", "--help-variable": "CMAKE_VERSION",
	"--help-diagnostic": "deprecated",
}

// TestEveryPublishedOptionIsRecognised is the important one. An option cmake
// documents must never come back as unknown: it may be honoured, ignored, or
// refused with a reason, but the answer must show that this cmake has heard of
// it.
func TestEveryPublishedOptionIsRecognised(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "CMakeLists.txt"),
		"cmake_minimum_required(VERSION 3.16)\nproject(P LANGUAGES NONE)\n")
	mustWrite(t, filepath.Join(dir, "c.cmake"), "")
	mustWrite(t, filepath.Join(dir, "s.cmake"), "")
	mustWrite(t, filepath.Join(dir, "t.cmake"), "")

	for _, name := range publishedOptions(t) {
		args := []string{name}
		if v, needs := optionsNeedingValues[name]; needs {
			args = append(args, v)
		}
		args = append(args, "-S", dir, "-B", filepath.Join(dir, "out"), "-N")

		_, _, errOut := run(t, dir, args...)
		if strings.Contains(errOut, "unknown option") {
			t.Errorf("%s is documented by cmake but reported as unknown:\n  %s",
				name, strings.TrimSpace(errOut))
		}
	}
}

// TestBuildModeOptionsAreRecognised does the same for `cmake --build`, whose
// options are a separate list.
func TestBuildModeOptionsAreRecognised(t *testing.T) {
	realCMake(t)
	dir := t.TempDir()

	// Every one of these appears in `cmake --build`'s own usage text.
	cases := [][]string{
		{"--build", dir, "--parallel"},
		{"--build", dir, "--parallel", "2"},
		{"--build", dir, "-j"},
		{"--build", dir, "-j2"},
		{"--build", dir, "--target", "all"},
		{"--build", dir, "-t", "all"},
		{"--build", dir, "--config", "Debug"},
		{"--build", dir, "--clean-first"},
		{"--build", dir, "--verbose"},
		{"--build", dir, "-v"},
		{"--build", dir, "--", "-native"},
	}
	for _, args := range cases {
		_, _, errOut := run(t, dir, args...)
		if strings.Contains(errOut, "unknown --build option") {
			t.Errorf("%v: %s", args, strings.TrimSpace(errOut))
		}
	}
}

// TestToolModeCommandsAreRecognised holds `cmake -E` to the commands cmake
// lists. A generated build rule calls these, so an unrecognised one is a build
// that cannot run rather than a missing convenience.
func TestToolModeCommandsAreRecognised(t *testing.T) {
	out, err := exec.Command(realCMake(t), "-E").CombinedOutput()
	if err != nil {
		// `cmake -E` with no command exits non-zero after printing the list.
		if len(out) == 0 {
			t.Skipf("cannot list cmake -E commands: %v", err)
		}
	}
	published := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9_]+)`).FindAllStringSubmatch(string(out), -1)
	if len(published) < 20 {
		t.Fatalf("only found %d -E commands; the parser is wrong", len(published))
	}

	dir := t.TempDir()
	for _, m := range published {
		name := m[1]
		_, _, errOut := run(t, dir, "-E", name)
		if strings.Contains(errOut, "does not implement") {
			t.Errorf("cmake -E %s is documented but reported as not implemented", name)
		}
	}
}

// TestUnknownOptionIsStillAnError is the other side of the contract: being
// generous about documented options must not turn a typo into silence.
func TestUnknownOptionIsStillAnError(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"--not-a-real-flag", "--fresch", "-Q"} {
		code, _, errOut := run(t, dir, bad)
		if code == 0 {
			t.Errorf("%q was accepted", bad)
		}
		if !strings.Contains(errOut, "unknown option") {
			t.Errorf("%q did not report an unknown option: %s", bad, strings.TrimSpace(errOut))
		}
	}
}

func init() {
	// Keep the linter from flagging os as unused when the helpers move around.
	_ = os.Getenv
}
