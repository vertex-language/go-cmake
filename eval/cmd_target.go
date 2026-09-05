package eval

import (
	"context"
	"strings"
)

func init() {
	register("add_executable", cmdAddExecutable)
	register("add_library", cmdAddLibrary)
	register("add_custom_target", cmdAddCustomTarget)
	register("add_custom_command", cmdAddCustomCommand)
	register("add_dependencies", cmdAddDependencies)
	register("target_sources", cmdTargetSources)
	register("target_include_directories", cmdTargetIncludeDirectories)
	register("target_compile_definitions", cmdTargetCompileDefinitions)
	register("target_compile_options", cmdTargetCompileOptions)
	register("target_compile_features", cmdTargetCompileFeatures)
	register("target_link_libraries", cmdTargetLinkLibraries)
	register("target_link_options", cmdTargetLinkOptions)
	register("target_link_directories", cmdTargetLinkDirectories)
	register("target_precompile_headers", cmdNoOp)
	register("set_target_properties", cmdSetTargetProperties)
	register("get_target_property", cmdGetTargetProperty)
	register("set_source_files_properties", cmdSetSourceFilesProperties)
	register("get_source_file_property", cmdGetSourceFileProperty)
	register("add_test", cmdAddTest)
	register("set_tests_properties", cmdSetTestsProperties)
	register("install", cmdInstall)
	register("export", cmdNoOp)
	register("add_compile_test", cmdNoOp)
}

// libraryTypes are the keywords add_library accepts in place of a source file.
var libraryTypes = map[string]bool{
	"STATIC": true, "SHARED": true, "MODULE": true, "OBJECT": true,
	"INTERFACE": true, "UNKNOWN": true,
}

func cmdAddExecutable(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("add_executable called with incorrect number of arguments")
	}
	name := v[0]
	if err := e.checkTargetNameFree("add_executable", name); err != nil {
		return err
	}

	// add_executable(name ALIAS other) and add_executable(name IMPORTED)
	// declare a target that has no sources of its own.
	for i := 1; i < len(v); i++ {
		switch v[i] {
		case "ALIAS":
			if i+1 >= len(v) {
				return e.fatalf("add_executable ALIAS requires a target name")
			}
			e.state.AddTarget(&TargetState{Name: name, Type: "ALIAS", AliasOf: v[i+1]})
			return nil
		case "IMPORTED":
			t := e.newTarget(name, "EXECUTABLE")
			t.Imported = true
			t.GlobalScope = containsStr(v, "GLOBAL")
			e.state.AddTarget(t)
			return nil
		}
	}

	t := e.newTarget(name, "EXECUTABLE")
	for _, a := range v[1:] {
		switch a {
		case "WIN32", "MACOSX_BUNDLE":
			t.SetProperty(a+"_EXECUTABLE", "TRUE")
		case "EXCLUDE_FROM_ALL":
			t.ExcludeAll = true
		default:
			t.Sources = append(t.Sources, e.sourcePath(a))
		}
	}
	e.state.AddTarget(t)
	return nil
}

func cmdAddLibrary(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("add_library called with incorrect number of arguments")
	}
	name := v[0]
	if err := e.checkTargetNameFree("add_library", name); err != nil {
		return err
	}

	for i := 1; i < len(v); i++ {
		if v[i] == "ALIAS" {
			if i+1 >= len(v) {
				return e.fatalf("add_library ALIAS requires a target name")
			}
			e.state.AddTarget(&TargetState{Name: name, Type: "ALIAS", AliasOf: v[i+1]})
			return nil
		}
	}

	// The default library type follows BUILD_SHARED_LIBS, which is the switch
	// that lets one project be built either way without editing it.
	typ := "STATIC"
	if isOn(e.state.GetVar("BUILD_SHARED_LIBS")) {
		typ = "SHARED"
	}
	imported := false
	t := e.newTarget(name, typ)
	for _, a := range v[1:] {
		switch {
		case libraryTypes[a]:
			t.Type = a
		case a == "IMPORTED":
			imported = true
		case a == "GLOBAL":
			t.GlobalScope = true
		case a == "EXCLUDE_FROM_ALL":
			t.ExcludeAll = true
		default:
			t.Sources = append(t.Sources, e.sourcePath(a))
		}
	}
	t.Imported = imported
	e.state.AddTarget(t)
	return nil
}

