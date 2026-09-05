package eval

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/vertex-language/go-cmake/run"
)

// try_compile is how a project asks a question it cannot answer by reading:
// does this compiler accept this construct, does this header exist, does this
// symbol resolve. Everything built on it -- check_include_file,
// check_c_source_compiles, check_symbol_exists, and most of what a
// Find<Name>.cmake does to validate a candidate -- reduces to compiling one
// small file and seeing whether it worked.
//
// Reporting failure without trying is the worst available answer. A project
// told its compiler lacks a feature it has will disable the feature, and the
// build that results is not wrong in any way that shows up until much later.
// Reporting success without trying is worse still. So this compiles.

// ErrNoCompiler is returned by a [Compiler] asked about a language it has no
// compiler for. It is a distinct error because the two outcomes mean different
// things: a probe that ran and said no is an answer, while a probe that could
// not run at all is a gap the user should hear about.
var ErrNoCompiler = errors.New("no compiler for this language")

// Compiler is what try_compile needs to know about the toolchain.
//
// It is an interface, and set on the State rather than imported, because eval
// sits below the toolchain package: the configure phase must not depend on how
// a compiler is discovered, only on being handed one.
type Compiler interface {
	// CompileAndLink builds one executable from the given sources and reports
	// the command output. A nil error means the compiler produced a binary.
	CompileAndLink(ctx context.Context, req CompileRequest) (output string, err error)
}

// Runner2 is the optional half of [Compiler]: a compiler that can also run what
// it built, which is what try_run needs. It is separate because compiling and
// running are different capabilities -- a cross-compiler can do the first and
// not the second, and saying so in the type is better than discovering it at
// the point of use.
type Runner2 interface {
	Compiler
	CompileLinkAndRun(ctx context.Context, req CompileRequest, runner run.Runner) (exitCode int, output string, err error)
}

// CompileRequest is one probe.
type CompileRequest struct {
	// Sources are the files to compile, already absolute.
	Sources []string

	// Language is "C" or "CXX"; empty means infer from the source extension.
	Language string

	// Dir is a scratch directory the probe may write into.
	Dir string

	// Defines, IncludeDirs, CompileOptions and LinkLibraries are the settings
	// the caller asked to be in force for the probe, which is what makes
	// check_symbol_exists able to test a symbol from a specific library.
	Defines      []string
	IncludeDirs  []string
	CompileOpts  []string
	LinkLibs     []string
	LinkOptions  []string
	Standard     string
	StandardLang string
}

func cmdTryCompile(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("try_compile called with incorrect number of arguments")
	}
	result := v[0]

	req, outputVar, err := e.parseTryCompile(v[1:])
	if err != nil {
		return err
	}

	if len(req.Sources) == 0 {
		return e.fatalf("try_compile given no sources to compile")
	}
	if e.state.Compiler == nil {
		e.state.SetVar(result, "FALSE")
		e.state.Unsupported = append(e.state.Unsupported, "try_compile")
		return nil
	}

	output, buildErr := e.state.Compiler.CompileAndLink(ctx, req)
	if outputVar != "" {
		e.state.SetVar(outputVar, output)
	}
	if buildErr != nil {
		e.state.SetVar(result, "FALSE")
		// A probe that could not be attempted is recorded, so that configure
		// can say the result is unanswered rather than negative.
		if errors.Is(buildErr, ErrNoCompiler) {
			e.state.Unsupported = append(e.state.Unsupported, "try_compile")
		}
		return nil
	}
	// The result is cached because a probe is expensive and its answer does not
	// change between configures of the same tree, which is why CMake caches it
	// too and why deleting the cache is how you make it ask again.
	e.state.SetVar(result, "TRUE")
	return nil
}

