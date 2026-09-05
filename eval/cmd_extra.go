package eval

import (
	"context"
	"sort"
	"strings"
)

func init() {
	register("aux_source_directory", cmdAuxSourceDirectory)
	register("source_group", cmdNoOp)
	register("get_test_property", cmdGetTestProperty)
	register("write_file", cmdWriteFile)
	register("make_directory", cmdMakeDirectory)
}

// cmdAuxSourceDirectory collects the compilable sources in a directory.
//
// CMake documents this command and then advises against it, for a reason worth
// repeating: the result is captured when configure runs, so a source file added
// afterwards is not built until someone re-runs configure, and the failure looks
// like a linker error rather than a missing file.
func cmdAuxSourceDirectory(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("aux_source_directory called with incorrect number of arguments")
	}
	dir := e.state.absPath(v[0])
	matches, err := e.fs.Glob(dir + "/*")
	if err != nil {
		return e.fatalf("aux_source_directory could not read %s: %v", v[0], err)
	}
	var sources []string
	for _, m := range matches {
		if sourceExtensions[strings.ToLower(extensionOf(m))] {
			sources = append(sources, slashPath(m))
		}
	}
	sort.Strings(sources)
	existing := SplitList(e.state.GetVar(v[1]))
	e.state.SetVar(v[1], JoinList(append(existing, sources...)))
	return nil
}

// sourceExtensions are the ones CMake treats as compilable sources.
var sourceExtensions = map[string]bool{
	".c": true, ".cc": true, ".cpp": true, ".cxx": true, ".c++": true,
	".m": true, ".mm": true, ".f": true, ".f90": true, ".cu": true,
}

func extensionOf(p string) string {
	base := BaseName(p)
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		return base[i:]
	}
	return ""
}

func cmdGetTestProperty(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 3 {
		return e.fatalf("get_test_property called with incorrect number of arguments")
	}
	test, prop, out := v[0], v[1], v[2]
	value, ok := e.state.Properties[propertyKey("TEST", test, prop)]
	if !ok {
		e.state.SetVar(out, "NOTFOUND")
		return nil
	}
	e.state.SetVar(out, value)
	return nil
}

// cmdWriteFile is the CMake 2.x spelling of file(WRITE). It is implemented
// because old projects still call it, and failing on it would stop a configure
// that CMake itself completes.
func cmdWriteFile(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 1 {
		return e.fatalf("write_file called with incorrect number of arguments")
	}
	appendMode := len(v) > 2 && v[len(v)-1] == "APPEND"
	content := strings.Join(v[1:], "")
	if appendMode {
		content = strings.Join(v[1:len(v)-1], "")
	}
	target := e.state.absPath(v[0])
	if err := e.fs.MkdirAll(dirOf(target)); err != nil {
		return e.fatalf("write_file could not create a directory: %v", err)
	}
	if appendMode {
		if old, err := e.fs.ReadFile(target); err == nil {
			content = string(old) + content
		}
	}
	if err := e.fs.WriteFile(target, []byte(content+"\n")); err != nil {
		return e.fatalf("write_file failed on %s: %v", v[0], err)
	}
	return nil
}

// cmdMakeDirectory is the CMake 2.x spelling of file(MAKE_DIRECTORY).
func cmdMakeDirectory(_ context.Context, e *evaluator, args []Arg) error {
	for _, d := range Args(args) {
		if err := e.fs.MkdirAll(e.state.absPath(d)); err != nil {
			return e.fatalf("make_directory failed on %s: %v", d, err)
		}
	}
	return nil
}
