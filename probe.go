package cmake

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/run"
	"github.com/vertex-language/go-cmake/toolchain"
)

// probe answers try_compile by actually compiling.
//
// It builds one executable in one command rather than compiling and linking
// separately, because that is all a probe needs and because the compiler driver
// already knows how to do both. What it is asked is never "produce this
// artefact" but "does this work", and the answer is the driver's exit status.
type probe struct {
	tc     *toolchain.Toolchain
	runner run.Runner
	dir    string // where scratch files go
}

// CompileAndLink builds the request's sources into an executable.
func (p *probe) CompileAndLink(ctx context.Context, req eval.CompileRequest) (string, error) {
	code, output, _, err := p.build(ctx, req)
	if err != nil {
		return output, err
	}
	if code != 0 {
		return output, fmt.Errorf("compiler exited with status %d", code)
	}
	return output, nil
}

// CompileLinkAndRun builds the probe and then runs it, which is what try_run
// needs: the answer is in the program's exit status, not the compiler's.
func (p *probe) CompileLinkAndRun(ctx context.Context, req eval.CompileRequest, runner run.Runner) (int, string, error) {
	code, output, binary, err := p.build(ctx, req)
	if err != nil {
		return -1, output, err
	}
	if code != 0 {
		return -1, output, fmt.Errorf("compiler exited with status %d", code)
	}

	var buf bytes.Buffer
	runCode, err := runner.Run(ctx, run.Command{
		Argv:   []string{binary},
		Dir:    filepath.Dir(binary),
		Stdout: &buf,
		Stderr: &buf,
	})
	if err != nil {
		return -1, output + buf.String(), err
	}
	return runCode, output + buf.String(), nil
}

// build compiles and links, returning the compiler's exit status, its output,
// and where the binary landed.
func (p *probe) build(ctx context.Context, req eval.CompileRequest) (code int, output, binary string, err error) {
	if p.runner == nil {
		return -1, "", "", fmt.Errorf("no process runner")
	}
	lang := req.Language
	if lang == "" {
		lang = languageOf(req.Sources)
	}
	compiler, ok := p.tc.Compilers[lang]
	if !ok {
		return -1, "", "", fmt.Errorf("%w: %s", eval.ErrNoCompiler, lang)
	}

	// Each probe gets its own directory named for what it is, so that a failed
	// one can be looked at and a second probe cannot overwrite the first.
	scratch := path.Join(p.dir, "CMakeFiles", "probe-"+probeID(req))
	if err := os.MkdirAll(filepath.FromSlash(scratch), 0755); err != nil {
		return -1, "", "", err
	}
	binary = path.Join(scratch, "probe"+p.tc.ExeSuffix)

	argv := p.commandLine(compiler, req, binary, scratch)
	var buf bytes.Buffer
	code, err = p.runner.Run(ctx, run.Command{
		Argv:   argv,
		Line:   strings.Join(quoteAll(argv), " "),
		Dir:    filepath.FromSlash(scratch),
		Env:    p.env(),
		Stdout: &buf,
		Stderr: &buf,
	})
	return code, strings.Join(argv, " ") + "\n" + buf.String(), binary, err
}

