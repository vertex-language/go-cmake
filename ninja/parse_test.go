package ninja

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// memFS serves a fixed set of files, so a parser test needs no disk.
type memFS map[string]string

func (m memFS) ReadFile(name string) ([]byte, error) {
	if v, ok := m[filepath.ToSlash(name)]; ok {
		return []byte(v), nil
	}
	return nil, os.ErrNotExist
}

func (m memFS) Stat(name string) (fs.FileInfo, error) {
	if _, ok := m[filepath.ToSlash(name)]; ok {
		return fakeInfo{name: name}, nil
	}
	return nil, os.ErrNotExist
}

type fakeInfo struct {
	name string
	mod  time.Time
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return 0644 }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

func parseString(t *testing.T, src string) *File {
	t.Helper()
	f, err := Parse(memFS{"build.ninja": src}, "build.ninja")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}

func TestParseVariablesAndRules(t *testing.T) {
	f := parseString(t, `
cc = gcc
flags = -O2

rule compile
  command = $cc $flags -c $in -o $out
  description = CC $out
`)
	if f.Vars["cc"] != "gcc" {
		t.Errorf("cc = %q", f.Vars["cc"])
	}
	r, ok := f.Rules["compile"]
	if !ok {
		t.Fatal("rule compile not parsed")
	}
	// A rule body is stored unexpanded: $in and $out are only knowable per edge.
	if r.Vars["command"] != "$cc $flags -c $in -o $out" {
		t.Errorf("command = %q", r.Vars["command"])
	}
}