// checkTargetNameFree reports a duplicate target name. CMake treats this as a
// non-fatal error: it names the directory that already claimed the name and
// carries on, so that the rest of the tree is still checked.
func (e *evaluator) checkTargetNameFree(command, name string) error {
	existing, exists := e.state.Targets[name]
	if !exists {
		return nil
	}
	return e.errorf("%s cannot create target %q because another target with the same\n"+
		"  name already exists.  The existing target is %s created in\n"+
		"  source directory\n  %q."+
		"\n  See documentation for policy CMP0002 for more details.",
		command, name, describeTarget(existing), existing.SourceDir)
}

// describeTarget renders a target the way CMake names it in a diagnostic.
func describeTarget(t *TargetState) string {
	switch t.Type {
	case "EXECUTABLE":
		return "an executable"
	case "STATIC":
		return "a static library"
	case "SHARED":
		return "a shared library"
	case "MODULE":
		return "a module library"
	case "OBJECT":
		return "an object library"
	case "INTERFACE":
		return "an interface library"
	case "UTILITY":
		return "a custom target"
	default:
		return "a target"
	}
}

// newTarget creates a target that has already inherited the directory-level
// include paths, definitions, and options in force where it was declared.
func (e *evaluator) newTarget(name, typ string) *TargetState {
	d := e.state.Dir()
	return &TargetState{
		Name:       name,
		Type:       typ,
		SourceDir:  d.Source,
		BinaryDir:  d.Binary,
		Properties: map[string]string{},
		// A target copies the directory's include paths, options, link
		// directories and link libraries at the moment it is created. It does
		// not copy the directory's compile definitions: those stay a directory
		// property and are combined with the target's own at generate time,
		// which is why get_target_property(COMPILE_DEFINITIONS) comes back
		// NOTFOUND for a target whose only definitions came from
		// add_compile_definitions.
		IncludeDirs: append([]string{}, d.IncludeDirs...),
		CompileOpts: append([]string{}, d.Options...),
		LinkOpts:    append([]string{}, d.LinkOptions...),
		LinkDirs:    append([]string{}, d.LinkDirs...),
		LinkLibs:    append([]string{}, d.LinkLibs...),
	}
}

// sourcePath records a source file reference the way CMake does: verbatim.
// A relative source is resolved later against the target's own SourceDir, so
// that get_target_property(SOURCES) reports what the project wrote rather than
// an absolute path the project never mentioned.
func (e *evaluator) sourcePath(p string) string {
	return slashPath(p)
}

// ResolveSource makes one of a target's sources absolute, which is what a
// generator needs and what the SOURCES property deliberately does not hold.
func (t *TargetState) ResolveSource(p string) string {
	if isAbsolutePath(p) || strings.HasPrefix(p, "$<") {
		return slashPath(p)
	}
	return joinPath(t.SourceDir, p)
}

func cmdAddCustomTarget(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("add_custom_target called with incorrect number of arguments")
	}
	name := v[0]
	t := e.newTarget(name, "UTILITY")
	t.ExcludeAll = true

	var current []string
	keyword := ""
	flush := func() {
		if len(current) > 0 {
			t.Commands = append(t.Commands, current)
			current = nil
		}
	}
	for _, a := range v[1:] {
		switch a {
		case "ALL":
			t.ExcludeAll = false
			continue
		case "COMMAND":
			flush()
			keyword = "COMMAND"
			continue
		case "DEPENDS", "BYPRODUCTS", "WORKING_DIRECTORY", "COMMENT", "SOURCES", "JOB_POOL":
			flush()
			keyword = a
			continue
		case "VERBATIM", "USES_TERMINAL", "COMMAND_EXPAND_LISTS":
			continue
		}
		switch keyword {
		case "COMMAND":
			current = append(current, a)
		case "DEPENDS":
			t.Depends = append(t.Depends, a)
		case "SOURCES":
			t.Sources = append(t.Sources, e.sourcePath(a))
		case "WORKING_DIRECTORY":
			t.SetProperty("WORKING_DIRECTORY", e.state.absPath(a))
		case "COMMENT":
			t.SetProperty("COMMENT", a)
		}
	}
	flush()
	e.state.AddTarget(t)
	return nil
}

