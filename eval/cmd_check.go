package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vertex-language/go-cmake/regex"
)

// The Check modules are how a project asks about its compiler in practice.
// Almost nobody calls try_compile directly; they call check_include_file or
// check_symbol_exists, which write a small program and compile it.
//
// CMake ships these as .cmake modules. This package provides them as commands
// instead, for a reason worth stating: shipping copies of CMake's modules would
// mean shipping a moving target that has to be kept in step with a version of
// CMake this package does not control. Implementing the same question directly
// is both smaller and harder to get subtly wrong.
//
// The include() that a project writes before using one of these is accepted and
// does nothing, because the command it would have defined is already here.

func init() {
	register("check_include_file", cmdCheckIncludeFile)
	register("check_include_file_cxx", cmdCheckIncludeFileCXX)
	register("check_include_files", cmdCheckIncludeFiles)
	register("check_c_source_compiles", cmdCheckSourceCompiles("C"))
	register("check_cxx_source_compiles", cmdCheckSourceCompiles("CXX"))
	register("check_c_source_runs", cmdCheckSourceRuns("C"))
	register("check_cxx_source_runs", cmdCheckSourceRuns("CXX"))
	register("check_function_exists", cmdCheckFunctionExists)
	register("check_symbol_exists", cmdCheckSymbolExists("C"))
	register("check_cxx_symbol_exists", cmdCheckSymbolExists("CXX"))
	register("check_library_exists", cmdCheckLibraryExists)
	register("check_type_size", cmdCheckTypeSize)
	register("check_c_compiler_flag", cmdCheckCompilerFlag("C"))
	register("check_cxx_compiler_flag", cmdCheckCompilerFlag("CXX"))
}

// builtinModules are the CMake modules whose commands this package implements
// directly. An include() of one succeeds and does nothing.
var builtinModules = map[string]bool{
	"CheckIncludeFile":       true,
	"CheckIncludeFileCXX":    true,
	"CheckIncludeFiles":      true,
	"CheckCSourceCompiles":   true,
	"CheckCXXSourceCompiles": true,
	"CheckCSourceRuns":       true,
	"CheckCXXSourceRuns":     true,
	"CheckFunctionExists":    true,
	"CheckSymbolExists":      true,
	"CheckCXXSymbolExists":   true,
	"CheckLibraryExists":     true,
	"CheckTypeSize":          true,
	"CheckCCompilerFlag":     true,
	"CheckCXXCompilerFlag":   true,
	"CMakeParseArguments":    true, // cmake_parse_arguments is a command here
	"FetchContent":           true, // the FetchContent_* commands are registered
	"GNUInstallDirs":         false,
}

// probeResult runs one check: it writes the source, compiles it, caches the
// answer, and reports it the way the Check modules do.
//
// The caching is not an optimisation. A check's answer is a property of the
// compiler, not of the project, so re-asking on every configure would make a
// no-op configure slow for nothing -- and, more importantly, a user who edits
// the cached answer to work around a bad probe expects the edit to stick.
func (e *evaluator) probeResult(ctx context.Context, name, description, source, language string, opts probeOptions) error {
	if entry, ok := e.state.Cache.Get(name); ok {
		// Already answered. The value is deliberately not re-derived.
		_ = entry
		return nil
	}

	e.state.log("STATUS", "Performing Test "+description)

	dir := joinPath(e.state.Dir().Binary, "CMakeFiles")
	if err := e.fs.MkdirAll(dir); err != nil {
		return e.fatalf("could not create %s: %v", dir, err)
	}
	file := joinPath(dir, "check_"+MakeCIdentifier(name)+sourceExtension(language))
	if err := e.fs.WriteFile(file, []byte(source)); err != nil {
		return e.fatalf("could not write %s: %v", file, err)
	}

	req := CompileRequest{
		Sources:  []string{file},
		Language: language,
		Dir:      e.state.Dir().Binary,
	}
	req.Defines = append(req.Defines, cleanDefines(SplitList(e.state.GetVar("CMAKE_REQUIRED_DEFINITIONS")))...)
	req.Defines = append(req.Defines, opts.defines...)
	req.IncludeDirs = append(req.IncludeDirs, SplitList(e.state.GetVar("CMAKE_REQUIRED_INCLUDES"))...)
	req.LinkLibs = append(req.LinkLibs, SplitList(e.state.GetVar("CMAKE_REQUIRED_LIBRARIES"))...)
	req.LinkLibs = append(req.LinkLibs, opts.libraries...)
	req.CompileOpts = append(req.CompileOpts, SplitList(e.state.GetVar("CMAKE_REQUIRED_FLAGS"))...)
	req.CompileOpts = append(req.CompileOpts, opts.flags...)

	passed, output := e.runProbe(ctx, req, opts.run)

	// A FAIL_REGEX turns a compile that succeeded into a failure, which is how
	// a check detects a warning that means the feature is not really there.
	for _, expr := range opts.failRegex {
		if re, err := regex.Compile(expr); err == nil && re.MatchString(output) {
			passed = false
		}
	}

	if passed {
		e.state.log("STATUS", "Performing Test "+description+" - Success")
		e.state.Cache.Set(name, "1", CacheInternal, "Result of "+description, true)
	} else {
		e.state.log("STATUS", "Performing Test "+description+" - Failed")
		e.state.Cache.Set(name, "", CacheInternal, "Result of "+description, true)
	}
	e.state.Current.Unset(name)
	return nil
}

