package eval

// Commands that bring in more CMake code: another file, another directory,
// or a string evaluated as source.

import (
	"context"
	"strings"
)

func init() {
	register("include", cmdInclude)
	register("include_guard", cmdIncludeGuard)
	register("add_subdirectory", cmdAddSubdirectory)
	register("cmake_language", cmdCMakeLanguage)
}

func cmdInclude(ctx context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("include called with incorrect number of arguments")
	}
	file := vals[0]
	optional := false
	resultVar := ""
	for i := 1; i < len(vals); i++ {
		switch vals[i] {
		case "OPTIONAL":
			optional = true
		case "RESULT_VARIABLE":
			if i+1 < len(vals) {
				resultVar = vals[i+1]
				i++
			}
		}
	}

	path := e.resolveInclude(file)
	if path == "" {
		if resultVar != "" {
			e.state.SetVar(resultVar, "NOTFOUND")
		}
		if optional {
			return nil
		}
		return e.fatalf("include could not find requested file:\n\n    %s", file)
	}
	if resultVar != "" {
		e.state.SetVar(resultVar, path)
	}
	err := e.evalFile(ctx, path)
	if _, ok := err.(returnSignal); ok {
		// return() in an included file ends that file only.
		return nil
	}
	return err
}

// resolveInclude finds an include() argument: an existing path, or a module
// name looked up in CMAKE_MODULE_PATH and then the bundled modules.
func (e *evaluator) resolveInclude(file string) string {
	candidates := []string{}
	if isAbsolutePath(file) || strings.ContainsAny(file, "/\\") {
		candidates = append(candidates, e.state.absPath(file))
	}
	if !strings.HasSuffix(file, ".cmake") {
		base := file + ".cmake"
		for _, dir := range SplitList(e.state.GetVar("CMAKE_MODULE_PATH")) {
			candidates = append(candidates, joinPath(dir, base))
		}
		candidates = append(candidates, e.state.absPath(base))
	} else {
		for _, dir := range SplitList(e.state.GetVar("CMAKE_MODULE_PATH")) {
			candidates = append(candidates, joinPath(dir, file))
		}
		candidates = append(candidates, e.state.absPath(file))
	}
	for _, c := range candidates {
		if fi, err := e.fs.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

func cmdIncludeGuard(_ context.Context, e *evaluator, args []Arg) error {
	scope := "DIRECTORY"
	if len(args) > 0 {
		scope = args[0].Val
	}
	file := e.state.GetVar("CMAKE_CURRENT_LIST_FILE")
	key := scope + ":" + file
	if scope == "DIRECTORY" {
		key = scope + ":" + e.state.Dir().Source + ":" + file
	}
	if e.state.IncludeGuards[key] {
		return returnSignal{}
	}
	e.state.IncludeGuards[key] = true
	return nil
}

func cmdAddSubdirectory(ctx context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("add_subdirectory called with incorrect number of arguments")
	}
	src := vals[0]
	binary := ""
	for _, v := range vals[1:] {
		if v == "EXCLUDE_FROM_ALL" || v == "SYSTEM" {
			continue
		}
		binary = v
	}

	srcDir := e.state.absPath(src)
	if binary == "" {
		// The default binary directory mirrors the source directory's position
		// relative to the directory adding it, so that a tree three levels deep
		// lands three levels deep in the build tree rather than repeating each
		// ancestor's name.
		rel := relPath(e.state.Dir().Source, srcDir)
		if rel == "" {
			return e.fatalf("add_subdirectory not given a binary directory but the given source\n"+
				"  directory %q is not a subdirectory of %q.  When specifying an\n"+
				"  out-of-tree source a binary directory must be explicitly specified.",
				srcDir, e.state.Dir().Source)
		}
		binary = joinPath(e.state.Dir().Binary, rel)
	} else if !isAbsolutePath(binary) {
		binary = joinPath(e.state.Dir().Binary, binary)
	}

	listFile := joinPath(srcDir, "CMakeLists.txt")
	if fi, err := e.fs.Stat(listFile); err != nil || fi.IsDir() {
		return e.fatalf("add_subdirectory given source %q which is not an existing directory containing a CMakeLists.txt file.", src)
	}

	e.state.PushDir(srcDir, binary)
	defer e.state.PopDir()
	err := e.evalFile(ctx, listFile)
	if _, ok := err.(returnSignal); ok {
		return nil
	}
	return err
}

func cmdCMakeLanguage(ctx context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("cmake_language called with incorrect number of arguments")
	}
	switch vals[0] {
	case "CALL":
		if len(vals) < 2 {
			return e.fatalf("cmake_language CALL requires a command name")
		}
		return e.callByName(ctx, vals[1], Strings(vals[2:]...))
	case "EVAL":
		if len(vals) < 2 || vals[1] != "CODE" {
			return e.fatalf("cmake_language EVAL requires the CODE keyword")
		}
		return e.evalSource(ctx, strings.Join(vals[2:], " "), "<cmake_language EVAL>")
	case "DEFER":
		// Deferred calls run at the end of the current directory; they are
		// recorded now and flushed by the directory driver.
		return e.deferCall(vals[1:])
	case "GET_MESSAGE_LOG_LEVEL":
		if len(vals) > 1 {
			e.state.SetVar(vals[1], "STATUS")
		}
		return nil
	case "SET_DEPENDENCY_PROVIDER", "EXIT":
		return nil
	}
	return e.fatalf("cmake_language given unknown mode %q", vals[0])
}

// deferCall records a cmake_language(DEFER CALL ...) for the current directory.
func (e *evaluator) deferCall(vals []string) error {
	i := 0
	for i < len(vals) && vals[i] != "CALL" {
		// DIRECTORY <dir> and ID <id> are accepted and ignored: the call still
		// runs at the end of the directory that recorded it.
		i++
	}
	if i >= len(vals) {
		return e.fatalf("cmake_language DEFER requires a CALL")
	}
	rest := vals[i+1:]
	if len(rest) == 0 {
		return e.fatalf("cmake_language DEFER CALL requires a command name")
	}
	d := e.state.Dir()
	d.Deferred = append(d.Deferred, append([]string{}, rest...))
	return nil
}