func cmdAddCustomCommand(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("add_custom_command called with incorrect number of arguments")
	}

	// The TARGET form attaches to an existing target; the OUTPUT form declares
	// a new build-graph node.
	if v[0] == "TARGET" {
		return e.customCommandOnTarget(v)
	}

	cc := CustomCommand{SourceDir: e.state.Dir().Source, WorkDir: e.state.Dir().Binary}
	var current []string
	keyword := ""
	flush := func() {
		if len(current) > 0 {
			cc.Commands = append(cc.Commands, current)
			current = nil
		}
	}
	for _, a := range v {
		switch a {
		case "OUTPUT", "DEPENDS", "BYPRODUCTS", "IMPLICIT_DEPENDS", "MAIN_DEPENDENCY",
			"WORKING_DIRECTORY", "COMMENT", "DEPFILE", "JOB_POOL":
			flush()
			keyword = a
			continue
		case "COMMAND":
			flush()
			keyword = "COMMAND"
			continue
		case "VERBATIM":
			cc.Verbatim = true
			continue
		case "APPEND", "USES_TERMINAL", "COMMAND_EXPAND_LISTS", "DEPENDS_EXPLICIT_ONLY":
			continue
		}
		switch keyword {
		case "OUTPUT":
			cc.Outputs = append(cc.Outputs, e.outputPath(a))
		case "COMMAND":
			current = append(current, a)
		case "DEPENDS", "MAIN_DEPENDENCY":
			cc.Depends = append(cc.Depends, a)
		case "BYPRODUCTS":
			cc.Byproducts = append(cc.Byproducts, e.outputPath(a))
		case "WORKING_DIRECTORY":
			cc.WorkDir = e.state.absPath(a)
		case "COMMENT":
			cc.Comment = a
		}
	}
	flush()
	if len(cc.Outputs) == 0 {
		return e.fatalf("add_custom_command requires an OUTPUT or a TARGET")
	}
	e.state.CustomCommands = append(e.state.CustomCommands, cc)
	return nil
}

// outputPath resolves a generated file's path, which is relative to the
// current binary directory rather than the source directory.
func (e *evaluator) outputPath(p string) string {
	if isAbsolutePath(p) {
		return slashPath(p)
	}
	return slashPath(joinPath(e.state.Dir().Binary, p))
}

func (e *evaluator) customCommandOnTarget(v []string) error {
	if len(v) < 2 {
		return e.fatalf("add_custom_command TARGET requires a target name")
	}
	t, ok := e.state.Target(v[1])
	if !ok {
		return e.fatalf("add_custom_command TARGET %q is not a target", v[1])
	}
	when := "POST_BUILD"
	var current []string
	keyword := ""
	var commands [][]string
	flush := func() {
		if len(current) > 0 {
			commands = append(commands, current)
			current = nil
		}
	}
	for _, a := range v[2:] {
		switch a {
		case "PRE_BUILD", "PRE_LINK", "POST_BUILD":
			flush()
			when = a
			keyword = ""
			continue
		case "COMMAND":
			flush()
			keyword = "COMMAND"
			continue
		case "BYPRODUCTS", "WORKING_DIRECTORY", "COMMENT", "DEPENDS":
			flush()
			keyword = a
			continue
		case "VERBATIM", "USES_TERMINAL", "COMMAND_EXPAND_LISTS":
			continue
		}
		if keyword == "COMMAND" {
			current = append(current, a)
		}
	}
	flush()
	switch when {
	case "PRE_BUILD":
		t.PreBuild = append(t.PreBuild, commands...)
	case "PRE_LINK":
		t.PreLink = append(t.PreLink, commands...)
	default:
		t.PostBuild = append(t.PostBuild, commands...)
	}
	return nil
}

func cmdAddDependencies(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("add_dependencies called with incorrect number of arguments")
	}
	t, ok := e.state.Target(v[0])
	if !ok {
		return e.fatalf("add_dependencies Cannot add target-level dependencies to non-existent target %q.", v[0])
	}
	t.Depends = append(t.Depends, v[1:]...)
	return nil
}