// probeOptions are the per-check settings that reach the compiler.
type probeOptions struct {
	defines   []string
	libraries []string
	flags     []string
	failRegex []string
	run       bool // the program must also run, not merely link
}

// runProbe compiles, and optionally runs, one probe.
func (e *evaluator) runProbe(ctx context.Context, req CompileRequest, mustRun bool) (bool, string) {
	if e.state.Compiler == nil {
		e.state.Unsupported = append(e.state.Unsupported, "try_compile")
		return false, "no compiler is available"
	}
	if mustRun {
		runner, ok := e.state.Compiler.(Runner2)
		if !ok || e.state.Runner == nil {
			return false, "no runner is available"
		}
		code, output, err := runner.CompileLinkAndRun(ctx, req, e.state.Runner)
		return err == nil && code == 0, output
	}
	output, err := e.state.Compiler.CompileAndLink(ctx, req)
	if errors.Is(err, ErrNoCompiler) {
		e.state.Unsupported = append(e.state.Unsupported, "try_compile")
	}
	return err == nil, output
}

func sourceExtension(language string) string {
	if language == "CXX" {
		return ".cpp"
	}
	return ".c"
}

// ----------------------------------------------------------------------------
// The checks

func cmdCheckIncludeFile(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("check_include_file called with incorrect number of arguments")
	}
	header, variable := v[0], v[1]
	var opts probeOptions
	if len(v) > 2 {
		opts.flags = append(opts.flags, v[2])
	}
	source := fmt.Sprintf("#include <%s>\nint main(void) { return 0; }\n", header)
	return e.probeResult(ctx, variable, variable, source, "C", opts)
}

func cmdCheckIncludeFileCXX(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("check_include_file_cxx called with incorrect number of arguments")
	}
	source := fmt.Sprintf("#include <%s>\nint main() { return 0; }\n", v[0])
	return e.probeResult(ctx, v[1], v[1], source, "CXX", probeOptions{})
}

func cmdCheckIncludeFiles(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("check_include_files called with incorrect number of arguments")
	}
	language := "C"
	for i := 2; i+1 < len(v); i++ {
		if v[i] == "LANGUAGE" {
			language = v[i+1]
		}
	}
	var b strings.Builder
	// The headers are included in the order given, because one of them
	// routinely depends on another having been included first.
	for _, h := range SplitList(v[0]) {
		fmt.Fprintf(&b, "#include <%s>\n", h)
	}
	b.WriteString("int main(void) { return 0; }\n")
	return e.probeResult(ctx, v[1], v[1], b.String(), language, probeOptions{})
}

func cmdCheckSourceCompiles(language string) cmdFunc {
	return func(ctx context.Context, e *evaluator, args []Arg) error {
		v := Args(args)
		if len(v) < 2 {
			return e.fatalf("check_source_compiles called with incorrect number of arguments")
		}
		source, variable := v[0], v[1]
		var opts probeOptions
		for i := 2; i < len(v); i++ {
			if v[i] == "FAIL_REGEX" && i+1 < len(v) {
				opts.failRegex = append(opts.failRegex, v[i+1])
				i++
			}
		}
		return e.probeResult(ctx, variable, variable, source, language, opts)
	}
}

func cmdCheckSourceRuns(language string) cmdFunc {
	return func(ctx context.Context, e *evaluator, args []Arg) error {
		v := Args(args)
		if len(v) < 2 {
			return e.fatalf("check_source_runs called with incorrect number of arguments")
		}
		return e.probeResult(ctx, v[1], v[1], v[0], language, probeOptions{run: true})
	}
}

// cmdCheckFunctionExists tests that a symbol links, which is a weaker question
// than whether it is declared: the probe declares it itself.
func cmdCheckFunctionExists(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("check_function_exists called with incorrect number of arguments")
	}
	function, variable := v[0], v[1]
	source := fmt.Sprintf(`char %s(void);
int main(void) { return (int)(long)&%s; }
`, function, function)
	return e.probeResult(ctx, variable, variable, source, "C", probeOptions{})
}

