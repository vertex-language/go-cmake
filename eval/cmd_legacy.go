package eval

import (
	"context"
	"strings"
)

// The commands here are CMake 2.x spellings that never went away.
//
// Every one of them has a modern replacement and CMake's documentation says so,
// which is exactly why they still matter: a project written against them keeps
// working, so nobody rewrites it, so it is still what a build encounters twenty
// years later. Implementing them costs a few lines each and is the difference
// between configuring such a project and refusing it.

func init() {
	register("exec_program", cmdExecProgram)
	register("remove", cmdRemove)
	register("install_files", cmdInstallLegacy("FILES"))
	register("install_programs", cmdInstallLegacy("PROGRAMS"))
	register("install_targets", cmdInstallTargets)
	register("load_cache", cmdLoadCache)
	register("build_command", cmdBuildCommand)
	register("create_test_sourcelist", cmdCreateTestSourcelist)
	register("subdir_depends", cmdNoOp)
	register("output_required_files", cmdNoOp)
	register("variable_requires", cmdVariableRequires)
	register("export_library_dependencies", cmdNoOp)
	register("use_mangled_mesa", cmdNoOp)
	register("utility_source", cmdNoOp)
	register("qt_wrap_cpp", cmdNoOp)
	register("qt_wrap_ui", cmdNoOp)
	register("fltk_wrap_ui", cmdNoOp)
	register("load_command", cmdNoOp)
	register("include_external_msproject", cmdNoOp)
	register("build_name", cmdBuildName)
}

// cmdExecProgram is execute_process with a worse interface: one command, a
// working directory, and the output and status in two variables.
func cmdExecProgram(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("exec_program called with incorrect number of arguments")
	}

	// CMP0153 retired exec_program. Under NEW it is an error; before that a
	// warning, which is how a policy gives a project time to move off a command
	// rather than breaking it on the day the replacement arrives.
	switch e.state.PolicyGet("CMP0153") {
	case "NEW":
		return e.fatalf("The exec_program command should not be called; see CMP0153.  Use" +
			"\n  execute_process() instead.")
	case "OLD":
	default:
		e.state.log("POLICY", "Policy CMP0153 is not set: The exec_program command should not be called."+
			"\n  Run \"cmake --help-policy CMP0153\" for policy details.  Use the cmake_policy"+
			"\n  command to set the policy and suppress this warning."+
			"\n\n  Use execute_process() instead.")
	}

	program := v[0]
	workDir := ""
	var arguments []string
	outVar, returnVar := "", ""

	i := 1
	// The second positional argument is a working directory, unless it is one
	// of the keywords -- which is the whole trouble with this command.
	if i < len(v) && v[i] != "ARGS" && v[i] != "OUTPUT_VARIABLE" && v[i] != "RETURN_VALUE" {
		workDir = e.state.absPath(v[i])
		i++
	}
	keyword := ""
	for ; i < len(v); i++ {
		switch v[i] {
		case "ARGS", "OUTPUT_VARIABLE", "RETURN_VALUE":
			keyword = v[i]
			continue
		}
		switch keyword {
		case "ARGS":
			arguments = append(arguments, v[i])
		case "OUTPUT_VARIABLE":
			outVar = v[i]
			keyword = ""
		case "RETURN_VALUE":
			returnVar = v[i]
			keyword = ""
		}
	}

	// The arguments arrive as one string in the old form, so they are split the
	// way a shell would rather than passed as a single argument.
	var argv []string
	argv = append(argv, program)
	for _, a := range arguments {
		argv = append(argv, splitCommandLine(a, isWindows())...)
	}

	rest := []string{"COMMAND"}
	rest = append(rest, argv...)
	if workDir != "" {
		rest = append(rest, "WORKING_DIRECTORY", workDir)
	}
	if outVar != "" {
		rest = append(rest, "OUTPUT_VARIABLE", outVar, "OUTPUT_STRIP_TRAILING_WHITESPACE")
	}
	if returnVar != "" {
		rest = append(rest, "RESULT_VARIABLE", returnVar)
	}
	return cmdExecuteProcess(ctx, e, Strings(rest...))
}

// cmdRemove is list(REMOVE_ITEM) written before list() existed.
func cmdRemove(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("remove called with incorrect number of arguments")
	}
	name := v[0]
	drop := setOf(v[1:])
	var out []string
	for _, item := range SplitList(e.state.GetVar(name)) {
		if !drop[item] {
			out = append(out, item)
		}
	}
	e.state.SetVar(name, JoinList(out))
	return nil
}

// cmdInstallLegacy is install(FILES|PROGRAMS) with the destination first and
// the prefix implied.
func cmdInstallLegacy(kind string) cmdFunc {
	return func(ctx context.Context, e *evaluator, args []Arg) error {
		v := Args(args)
		if len(v) < 2 {
			return e.fatalf("%s called with incorrect number of arguments", strings.ToLower("install_"+kind))
		}
		dest := strings.TrimPrefix(v[0], "/")
		files := v[1:]

		// The old form accepts a regex over the current directory in place of a
		// file list, which is why it takes an extension argument.
		if len(files) >= 2 && strings.HasPrefix(files[0], ".") {
			matched, err := e.fs.Glob(joinPath(e.state.Dir().Source, "*"+files[0]))
			if err == nil {
				files = matched
			}
		}

		rule := []string{kind}
		rule = append(rule, files...)
		rule = append(rule, "DESTINATION", dest)
		return cmdInstall(ctx, e, Strings(rule...))
	}
}

