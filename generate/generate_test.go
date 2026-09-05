package generate_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/generate"
	"github.com/vertex-language/go-cmake/toolchain"
)

// configure evaluates a CMakeLists.txt written to a temporary directory and
// returns the resulting state, so a generator test can start from real
// configure output rather than a hand-built State that might not be reachable.
func configure(t *testing.T, source string) *eval.State {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	// Every source a test names is created empty, so that path resolution and
	// language detection see real files.
	for _, name := range []string{"a.c", "b.c", "c.c", "main.c", "one.cpp"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0644)
	}
	slash := filepath.ToSlash(dir)
	state := eval.NewState(slash, slash+"/build", os.Environ())
	state.LogSink = func(string, string) {}
	if err := eval.EvalProject(context.Background(), state, diskFS{}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return state
}

type diskFS struct{}

func (diskFS) ReadFile(name string) ([]byte, error)  { return os.ReadFile(name) }
func (diskFS) WriteFile(n string, d []byte) error    { return os.WriteFile(n, d, 0644) }
func (diskFS) MkdirAll(name string) error            { return os.MkdirAll(name, 0755) }
func (diskFS) Glob(p string) ([]string, error)       { return filepath.Glob(p) }
func (diskFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (diskFS) Remove(name string) error              { return os.RemoveAll(name) }

var _ = time.Now

func resolve(t *testing.T, source string) *generate.Graph {
	t.Helper()
	g, err := generate.Resolve(configure(t, source))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return g
}

func TestUsageRequirementsPropagate(t *testing.T) {
	g := resolve(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(base STATIC a.c)
target_include_directories(base PUBLIC pub PRIVATE priv)
target_compile_definitions(base PUBLIC PUB=1 PRIVATE PRIV=1)
add_library(mid STATIC b.c)
target_link_libraries(mid PUBLIC base)
add_executable(app main.c)
target_link_libraries(app PRIVATE mid)
`)

	app := g.Targets["app"]
	if !hasSuffix(app.IncludeDirs, "pub") {
		t.Errorf("app include dirs %v should contain the transitively public one", app.IncludeDirs)
	}
	if hasSuffix(app.IncludeDirs, "priv") {
		t.Errorf("app include dirs %v must not contain base's private one", app.IncludeDirs)
	}
	if !contains(app.Defines, "PUB=1") {
		t.Errorf("app defines %v should inherit PUB=1", app.Defines)
	}
	if contains(app.Defines, "PRIV=1") {
		t.Errorf("app defines %v must not inherit base's private definition", app.Defines)
	}

	// base's own compilation sees both.
	base := g.Targets["base"]
	if !contains(base.Defines, "PUB=1") || !contains(base.Defines, "PRIV=1") {
		t.Errorf("base defines = %v, want both", base.Defines)
	}
}

func TestLinkOrderPutsDependenciesLast(t *testing.T) {
	g := resolve(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(low STATIC a.c)
add_library(high STATIC b.c)
target_link_libraries(high PUBLIC low)
add_executable(app main.c)
target_link_libraries(app PRIVATE high)
`)
	libs := g.Targets["app"].LinkLibs
	hi, lo := indexOf(libs, "high"), indexOf(libs, "low")
	if hi < 0 || lo < 0 {
		t.Fatalf("link libs = %v, want both high and low", libs)
	}
	// A single-pass linker resolves a symbol only against archives that come
	// after the object needing it, so the dependency must be later in the list.
	if hi > lo {
		t.Errorf("link order %v puts the dependency before its dependent", libs)
	}
}

func TestInterfaceLibraryContributesRequirementsButNotAFile(t *testing.T) {
	g := resolve(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(headers INTERFACE)
target_include_directories(headers INTERFACE inc)
target_compile_definitions(headers INTERFACE HEADER_ONLY=1)
add_executable(app main.c)
target_link_libraries(app PRIVATE headers)
`)
	app := g.Targets["app"]
	if !contains(app.Defines, "HEADER_ONLY=1") {
		t.Errorf("defines = %v", app.Defines)
	}
	if !hasSuffix(app.IncludeDirs, "inc") {
		t.Errorf("include dirs = %v", app.IncludeDirs)
	}
	if contains(app.LinkLibs, "headers") {
		t.Errorf("an interface library must not appear on the link line: %v", app.LinkLibs)
	}
}

func TestDependencyCycleIsReported(t *testing.T) {
	state := configure(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(a SHARED a.c)
add_library(b SHARED b.c)
target_link_libraries(a PUBLIC b)
target_link_libraries(b PUBLIC a)
add_executable(app main.c)
target_link_libraries(app PRIVATE a)
`)
	if _, err := generate.Resolve(state); err == nil {
		t.Error("a cycle through shared libraries must be reported")
	}
}

func TestStaticLibraryCycleIsAllowed(t *testing.T) {
	// Mutually-recursive static libraries are legal: the archives are simply
	// given to the linker more than once.
	state := configure(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(a STATIC a.c)
add_library(b STATIC b.c)
target_link_libraries(a PUBLIC b)
target_link_libraries(b PUBLIC a)
`)
	if _, err := generate.Resolve(state); err != nil {
		t.Errorf("a static library cycle should be permitted: %v", err)
	}
}

func TestNinjaFileContents(t *testing.T) {
	state := configure(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(lib STATIC a.c b.c)
add_executable(app main.c)
target_link_libraries(app PRIVATE lib)
`)
	g, err := generate.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	tc := toolchain.Detect([]string{"C"}, toolchain.OSEnv())
	if _, ok := tc.Compiler("C"); !ok {
		t.Skip("no C compiler; the generated file would name no rules")
	}
	n := &generate.Ninja{
		Graph: g, Toolchain: tc,
		SourceDir: state.SourceDir, BinaryDir: state.BinaryDir,
	}
	var b strings.Builder
	if _, err := n.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"rule compile_C",
		"rule archive",
		"rule link_exe",
		"build all: phony",
		"default all",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file is missing %q\n%s", want, out)
		}
	}
	// The executable must depend on the library, or a parallel build could link
	// before the archive exists.
	if !strings.Contains(out, "|") {
		t.Error("no implicit dependency edge was emitted")
	}
}