// ----------------------------------------------------------------------------
// target_* commands
//
// Every one of these has the same shape: a target, then runs of values tagged
// by scope. scopedApply is that shape, written once.

// scopedApply walks a scope-tagged argument list, calling build for values that
// affect this target's own compilation and iface for values a consumer sees.
func scopedApply(v []string, build, iface func(string)) error {
	scope := ""
	for _, a := range v {
		switch a {
		case "PRIVATE", "PUBLIC", "INTERFACE":
			scope = a
			continue
		}
		switch scope {
		case "PRIVATE":
			build(a)
		case "INTERFACE":
			iface(a)
		case "PUBLIC":
			build(a)
			iface(a)
		default:
			// An untagged list is the old signature, which is build-side only.
			build(a)
		}
	}
	return nil
}

// targetArg resolves the target named by a target_* command.
func (e *evaluator) targetArg(command string, v []string) (*TargetState, []string, error) {
	if len(v) < 1 {
		return nil, nil, e.fatalf("%s called with incorrect number of arguments", command)
	}
	t, ok := e.state.Target(v[0])
	if !ok {
		return nil, nil, e.fatalf("%s called with non-compilable target type", command)
	}
	return t, v[1:], nil
}

func cmdTargetSources(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_sources", Args(args))
	if err != nil {
		return err
	}
	// FILE_SET declarations describe headers rather than compiled sources and
	// are recorded as properties, not appended to the source list.
	if i := indexOf(rest, "FILE_SET"); i >= 0 {
		t.SetProperty("INTERFACE_HEADER_SETS", JoinList(rest[i:]))
		rest = rest[:i]
	}
	return scopedApply(rest,
		func(s string) { t.Sources = append(t.Sources, e.sourcePath(s)) },
		func(s string) { t.Sources = append(t.Sources, e.sourcePath(s)) })
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func cmdTargetIncludeDirectories(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_include_directories", Args(args))
	if err != nil {
		return err
	}
	var filtered []string
	for _, a := range rest {
		if a == "SYSTEM" || a == "BEFORE" || a == "AFTER" {
			continue
		}
		filtered = append(filtered, a)
	}
	abs := func(p string) string {
		if isAbsolutePath(p) || strings.HasPrefix(p, "$<") {
			return slashPath(p)
		}
		return slashPath(joinPath(e.state.Dir().Source, p))
	}
	return scopedApply(filtered,
		func(s string) { t.IncludeDirs = append(t.IncludeDirs, abs(s)) },
		func(s string) { t.IfaceIncludeDirs = append(t.IfaceIncludeDirs, abs(s)) })
}

func cmdTargetCompileDefinitions(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_compile_definitions", Args(args))
	if err != nil {
		return err
	}
	// A leading -D is stripped: CMake adds the flag itself per compiler.
	clean := func(s string) string { return strings.TrimPrefix(s, "-D") }
	return scopedApply(rest,
		func(s string) { t.Defines = append(t.Defines, clean(s)) },
		func(s string) { t.IfaceDefines = append(t.IfaceDefines, clean(s)) })
}

func cmdTargetCompileOptions(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_compile_options", Args(args))
	if err != nil {
		return err
	}
	if len(rest) > 0 && rest[0] == "BEFORE" {
		rest = rest[1:]
	}
	return scopedApply(rest,
		func(s string) { t.CompileOpts = append(t.CompileOpts, s) },
		func(s string) { t.IfaceCompileOpts = append(t.IfaceCompileOpts, s) })
}

func cmdTargetCompileFeatures(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_compile_features", Args(args))
	if err != nil {
		return err
	}
	return scopedApply(rest,
		func(s string) { t.CompileFeats = append(t.CompileFeats, s) },
		func(s string) { t.IfaceCompileFeats = append(t.IfaceCompileFeats, s) })
}

func cmdTargetLinkLibraries(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_link_libraries", Args(args))
	if err != nil {
		return err
	}
	// The plain signature target_link_libraries(t a b c) is build-side and, for
	// a static library, also interface-side: a consumer must link a's
	// dependencies transitively because a static archive carries no link list.
	tagged := false
	for _, a := range rest {
		if a == "PRIVATE" || a == "PUBLIC" || a == "INTERFACE" {
			tagged = true
			break
		}
	}
	if !tagged {
		for _, a := range rest {
			if a == "debug" || a == "optimized" || a == "general" {
				continue
			}
			t.LinkLibs = append(t.LinkLibs, a)
			t.IfaceLinkLibs = append(t.IfaceLinkLibs, a)
		}
		return nil
	}
	return scopedApply(rest,
		func(s string) { t.LinkLibs = append(t.LinkLibs, s) },
		func(s string) { t.IfaceLinkLibs = append(t.IfaceLinkLibs, s) })
}

func cmdTargetLinkOptions(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_link_options", Args(args))
	if err != nil {
		return err
	}
	if len(rest) > 0 && rest[0] == "BEFORE" {
		rest = rest[1:]
	}
	return scopedApply(rest,
		func(s string) { t.LinkOpts = append(t.LinkOpts, s) },
		func(s string) { t.IfaceLinkOpts = append(t.IfaceLinkOpts, s) })
}

func cmdTargetLinkDirectories(_ context.Context, e *evaluator, args []Arg) error {
	t, rest, err := e.targetArg("target_link_directories", Args(args))
	if err != nil {
		return err
	}
	if len(rest) > 0 && (rest[0] == "BEFORE" || rest[0] == "AFTER") {
		rest = rest[1:]
	}
	return scopedApply(rest,
		func(s string) { t.LinkDirs = append(t.LinkDirs, e.state.absPath(s)) },
		func(s string) { t.IfaceLinkDirs = append(t.IfaceLinkDirs, e.state.absPath(s)) })
}

// ----------------------------------------------------------------------------
// target and source properties

func cmdSetTargetProperties(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	i := indexOf(v, "PROPERTIES")
	if i < 0 {
		return e.fatalf("set_target_properties called without a PROPERTIES keyword")
	}
	names := v[:i]
	props := v[i+1:]
	if len(props)%2 != 0 {
		return e.fatalf("set_target_properties called with incorrect number of arguments")
	}
	for _, n := range names {
		t, ok := e.state.Target(n)
		if !ok {
			return e.fatalf("set_target_properties Can not find target to add properties to: %s", n)
		}
		for j := 0; j+1 < len(props); j += 2 {
			t.SetProperty(props[j], props[j+1])
		}
	}
	return nil
}

func cmdGetTargetProperty(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 3 {
		return e.fatalf("get_target_property called with incorrect number of arguments")
	}
	out, name, prop := v[0], v[1], v[2]
	t, ok := e.state.Target(name)
	if !ok {
		return e.fatalf("get_target_property() called with non-existent target %q.", name)
	}
	val, found := t.Property(prop)
	if !found {
		// An unset property yields <var>-NOTFOUND, which tests false.
		e.state.SetVar(out, out+"-NOTFOUND")
		return nil
	}
	e.state.SetVar(out, val)
	return nil
}

func cmdSetSourceFilesProperties(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	i := indexOf(v, "PROPERTIES")
	if i < 0 {
		return e.fatalf("set_source_files_properties called without a PROPERTIES keyword")
	}
	for _, f := range v[:i] {
		src := e.sourcePath(f)
		props := v[i+1:]
		for j := 0; j+1 < len(props); j += 2 {
			e.state.Properties[propertyKey("SOURCE", src, props[j])] = props[j+1]
		}
	}
	return nil
}

func cmdGetSourceFileProperty(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 3 {
		return e.fatalf("get_source_file_property called with incorrect number of arguments")
	}
	out, file, prop := v[0], v[1], v[len(v)-1]
	val, ok := e.state.Properties[propertyKey("SOURCE", e.sourcePath(file), prop)]
	if !ok {
		e.state.SetVar(out, "NOTFOUND")
		return nil
	}
	e.state.SetVar(out, val)
	return nil
}

// ----------------------------------------------------------------------------
// tests and install

func cmdAddTest(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("add_test called with incorrect number of arguments")
	}
	// The modern form is add_test(NAME n COMMAND ...); the old positional form
	// is add_test(name exe args...).
	if v[0] == "NAME" {
		if len(v) < 2 {
			return e.fatalf("add_test requires a name after NAME")
		}
		entry := TestEntry{Name: v[1], WorkDir: e.state.Dir().Binary}
		keyword := ""
		for _, a := range v[2:] {
			switch a {
			case "COMMAND", "CONFIGURATIONS", "WORKING_DIRECTORY", "COMMAND_EXPAND_LISTS":
				keyword = a
				continue
			}
			switch keyword {
			case "COMMAND":
				entry.Command = append(entry.Command, a)
			case "WORKING_DIRECTORY":
				entry.WorkDir = e.state.absPath(a)
			case "CONFIGURATIONS":
				entry.Configurations = append(entry.Configurations, a)
			}
		}
		e.state.Tests = append(e.state.Tests, entry)
		return nil
	}
	e.state.Tests = append(e.state.Tests, TestEntry{
		Name:    v[0],
		Command: v[1:],
		WorkDir: e.state.Dir().Binary,
	})
	return nil
}