func TestParseBuildEdgeSections(t *testing.T) {
	f := parseString(t, `
rule r
  command = do

build out1 out2 | implicit_out : r in1 in2 | implicit1 || order1 order2
  key = value
`)
	if len(f.Edges) != 1 {
		t.Fatalf("got %d edges", len(f.Edges))
	}
	e := f.Edges[0]
	check := func(name string, got, want []string) {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("outputs", e.Outputs, []string{"out1", "out2"})
	check("implicit outputs", e.ImplicitOutputs, []string{"implicit_out"})
	check("inputs", e.Inputs, []string{"in1", "in2"})
	check("implicit", e.Implicit, []string{"implicit1"})
	check("order-only", e.OrderOnly, []string{"order1", "order2"})
	if e.Vars["key"] != "value" {
		t.Errorf("edge var = %q", e.Vars["key"])
	}
}

func TestParseEscapedPaths(t *testing.T) {
	// A path with a space is escaped as "$ ", and one with a colon as "$:".
	// Splitting on raw spaces or colons would tear these apart, which is the
	// single most common way a hand-written ninja parser goes wrong.
	f := parseString(t, `
rule r
  command = do

build C$:/Program$ Files/out.o: r C$:/Program$ Files/in.c
`)
	e := f.Edges[0]
	if len(e.Outputs) != 1 || e.Outputs[0] != "C:/Program Files/out.o" {
		t.Errorf("outputs = %q", e.Outputs)
	}
	if len(e.Inputs) != 1 || e.Inputs[0] != "C:/Program Files/in.c" {
		t.Errorf("inputs = %q", e.Inputs)
	}
}

func TestParseLineContinuation(t *testing.T) {
	f := parseString(t, `
rule r
  command = do

build out: r $
    a $
    b
`)
	e := f.Edges[0]
	if !reflect.DeepEqual(e.Inputs, []string{"a", "b"}) {
		t.Errorf("inputs = %v", e.Inputs)
	}
}

func TestParseDefaultsAndPools(t *testing.T) {
	f := parseString(t, `
pool heavy
  depth = 2

rule r
  command = do

build a: r
build b: r
default a b
`)
	if f.Pools["heavy"] != 2 {
		t.Errorf("pool depth = %d", f.Pools["heavy"])
	}
	if !reflect.DeepEqual(f.Defaults, []string{"a", "b"}) {
		t.Errorf("defaults = %v", f.Defaults)
	}
}

func TestParseInclude(t *testing.T) {
	files := memFS{
		"build.ninja": "include rules.ninja\nbuild out: r in\n",
		"rules.ninja": "rule r\n  command = do\n",
	}
	f, err := Parse(files, "build.ninja")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Rules["r"]; !ok {
		t.Error("included rule not visible")
	}
	if len(f.Edges) != 1 {
		t.Errorf("got %d edges", len(f.Edges))
	}
}

func TestExpandVars(t *testing.T) {
	vars := map[string]string{"a": "A", "long_name": "L"}
	lookup := func(n string) (string, bool) { v, ok := vars[n]; return v, ok }
	cases := []struct{ in, want string }{
		{"$a", "A"},
		{"${long_name}", "L"},
		{"x${a}y", "xAy"},
		{"$$a", "$a"},
		{"$ ", " "},
		{"$missing", ""},
		{"no vars here", "no vars here"},
	}
	for _, c := range cases {
		if got := expandVars(c.in, lookup); got != c.want {
			t.Errorf("expandVars(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShowIncludesParsing(t *testing.T) {
	out := "main.c\r\n" +
		"Note: including file: C:\\project\\value.h\r\n" +
		"Note: including file:  C:\\sdk\\stdio.h\r\n" +
		"main.c(3): warning C4101: unreferenced variable\r\n"
	deps := parseShowIncludes(out)
	want := []string{`C:\project\value.h`, `C:\sdk\stdio.h`}
	if !reflect.DeepEqual(deps, want) {
		t.Errorf("deps = %q, want %q", deps, want)
	}
	filtered := filterShowIncludes(out)
	if len(filtered) == 0 || filterContains(filtered, "including file") {
		t.Errorf("filtered output still holds include notes: %q", filtered)
	}
	if !filterContains(filtered, "warning C4101") {
		t.Errorf("filtering removed a real diagnostic: %q", filtered)
	}
}

// TestShowIncludesLocalised checks the case that motivates parsing by shape
// rather than by the English text: a non-English compiler still reports
// dependencies, and losing them would produce a build that never rebuilds.
func TestShowIncludesLocalised(t *testing.T) {
	out := "Hinweis: Einlesen der Datei: C:\\project\\value.h\r\n"
	deps := parseShowIncludes(out)
	if len(deps) != 1 || deps[0] != `C:\project\value.h` {
		t.Errorf("deps = %q", deps)
	}
}

func filterContains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestParseDepfile(t *testing.T) {
	data := []byte("out.o: a.c \\\n  b.h \\\n  c.h\n")
	target, deps, err := ParseDepfile(data)
	if err != nil {
		t.Fatal(err)
	}
	if target != "out.o" {
		t.Errorf("target = %q", target)
	}
	if !reflect.DeepEqual(deps, []string{"a.c", "b.h", "c.h"}) {
		t.Errorf("deps = %v", deps)
	}
}

// TestDepsLogRoundTripWithSpaces is the regression test for a bug that made
// every incremental build recompile everything while appearing to work: the
// dependency log joined paths with spaces, so every header under
// "C:/Program Files" came back as three paths that do not exist, and every
// object therefore looked stale.
func TestDepsLogRoundTripWithSpaces(t *testing.T) {
	original := NewDepsLog()
	deps := []string{
		`C:/Program Files (x86)/Windows Kits/10/Include/10.0.26100.0/ucrt/stdio.h`,
		`C:/my project/value.h`,
		`/usr/include/stdio.h`,
	}
	original.Add("C:/build/main.c.obj", deps)
	original.Add("C:/build/other.c.obj", nil)

	var buf strings.Builder
	if err := original.Write(&buf); err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadDepsLog(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	got := parsed.Get("C:/build/main.c.obj")
	if !reflect.DeepEqual(got, deps) {
		t.Errorf("round trip lost path structure:\n got %q\nwant %q", got, deps)
	}
	if _, ok := parsed.Deps["C:/build/other.c.obj"]; !ok {
		t.Error("an output with no dependencies was not preserved")
	}
}