func TestGeneratorExpressionsResolve(t *testing.T) {
	state := configure(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(lib STATIC a.c)
target_include_directories(lib PUBLIC
  $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/inc>
  $<INSTALL_INTERFACE:include>)
target_compile_definitions(lib PUBLIC
  $<$<BOOL:TRUE>:KEPT=1>
  $<$<BOOL:FALSE>:DROPPED=1>
  $<IF:$<STREQUAL:x,x>,CHOSEN=1,OTHER=1>
  UPPER=$<UPPER_CASE:value>)
add_executable(app main.c)
target_link_libraries(app PRIVATE lib)
`)
	g, err := generate.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	tc := toolchain.Detect([]string{"C"}, toolchain.OSEnv())
	if _, ok := tc.Compiler("C"); !ok {
		t.Skip("no C compiler")
	}
	n := &generate.Ninja{
		Graph: g, Toolchain: tc,
		SourceDir: state.SourceDir, BinaryDir: state.BinaryDir,
	}
	var b strings.Builder
	if _, err := n.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()

	if strings.Contains(out, "$<") {
		t.Errorf("an unresolved generator expression reached the build file:\n%s", out)
	}
	for _, want := range []string{"KEPT=1", "CHOSEN=1", "UPPER=VALUE", "/inc"} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file is missing %q", want)
		}
	}
	for _, unwanted := range []string{"DROPPED=1", "OTHER=1", "INSTALL_INTERFACE"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("generated file contains %q, which should have been dropped", unwanted)
		}
	}
}

func TestTargetFileExpression(t *testing.T) {
	state := configure(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_library(lib STATIC a.c)
add_executable(app main.c)
target_compile_definitions(app PRIVATE LIBPATH="$<TARGET_FILE:lib>")
target_link_libraries(app PRIVATE lib)
`)
	g, err := generate.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	tc := toolchain.Detect([]string{"C"}, toolchain.OSEnv())
	if _, ok := tc.Compiler("C"); !ok {
		t.Skip("no C compiler")
	}
	n := &generate.Ninja{
		Graph: g, Toolchain: tc,
		SourceDir: state.SourceDir, BinaryDir: state.BinaryDir,
	}
	var b strings.Builder
	if _, err := n.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "lib"+tc.StaticSuffix) {
		t.Errorf("$<TARGET_FILE:lib> did not resolve to the library's path:\n%s", b.String())
	}
}

func TestUnknownGeneratorExpressionIsAnError(t *testing.T) {
	state := configure(t, `
cmake_minimum_required(VERSION 3.16)
project(P LANGUAGES C)
add_executable(app main.c)
target_compile_definitions(app PRIVATE $<NO_SUCH_EXPRESSION:x>)
`)
	g, err := generate.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	tc := toolchain.Detect([]string{"C"}, toolchain.OSEnv())
	if _, ok := tc.Compiler("C"); !ok {
		t.Skip("no C compiler")
	}
	n := &generate.Ninja{
		Graph: g, Toolchain: tc,
		SourceDir: state.SourceDir, BinaryDir: state.BinaryDir,
	}
	var b strings.Builder
	if _, err := n.WriteTo(&b); err == nil {
		t.Error("an unknown generator expression must be reported, not passed through")
	}
}

// ---- small helpers ----

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func hasSuffix(list []string, suffix string) bool {
	for _, v := range list {
		if strings.HasSuffix(v, suffix) {
			return true
		}
	}
	return false
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}