// parseTryCompile reads the argument forms try_compile accepts.
func (e *evaluator) parseTryCompile(v []string) (CompileRequest, string, error) {
	req := CompileRequest{Dir: e.state.Dir().Binary}
	outputVar := ""

	// The old signature is positional: <bindir> <srcfile>. The new one names
	// everything. Both are still in wide use, and they are told apart by
	// whether SOURCES appears.
	keyword := ""
	positional := 0
	for i := 0; i < len(v); i++ {
		a := v[i]
		switch a {
		case "SOURCES", "CMAKE_FLAGS", "COMPILE_DEFINITIONS", "LINK_LIBRARIES",
			"LINK_OPTIONS", "OUTPUT_VARIABLE", "COPY_FILE", "COPY_FILE_ERROR",
			"SOURCE_FROM_CONTENT", "SOURCE_FROM_VAR", "SOURCE_FROM_FILE",
			"PROJECT", "SOURCE_DIR", "BINARY_DIR", "TARGET", "LOG_DESCRIPTION",
			"NO_CACHE", "NO_LOG", "C_STANDARD", "CXX_STANDARD",
			"C_STANDARD_REQUIRED", "CXX_STANDARD_REQUIRED", "C_EXTENSIONS",
			"CXX_EXTENSIONS", "LANGUAGE":
			keyword = a
			continue
		}
		switch keyword {
		case "":
			// Positional: the binary directory, then the source file.
			positional++
			if positional == 1 {
				req.Dir = e.state.absPath(a)
			} else {
				req.Sources = append(req.Sources, e.state.absPath(a))
			}
		case "SOURCES":
			req.Sources = append(req.Sources, e.state.absPath(a))
		case "COMPILE_DEFINITIONS":
			req.Defines = append(req.Defines, strings.TrimPrefix(a, "-D"))
		case "LINK_LIBRARIES":
			req.LinkLibs = append(req.LinkLibs, a)
		case "LINK_OPTIONS":
			req.LinkOptions = append(req.LinkOptions, a)
		case "OUTPUT_VARIABLE":
			outputVar = a
			keyword = ""
		case "LANGUAGE":
			req.Language = a
			keyword = ""
		case "C_STANDARD", "CXX_STANDARD":
			req.Standard = a
			req.StandardLang = strings.TrimSuffix(keyword, "_STANDARD")
			keyword = ""
		case "CMAKE_FLAGS":
			// CMAKE_FLAGS carries -DVAR=value settings for the sub-project.
			// The two that matter to a probe are the include path and the
			// definitions, which several Check modules pass this way.
			if name, value, found := strings.Cut(strings.TrimPrefix(a, "-D"), "="); found {
				switch name {
				case "INCLUDE_DIRECTORIES":
					req.IncludeDirs = append(req.IncludeDirs, SplitList(value)...)
				case "COMPILE_DEFINITIONS":
					req.Defines = append(req.Defines, SplitList(value)...)
				case "LINK_LIBRARIES":
					req.LinkLibs = append(req.LinkLibs, SplitList(value)...)
				}
			}
		case "PROJECT", "SOURCE_DIR", "BINARY_DIR", "TARGET":
			// The whole-project form builds a directory rather than a file.
			// It is rare, and the sources it would need are not known here.
			keyword = ""
		default:
			keyword = ""
		}
	}

	// CMAKE_REQUIRED_* are what the Check modules set around a probe, and a
	// project may set them directly for the same reason.
	req.Defines = append(req.Defines, cleanDefines(SplitList(e.state.GetVar("CMAKE_REQUIRED_DEFINITIONS")))...)
	req.IncludeDirs = append(req.IncludeDirs, SplitList(e.state.GetVar("CMAKE_REQUIRED_INCLUDES"))...)
	req.LinkLibs = append(req.LinkLibs, SplitList(e.state.GetVar("CMAKE_REQUIRED_LIBRARIES"))...)
	req.CompileOpts = append(req.CompileOpts, SplitList(e.state.GetVar("CMAKE_REQUIRED_FLAGS"))...)
	req.LinkOptions = append(req.LinkOptions, SplitList(e.state.GetVar("CMAKE_REQUIRED_LINK_OPTIONS"))...)
	return req, outputVar, nil
}

// cleanDefines strips the -D a caller may have included, since the compiler
// flag is added per toolchain rather than carried in the value.
func cleanDefines(defs []string) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		if d != "" {
			out = append(out, strings.TrimPrefix(strings.TrimPrefix(d, "-D"), "/D"))
		}
	}
	return out
}

// cmdTryRun compiles a probe and then runs it, which is how a project asks a
// question only the built program can answer -- the size of a type, the
// endianness of the machine it will run on.
func cmdTryRun(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("try_run called with incorrect number of arguments")
	}
	runResult, compileResult := v[0], v[1]

	req, outputVar, err := e.parseTryCompile(v[2:])
	if err != nil {
		return err
	}
	if e.state.Compiler == nil || e.state.Runner == nil || len(req.Sources) == 0 {
		e.state.SetVar(compileResult, "FALSE")
		e.state.SetVar(runResult, "FAILED_TO_RUN")
		e.state.Unsupported = append(e.state.Unsupported, "try_run")
		return nil
	}

	// try_run needs the binary, so the request has to name where it goes.
	runner, ok := e.state.Compiler.(Runner2)
	if !ok {
		e.state.SetVar(compileResult, "FALSE")
		e.state.SetVar(runResult, "FAILED_TO_RUN")
		e.state.Unsupported = append(e.state.Unsupported, "try_run")
		return nil
	}

	code, output, err := runner.CompileLinkAndRun(ctx, req, e.state.Runner)
	if outputVar != "" {
		e.state.SetVar(outputVar, output)
	}
	if err != nil {
		e.state.SetVar(compileResult, "FALSE")
		e.state.SetVar(runResult, "FAILED_TO_RUN")
		return nil
	}
	e.state.SetVar(compileResult, "TRUE")
	e.state.SetVar(runResult, strconv.Itoa(code))
	return nil
}