func cmdSetTestsProperties(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	i := indexOf(v, "PROPERTIES")
	if i < 0 {
		return e.fatalf("set_tests_properties called without a PROPERTIES keyword")
	}
	for _, name := range v[:i] {
		props := v[i+1:]
		for j := 0; j+1 < len(props); j += 2 {
			e.state.Properties[propertyKey("TEST", name, props[j])] = props[j+1]
		}
	}
	return nil
}

func cmdInstall(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("install called with incorrect number of arguments")
	}
	rule := InstallRule{Kind: v[0], SourceDir: e.state.Dir().Source}
	keyword := "ITEMS"
	switch v[0] {
	case "TARGETS", "FILES", "PROGRAMS", "DIRECTORY", "SCRIPT", "CODE", "EXPORT", "IMPORTED_RUNTIME_ARTIFACTS":
		v = v[1:]
	default:
		return e.fatalf("install does not recognize mode %s", rule.Kind)
	}
	for _, a := range v {
		switch a {
		case "DESTINATION", "COMPONENT", "PERMISSIONS", "RENAME", "CONFIGURATIONS",
			"ARCHIVE", "LIBRARY", "RUNTIME", "OBJECTS", "FRAMEWORK", "BUNDLE",
			"PUBLIC_HEADER", "PRIVATE_HEADER", "RESOURCE", "FILE_SET", "NAMESPACE",
			"INCLUDES", "PATTERN", "REGEX", "EXCLUDE", "TYPE", "EXPORT":
			keyword = a
			continue
		case "OPTIONAL":
			rule.Optional = true
			continue
		case "USE_SOURCE_PERMISSIONS", "FILES_MATCHING", "NAMELINK_SKIP", "NAMELINK_ONLY",
			"MESSAGE_NEVER", "EXCLUDE_FROM_ALL":
			continue
		}
		switch keyword {
		case "ITEMS":
			rule.Items = append(rule.Items, a)
		case "DESTINATION":
			rule.Destination = a
			keyword = ""
		case "COMPONENT":
			rule.Component = a
			keyword = ""
		case "RENAME":
			rule.Rename = a
			keyword = ""
		case "PERMISSIONS":
			rule.Permissions = append(rule.Permissions, a)
		case "TYPE":
			// install(FILES ... TYPE BIN) picks a standard destination instead
			// of naming one, so the type stands in for DESTINATION.
			if rule.Destination == "" {
				rule.Destination = standardDestination(a)
			}
			keyword = ""
		}
	}
	e.state.InstallRules = append(e.state.InstallRules, rule)
	return nil
}

// standardDestination maps install(TYPE <t>) to the GNU-style directory CMake
// would pick for it.
func standardDestination(typ string) string {
	switch typ {
	case "BIN":
		return "bin"
	case "SBIN":
		return "sbin"
	case "LIB":
		return "lib"
	case "INCLUDE":
		return "include"
	case "SYSCONF":
		return "etc"
	case "DATA":
		return "share"
	case "DOC":
		return "share/doc"
	case "MAN":
		return "share/man"
	default:
		return strings.ToLower(typ)
	}
}