// cmdCheckSymbolExists tests that a symbol is usable after including some
// headers, which covers a macro as well as a function -- and a macro is why
// this exists separately from check_function_exists.
func cmdCheckSymbolExists(language string) cmdFunc {
	return func(ctx context.Context, e *evaluator, args []Arg) error {
		v := Args(args)
		if len(v) < 3 {
			return e.fatalf("check_symbol_exists called with incorrect number of arguments")
		}
		symbol, headers, variable := v[0], SplitList(v[1]), v[2]

		var b strings.Builder
		for _, h := range headers {
			fmt.Fprintf(&b, "#include <%s>\n", h)
		}
		// Taking the address works for a function; the #ifndef branch catches a
		// macro, which has no address to take.
		fmt.Fprintf(&b, `
int main(void)
{
#ifndef %s
  return ((int*)(&%s))[0];
#else
  return 0;
#endif
}
`, symbol, symbol)
		return e.probeResult(ctx, variable, variable, b.String(), language, probeOptions{})
	}
}

func cmdCheckLibraryExists(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 4 {
		return e.fatalf("check_library_exists called with incorrect number of arguments")
	}
	library, function, location, variable := v[0], v[1], v[2], v[3]
	source := fmt.Sprintf(`char %s(void);
int main(void) { return (int)(long)&%s; }
`, function, function)
	opts := probeOptions{libraries: []string{library}}
	if location != "" {
		opts.flags = append(opts.flags, "-L"+location)
	}
	return e.probeResult(ctx, variable, variable, source, "C", opts)
}

// cmdCheckTypeSize reports whether a type exists and how large it is.
//
// The size is obtained without running anything: an array declared with a
// negative dimension is a compile error, so a probe that compiles only when the
// size matches turns the question into a search. That is how CMake does it too,
// and it is what makes the check work when cross-compiling.
func cmdCheckTypeSize(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("check_type_size called with incorrect number of arguments")
	}
	typeName, variable := v[0], v[1]
	language := "C"
	for i := 2; i < len(v); i++ {
		if v[i] == "LANGUAGE" && i+1 < len(v) {
			language = v[i+1]
		}
	}

	if _, ok := e.state.Cache.Get(variable); ok {
		return nil
	}
	e.state.log("STATUS", "Check size of "+typeName)

	headers := SplitList(e.state.GetVar("CMAKE_EXTRA_INCLUDE_FILES"))
	for _, size := range []int{1, 2, 4, 8, 16, 32} {
		var b strings.Builder
		for _, h := range headers {
			fmt.Fprintf(&b, "#include <%s>\n", h)
		}
		fmt.Fprintf(&b, `#include <stddef.h>
int probe[(sizeof(%s) == %d) ? 1 : -1];
int main(void) { (void)probe; return 0; }
`, typeName, size)

		if ok, _ := e.probeSource(ctx, variable, b.String(), language); ok {
			e.state.log("STATUS", fmt.Sprintf("Check size of %s - done", typeName))
			e.state.Cache.Set(variable, fmt.Sprint(size), CacheInternal,
				"Size of "+typeName, true)
			e.state.Current.Unset(variable)
			e.state.SetVar("HAVE_"+variable, "1")
			return nil
		}
	}
	e.state.log("STATUS", "Check size of "+typeName+" - failed")
	e.state.Cache.Set(variable, "", CacheInternal, "Size of "+typeName, true)
	e.state.Current.Unset(variable)
	return nil
}

// probeSource compiles one source without reporting or caching, which the
// size search needs because it asks the same question several times.
func (e *evaluator) probeSource(ctx context.Context, name, source, language string) (bool, string) {
	dir := joinPath(e.state.Dir().Binary, "CMakeFiles")
	if err := e.fs.MkdirAll(dir); err != nil {
		return false, err.Error()
	}
	file := joinPath(dir, "size_"+MakeCIdentifier(name)+sourceExtension(language))
	if err := e.fs.WriteFile(file, []byte(source)); err != nil {
		return false, err.Error()
	}
	return e.runProbe(ctx, CompileRequest{
		Sources:     []string{file},
		Language:    language,
		Dir:         e.state.Dir().Binary,
		IncludeDirs: SplitList(e.state.GetVar("CMAKE_REQUIRED_INCLUDES")),
	}, false)
}

// cmdCheckCompilerFlag tests whether the compiler accepts a flag. The probe is
// a trivial program: what is being tested is the flag, not the code, so a
// warning about the flag has to count as a failure.
func cmdCheckCompilerFlag(language string) cmdFunc {
	return func(ctx context.Context, e *evaluator, args []Arg) error {
		v := Args(args)
		if len(v) < 2 {
			return e.fatalf("check_compiler_flag called with incorrect number of arguments")
		}
		flag, variable := v[0], v[1]
		source := "int main(void) { return 0; }\n"
		if language == "CXX" {
			source = "int main() { return 0; }\n"
		}
		return e.probeResult(ctx, variable, variable, source, language, probeOptions{
			flags: []string{flag},
			failRegex: []string{
				"unrecognized .*option", "unknown .*option", "not recognized",
				"ignoring unknown option", "warning: unknown", "D9002",
			},
		})
	}
}