// cmdInstallTargets is install(TARGETS) with the destination first.
func cmdInstallTargets(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("install_targets called with incorrect number of arguments")
	}
	dest := strings.TrimPrefix(v[0], "/")
	rule := []string{"TARGETS"}
	for _, name := range v[1:] {
		if name == "RUNTIME_DIRECTORY" {
			break
		}
		rule = append(rule, name)
	}
	rule = append(rule, "DESTINATION", dest)
	return cmdInstall(ctx, e, Strings(rule...))
}

// cmdLoadCache reads another build tree's cache.
//
// The form that matters reads named entries into local variables with a prefix,
// which is how one project consumes another's configuration without depending
// on it. The other form pollutes this project's own cache and is not honoured:
// silently importing another tree's every setting is not something a reader of
// the CMakeLists.txt would expect.
func cmdLoadCache(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("load_cache called with incorrect number of arguments")
	}
	dir := e.state.absPath(v[0])
	data, err := e.fs.ReadFile(joinPath(dir, "CMakeCache.txt"))
	if err != nil {
		return e.fatalf("load_cache could not read the cache in %s", v[0])
	}
	cache, err := ReadCache(strings.NewReader(string(data)))
	if err != nil {
		return e.fatalf("load_cache could not parse the cache in %s: %v", v[0], err)
	}

	prefix := ""
	var wanted []string
	keyword := ""
	for _, a := range v[1:] {
		switch a {
		case "READ_WITH_PREFIX", "EXCLUDE", "INCLUDE_INTERNALS":
			keyword = a
			continue
		}
		switch keyword {
		case "READ_WITH_PREFIX":
			if prefix == "" {
				prefix = a
			} else {
				wanted = append(wanted, a)
			}
		}
	}
	if prefix == "" {
		return e.fatalf("load_cache without READ_WITH_PREFIX is not supported: it would " +
			"import another tree's settings into this one without naming them")
	}
	for _, name := range wanted {
		if entry, ok := cache.Get(name); ok {
			e.state.SetVar(prefix+name, entry.Value)
		}
	}
	return nil
}

// cmdBuildCommand reports the command line that builds this tree, which a
// CTest script uses to drive a build it did not configure.
func cmdBuildCommand(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("build_command called with incorrect number of arguments")
	}
	variable := v[0]
	target, config := "", ""
	for i := 1; i+1 < len(v); i++ {
		switch v[i] {
		case "TARGET":
			target = v[i+1]
		case "CONFIGURATION":
			config = v[i+1]
		}
	}

	cmd := []string{e.state.GetVar("CMAKE_COMMAND"), "--build", "."}
	if config != "" {
		cmd = append(cmd, "--config", config)
	}
	if target != "" {
		cmd = append(cmd, "--target", target)
	}
	e.state.SetVar(variable, strings.Join(cmd, " "))
	return nil
}

// cmdCreateTestSourcelist writes the driver that a single test executable made
// of many test functions dispatches through.
func cmdCreateTestSourcelist(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("create_test_sourcelist called with incorrect number of arguments")
	}
	listVar, driverName := v[0], v[1]

	var tests []string
	for _, a := range v[2:] {
		switch a {
		case "EXTRA_INCLUDE", "FUNCTION":
			// Both take a value that customises the generated driver, which
			// this implementation does not vary.
			continue
		}
		tests = append(tests, a)
	}

	var b strings.Builder
	b.WriteString("/* Generated by go-cmake. Do not edit. */\n")
	b.WriteString("#include <stdio.h>\n#include <string.h>\n#include <stdlib.h>\n\n")
	var names []string
	for _, t := range tests {
		name := MakeCIdentifier(strings.TrimSuffix(BaseName(t), pathExtension(t)))
		names = append(names, name)
		b.WriteString("int " + name + "(int argc, char *argv[]);\n")
	}
	b.WriteString("\nint main(int argc, char *argv[])\n{\n")
	b.WriteString("  if (argc < 2) {\n    fprintf(stderr, \"usage: %s <test>\\n\", argv[0]);\n    return 1;\n  }\n")
	for _, name := range names {
		b.WriteString("  if (strcmp(argv[1], \"" + name + "\") == 0) return " + name + "(argc - 1, argv + 1);\n")
	}
	b.WriteString("  fprintf(stderr, \"no such test: %s\\n\", argv[1]);\n  return 1;\n}\n")

	driver := joinPath(e.state.Dir().Binary, driverName)
	if err := e.fs.MkdirAll(dirOf(driver)); err != nil {
		return e.fatalf("create_test_sourcelist could not create %s: %v", dirOf(driver), err)
	}
	if err := e.fs.WriteFile(driver, []byte(b.String())); err != nil {
		return e.fatalf("create_test_sourcelist could not write %s: %v", driverName, err)
	}

	sources := append([]string{driver}, tests...)
	e.state.SetVar(listVar, JoinList(sources))
	return nil
}

func pathExtension(p string) string {
	name := BaseName(p)
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}

// cmdVariableRequires is an assertion written before if() could do it.
func cmdVariableRequires(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("variable_requires called with incorrect number of arguments")
	}
	test, resultVar := v[0], v[1]
	if !IsOn(e.state.GetVar(test)) {
		return nil
	}
	for _, required := range v[2:] {
		if IsOff(e.state.GetVar(required)) {
			e.state.SetVar(resultVar, "0")
			return e.fatalf("%s is ON but %s is not set", test, required)
		}
	}
	e.state.SetVar(resultVar, "1")
	return nil
}

// cmdBuildName reports a name for this build, which an old dashboard script
// used to label its submission.
func cmdBuildName(_ context.Context, e *evaluator, args []Arg) error {
	if len(args) == 0 {
		return nil
	}
	name := hostSystemName()
	if id := e.state.GetVar("CMAKE_C_COMPILER_ID"); id != "" {
		name += "-" + id
	}
	e.state.Cache.Set(args[0].Val, name, CacheString, "Name of build", false)
	return nil
}
