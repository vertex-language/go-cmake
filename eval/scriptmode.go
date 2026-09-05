package eval

// Script mode has no project.
//
// `cmake -P` evaluates the language with no source tree, no build tree, and
// no targets, so a command that declares part of a project has nothing to
// declare it into. CMake refuses those rather than letting them appear to
// work, and this list is the same one: a script that called add_library and
// got silence would be harder to debug than one that was told plainly.
//
// It is the set cmake itself reports, obtained by asking it -- not by reasoning
// about which commands ought to need a project. The two do not quite agree:
// add_compile_options is refused and add_link_options is not, which is a quirk
// rather than a rule, and matching the quirk is the point.
var projectOnlyCommands = map[string]bool{
	"add_compile_options": true, "add_custom_command": true, "add_custom_target": true,
	"add_definitions": true, "add_dependencies": true, "add_executable": true,
	"add_library": true, "add_subdirectory": true, "add_test": true,
	"aux_source_directory": true, "build_command": true, "create_test_sourcelist": true,
	"define_property": true, "enable_language": true, "enable_testing": true,
	"export": true, "export_library_dependencies": true, "fltk_wrap_ui": true,
	"get_source_file_property": true, "get_target_property": true, "get_test_property": true,
	"include_directories": true, "include_external_msproject": true, "include_regular_expression": true,
	"install": true, "link_directories": true, "link_libraries": true,
	"load_command": true, "output_required_files": true, "project": true,
	"qt_wrap_cpp": true, "qt_wrap_ui": true, "remove_definitions": true,
	"set_source_files_properties": true, "set_target_properties": true, "set_tests_properties": true,
	"source_group": true, "subdir_depends": true, "target_compile_definitions": true,
	"target_compile_features": true, "target_compile_options": true, "target_include_directories": true,
	"target_link_libraries": true, "target_sources": true, "try_compile": true,
	"try_run": true, "utility_source": true, "variable_requires": true,
}