// commandLine assembles the one command that compiles and links the probe.
func (p *probe) commandLine(c toolchain.Compiler, req eval.CompileRequest, binary, scratch string) []string {
	msvc := p.tc.Kind() == toolchain.MSVC
	argv := []string{c.Path}

	definePrefix, includePrefix := "-D", "-I"
	if msvc {
		definePrefix, includePrefix = "/D", "/I"
	}
	for _, d := range req.Defines {
		argv = append(argv, definePrefix+d)
	}
	for _, d := range req.IncludeDirs {
		argv = append(argv, includePrefix+d)
	}
	for _, d := range p.tc.SystemIncludes() {
		argv = append(argv, includePrefix+d)
	}
	argv = append(argv, req.CompileOpts...)
	if flag := standardFlag(c, req); flag != "" {
		argv = append(argv, flag)
	}
	argv = append(argv, req.Sources...)

	if msvc {
		// cl.exe wants its output paths attached to the flag, and everything
		// after /link belongs to the linker.
		argv = append(argv, "/nologo", "/Fe"+filepath.FromSlash(binary), "/Fo"+filepath.FromSlash(scratch)+string(filepath.Separator))
		argv = append(argv, "/link")
		for _, d := range p.tc.SystemLibDirs() {
			argv = append(argv, "/LIBPATH:"+d)
		}
		argv = append(argv, req.LinkOptions...)
		for _, lib := range req.LinkLibs {
			argv = append(argv, msvcLibName(lib))
		}
		return argv
	}

	argv = append(argv, "-o", binary)
	argv = append(argv, req.LinkOptions...)
	for _, d := range p.tc.SystemLibDirs() {
		argv = append(argv, "-L"+d)
	}
	for _, lib := range req.LinkLibs {
		argv = append(argv, unixLibName(lib))
	}
	return argv
}

// standardFlag renders C_STANDARD or CXX_STANDARD for this compiler.
func standardFlag(c toolchain.Compiler, req eval.CompileRequest) string {
	if req.Standard == "" {
		return ""
	}
	if c.ID == "MSVC" {
		if req.StandardLang == "CXX" {
			return "/std:c++" + req.Standard
		}
		return "/std:c" + req.Standard
	}
	if req.StandardLang == "CXX" {
		return "-std=c++" + req.Standard
	}
	return "-std=c" + req.Standard
}

// msvcLibName renders a link library for the MSVC linker, which takes file
// names rather than the -l form.
func msvcLibName(lib string) string {
	if strings.ContainsAny(lib, "/\\") || strings.HasSuffix(lib, ".lib") {
		return lib
	}
	return lib + ".lib"
}

// unixLibName renders a link library for a compiler driver: a path stays a
// path, a bare name becomes -lname.
func unixLibName(lib string) string {
	if strings.ContainsAny(lib, "/\\") || strings.HasPrefix(lib, "-") {
		return lib
	}
	return "-l" + lib
}

// env is the environment the compiler runs with. MSVC needs INCLUDE and LIB
// even though the flags carry the same directories, because some of its own
// headers reach for them.
func (p *probe) env() []string {
	env := os.Environ()
	if p.tc.MSVC == nil {
		return env
	}
	if dirs := p.tc.SystemIncludes(); len(dirs) > 0 {
		env = append(env, "INCLUDE="+strings.Join(dirs, ";"))
	}
	if dirs := p.tc.SystemLibDirs(); len(dirs) > 0 {
		env = append(env, "LIB="+strings.Join(dirs, ";"))
	}
	return env
}

// languageOf picks the compiler from the source extensions. A probe with any
// C++ source is a C++ probe: a C compiler asked to build one fails in a way
// that would be read as the feature being absent.
func languageOf(sources []string) string {
	for _, s := range sources {
		switch strings.ToLower(path.Ext(s)) {
		case ".cpp", ".cxx", ".cc", ".c++", ".cppm", ".mm":
			return "CXX"
		}
	}
	return "C"
}

// probeID names a probe's directory after its content, so that the same
// question reuses the same directory and a different one gets its own.
func probeID(req eval.CompileRequest) string {
	h := sha1.New()
	for _, s := range req.Sources {
		fmt.Fprintln(h, s)
	}
	fmt.Fprintln(h, strings.Join(req.Defines, ";"))
	fmt.Fprintln(h, strings.Join(req.IncludeDirs, ";"))
	fmt.Fprintln(h, strings.Join(req.LinkLibs, ";"))
	fmt.Fprintln(h, req.Standard, req.StandardLang)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func quoteAll(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t\"") {
			out[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		} else {
			out[i] = a
		}
	}
	return out
}
