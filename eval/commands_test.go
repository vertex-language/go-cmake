package eval_test

import (
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/eval"
)

// The command table is built from an init() in each cmd_*.go file. That keeps a
// command's registration beside its implementation, but it also means a command
// can disappear from the language by having its one registration line deleted,
// with nothing but a project failing at configure time to say so. This test is
// the check that nothing else performs: every command listed here must be
// present, so removing one is a deliberate act that shows up as a test change.
var required = []string{
	// Variables and control
	"set", "unset", "option", "message", "math",
	"cmake_minimum_required", "cmake_policy", "cmake_language",

	// Project structure
	"project", "include", "include_guard", "add_subdirectory",
	"enable_language", "enable_testing",

	// Properties
	"set_property", "get_property", "define_property", "mark_as_advanced",
	"set_directory_properties", "get_directory_property", "get_cmake_property",
	"set_target_properties", "get_target_property",
	"set_source_files_properties", "get_source_file_property",
	"set_tests_properties", "get_test_property",

	// Directory-level settings
	"add_definitions", "remove_definitions", "add_compile_definitions",
	"add_compile_options", "add_link_options", "include_directories",
	"link_directories", "link_libraries",

	// Targets
	"add_executable", "add_library", "add_custom_target", "add_custom_command",
	"add_dependencies", "add_test", "install",
	"target_sources", "target_include_directories", "target_compile_definitions",
	"target_compile_options", "target_compile_features", "target_link_libraries",
	"target_link_options", "target_link_directories", "target_precompile_headers",

	// The string, list, and file families
	"string", "list", "file", "configure_file",

	// Paths and arguments
	"get_filename_component", "cmake_path", "separate_arguments",
	"cmake_parse_arguments", "cmake_host_system_information", "site_name",

	// Finding things
	"find_package", "find_library", "find_program", "find_path", "find_file",
	"find_package_handle_standard_args",

	// Processes
	"execute_process", "try_compile", "try_run",

	// Accepted and ignored, but a project may not use an unknown command
	"aux_source_directory", "source_group", "variable_watch",
	"include_regular_expression", "export", "write_file", "make_directory",
}

// controlFlow is handled by the evaluator rather than the command table,
// because these need the unexpanded AST: a command receives its arguments
// already expanded, and `if` must not expand the branch it does not take.
var controlFlow = []string{
	"if", "elseif", "else", "endif",
	"foreach", "endforeach", "while", "endwhile",
	"function", "endfunction", "macro", "endmacro",
	"block", "endblock", "break", "continue", "return",
}

// notCMakeCommands are the names this package registers that `cmake
// --help-command-list` does not report. Each one needs a reason, because a
// command that CMake does not have is a command no CMakeLists.txt can call --
// it is dead weight at best and a divergence at worst.
var notCMakeCommands = map[string]string{
	// A module function rather than a builtin, but every Find<Name>.cmake ends
	// with it, so providing it is what makes a project's own find modules work
	// without shipping CMake's module directory.
	"find_package_handle_standard_args": "provided so project find modules work",

	// The Check modules are .cmake files in CMake's distribution rather than
	// builtins. They are implemented here as commands instead: shipping copies
	// would mean tracking a moving target belonging to a version of CMake this
	// package does not control, and the question each asks is small enough that
	// asking it directly is both less code and harder to get subtly wrong.
	"check_include_file":        "CheckIncludeFile module",
	"check_include_file_cxx":    "CheckIncludeFileCXX module",
	"check_include_files":       "CheckIncludeFiles module",
	"check_c_source_compiles":   "CheckCSourceCompiles module",
	"check_cxx_source_compiles": "CheckCXXSourceCompiles module",
	"check_c_source_runs":       "CheckCSourceRuns module",
	"check_cxx_source_runs":     "CheckCXXSourceRuns module",
	"check_function_exists":     "CheckFunctionExists module",
	"check_symbol_exists":       "CheckSymbolExists module",
	"check_cxx_symbol_exists":   "CheckCXXSymbolExists module",
	"check_library_exists":      "CheckLibraryExists module",
	"check_type_size":           "CheckTypeSize module",
	"check_c_compiler_flag":     "CheckCCompilerFlag module",
	"check_cxx_compiler_flag":   "CheckCXXCompilerFlag module",

	// FetchContent is a module too, and the busiest one in modern CMake. It is
	// implemented here for the same reason as the Check modules: the module is
	// several hundred lines of CMake that drives a sub-configure, and doing the
	// work directly is smaller than reproducing that machinery faithfully.
	"fetchcontent_declare":       "FetchContent module",
	"fetchcontent_makeavailable": "FetchContent module",
	"fetchcontent_populate":      "FetchContent module",
	"fetchcontent_getproperties": "FetchContent module",
	"fetchcontent_setpopulated":  "FetchContent module",
}

func TestEveryRequiredCommandIsRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, n := range eval.Commands() {
		have[n] = true
	}
	var missing []string
	for _, n := range required {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("commands are no longer registered: %s", strings.Join(missing, ", "))
	}
}

// TestCommandNamesAreLowercase guards the lookup rather than the table: command
// dispatch lowercases the name it is given, so a registration in any other case
// is unreachable and would fail only when a project called it.
func TestCommandNamesAreLowercase(t *testing.T) {
	for _, n := range eval.Commands() {
		if n != strings.ToLower(n) {
			t.Errorf("command %q is registered with a name dispatch can never match", n)
		}
	}
}

// TestNoInventedCommands checks the table against the names CMake actually
// documents. A command registered here that CMake does not have cannot be
// called by any real CMakeLists.txt, so it is either a typo or a misreading --
// this suite grew an "add_compile_test" that way, which was neither a CMake
// command nor anything at all.
func TestNoInventedCommands(t *testing.T) {
	documented := documentedCommands(t)
	for _, n := range eval.Commands() {
		if documented[n] {
			continue
		}
		if _, allowed := notCMakeCommands[n]; allowed {
			continue
		}
		t.Errorf("%q is registered but is not a CMake command", n)
	}
}

// TestControlFlowIsNotInTheCommandTable states the other half of the split: the
// constructs the evaluator handles must not also appear as commands, or a
// registration would silently shadow the real implementation.
func TestControlFlowIsNotInTheCommandTable(t *testing.T) {
	inTable := map[string]bool{}
	for _, n := range eval.Commands() {
		inTable[n] = true
	}
	for _, n := range controlFlow {
		if inTable[n] {
			t.Errorf("%q is a control-flow construct but is also registered as a command", n)
		}
	}
}

// documentedCommands asks the cmake binary what commands exist. Without one
// installed there is nothing to compare against, so the test skips.
func documentedCommands(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command(realCMake(t), "--help-command-list").Output()
	if err != nil {
		t.Skipf("cannot list cmake commands: %v", err)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set
}
