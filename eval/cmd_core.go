package eval

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

func init() {
	register("set", cmdSet)
	register("unset", cmdUnset)
	register("message", cmdMessage)
	register("option", cmdOption)
	register("cmake_minimum_required", cmdCMakeMinimumRequired)
	register("cmake_policy", cmdCMakePolicy)
	register("project", cmdProject)
	register("include", cmdInclude)
	register("include_guard", cmdIncludeGuard)
	register("add_subdirectory", cmdAddSubdirectory)
	register("math", cmdMath)
	register("mark_as_advanced", cmdMarkAsAdvanced)
	register("set_property", cmdSetProperty)
	register("get_property", cmdGetProperty)
	register("define_property", cmdDefineProperty)
	register("set_directory_properties", cmdSetDirectoryProperties)
	register("get_directory_property", cmdGetDirectoryProperty)
	register("get_cmake_property", cmdGetCMakeProperty)
	register("enable_language", cmdEnableLanguage)
	register("cmake_parse_arguments", cmdParseArguments)
	register("cmake_host_system_information", cmdHostSystemInformation)
	register("separate_arguments", cmdSeparateArguments)
	register("get_filename_component", cmdGetFilenameComponent)
	register("cmake_path", cmdCMakePath)
	register("site_name", cmdSiteName)
	register("variable_watch", cmdNoOp)
	register("include_regular_expression", cmdNoOp)
	register("enable_testing", cmdEnableTesting)
	register("add_definitions", cmdAddDefinitions)
	register("add_compile_definitions", cmdAddCompileDefinitions)
	register("add_compile_options", cmdAddCompileOptions)
	register("add_link_options", cmdAddLinkOptions)
	register("include_directories", cmdIncludeDirectories)
	register("link_directories", cmdLinkDirectories)
	register("link_libraries", cmdLinkLibraries)
	register("remove_definitions", cmdRemoveDefinitions)
	register("cmake_language", cmdCMakeLanguage)
}

// cmdNoOp accepts and ignores a command whose effect is outside this
// implementation's model.
func cmdNoOp(context.Context, *evaluator, []Arg) error { return nil }

// ----------------------------------------------------------------------------
// set / unset

func cmdSet(_ context.Context, e *evaluator, args []Arg) error {
	if len(args) == 0 {
		return e.fatalf("set called with incorrect number of arguments")
	}
	name := args[0].Val
	vals := args[1:]

	// set(ENV{VAR} value)
	if strings.HasPrefix(name, "ENV{") && strings.HasSuffix(name, "}") {
		key := name[4 : len(name)-1]
		if len(vals) == 0 {
			delete(e.state.Env, key)
		} else {
			e.state.Env[key] = strings.Join(Args(vals), "")
		}
		return nil
	}

	// Look for the CACHE keyword: set(VAR value... CACHE TYPE DOC [FORCE])
	for i, a := range vals {
		if a.Quoted || a.Val != "CACHE" {
			continue
		}
		rest := Args(vals[i+1:])
		if len(rest) < 2 {
			return e.fatalf("set called with an incorrect number of arguments for CACHE")
		}
		typ := parseCacheType(rest[0])
		doc := rest[1]
		force := len(rest) > 2 && rest[2] == "FORCE"
		value := JoinList(Args(vals[:i]))
		// An INTERNAL entry is always overwritten: it is CMake's own storage,
		// not a knob the user is expected to have edited.
		if typ == CacheInternal {
			force = true
		}
		e.state.Cache.Set(name, value, typ, doc, force)
		// A cache entry is shadowed by a normal variable of the same name, so
		// creating one has to clear the shadow or the new value is invisible.
		if force || !e.state.hadCacheEntry(name) {
			e.state.Current.Unset(name)
		}
		return nil
	}

	// set(VAR value... PARENT_SCOPE)
	if n := len(vals); n > 0 && !vals[n-1].Quoted && vals[n-1].Val == "PARENT_SCOPE" {
		vals = vals[:n-1]
		if !e.state.Current.HasParent() {
			// Nothing is wrong with the syntax, but the write goes nowhere, and
			// silence here has cost many people an afternoon.
			e.state.log("AUTHOR_WARNING", sprintf("Cannot set %q: current scope has no parent.", name))
			return nil
		}
		if len(vals) == 0 {
			e.state.Current.UnsetParent(name)
		} else {
			e.state.Current.SetParent(name, JoinList(Args(vals)))
		}
		return nil
	}

	if len(vals) == 0 {
		e.state.Current.Unset(name)
		return nil
	}
	e.state.SetVar(name, JoinList(Args(vals)))
	return nil
}

// hadCacheEntry reports whether the cache already held name before the write
// that is in progress. The Cache records this for the benefit of set().
func (s *State) hadCacheEntry(name string) bool {
	_, ok := s.Cache.Get(name)
	return ok
}

func parseCacheType(s string) CacheEntryType {
	switch strings.ToUpper(s) {
	case "BOOL":
		return CacheBool
	case "PATH":
		return CachePath
	case "FILEPATH":
		return CacheFilepath
	case "INTERNAL":
		return CacheInternal
	case "STATIC":
		return CacheStatic
	default:
		return CacheString
	}
}

func cmdUnset(_ context.Context, e *evaluator, args []Arg) error {
	if len(args) == 0 {
		return e.fatalf("unset called with incorrect number of arguments")
	}
	name := args[0].Val
	rest := Args(args[1:])

	if strings.HasPrefix(name, "ENV{") && strings.HasSuffix(name, "}") {
		delete(e.state.Env, name[4:len(name)-1])
		return nil
	}
	if containsStr(rest, "PARENT_SCOPE") {
		e.state.Current.UnsetParent(name)
		return nil
	}
	if containsStr(rest, "CACHE") {
		e.state.Cache.Unset(name)
		return nil
	}
	// unset() removes only the normal variable. A cache entry of the same name
	// survives and becomes visible again, which is how a project temporarily
	// shadows a cached value and then restores it.
	e.state.Current.Unset(name)
	return nil
}

// ----------------------------------------------------------------------------
// message

func cmdMessage(_ context.Context, e *evaluator, args []Arg) error {
	if len(args) == 0 {
		return nil
	}
	mode := ""
	vals := Args(args)
	switch vals[0] {
	case "STATUS", "WARNING", "AUTHOR_WARNING", "SEND_ERROR", "FATAL_ERROR",
		"DEPRECATION", "NOTICE", "VERBOSE", "DEBUG", "TRACE":
		mode = vals[0]
		vals = vals[1:]
	case "CHECK_START", "CHECK_PASS", "CHECK_FAIL":
		mode = "STATUS"
		vals = vals[1:]
	case "CONFIGURE_LOG":
		return nil
	}
	// message() concatenates its arguments with no separator; a list argument
	// therefore prints with its semicolons intact.
	text := strings.Join(vals, "")
	if mode == "FATAL_ERROR" {
		return e.fatalf("%s", text)
	}
	e.state.log(mode, text)
	if mode == "SEND_ERROR" {
		e.state.Errors = append(e.state.Errors, text)
	}
	return nil
}

// ----------------------------------------------------------------------------
// option

func cmdOption(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("option called with incorrect number of arguments")
	}
	name := vals[0]
	doc := ""
	if len(vals) > 1 {
		doc = vals[1]
	}
	def := "OFF"
	if len(vals) > 2 {
		if isOn(vals[2]) {
			def = "ON"
		}
	}
	// An existing normal variable of the same name takes precedence, which is
	// how a parent project overrides a subproject's option before adding it.
	if v, ok := e.state.Current.Get(name); ok {
		if _, cached := e.state.Cache.Get(name); !cached {
			e.state.Cache.Set(name, v, CacheBool, doc, true)
			return nil
		}
	}
	e.state.Cache.Set(name, def, CacheBool, doc, false)
	return nil
}

// ----------------------------------------------------------------------------
// cmake_minimum_required / cmake_policy

func cmdCMakeMinimumRequired(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	for i := 0; i < len(vals); i++ {
		if vals[i] != "VERSION" || i+1 >= len(vals) {
			continue
		}
		spec := vals[i+1]
		// The spec may be a range, "3.16...3.28": the lower bound is the
		// minimum required and the upper bound is the newest policy set to
		// apply, so a project can require an old CMake yet opt into new
		// behaviour when a newer one is running.
		low, high := spec, ""
		if k := strings.Index(spec, "..."); k >= 0 {
			low, high = spec[:k], spec[k+3:]
		}
		running := e.state.GetVar("CMAKE_VERSION")
		if CompareVersions(running, low) < 0 {
			return e.fatalf("CMake %s or higher is required.  You are running version %s", low, running)
		}
		apply := low
		if high != "" && CompareVersions(running, high) >= 0 {
			apply = high
		} else if high != "" {
			apply = running
		}
		e.state.SetPolicyVersion(apply)
		e.state.SetVar("CMAKE_MINIMUM_REQUIRED_VERSION", low)
	}
	return nil
}

func cmdCMakePolicy(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return nil
	}
	switch vals[0] {
	case "VERSION":
		if len(vals) > 1 {
			spec := vals[1]
			if k := strings.Index(spec, "..."); k >= 0 {
				spec = spec[:k]
			}
			e.state.SetPolicyVersion(spec)
		}
	case "SET":
		if len(vals) < 3 {
			return e.fatalf("cmake_policy SET requires a policy and a value")
		}
		if !knownPolicy(vals[1]) {
			return e.fatalf("Policy %q is not known to this version of CMake.", vals[1])
		}
		if vals[2] == "OLD" {
			available, intro := OldBehaviorAvailable(vals[1])
			if !available {
				return e.fatalf("Policy %s may not be set to OLD behavior because this version of CMake\n"+
					"  no longer supports it.  The policy was introduced in CMake version %s.0,\n"+
					"  and use of NEW behavior is now required.\n"+
					"\n"+
					"  Please either update your CMakeLists.txt files to conform to the new\n"+
					"  behavior or use an older version of CMake that still supports the old\n"+
					"  behavior.  Run cmake --help-policy %s for more information.",
					vals[1], intro, vals[1])
			}
			e.state.log("DEPRECATION", sprintf(
				"The OLD behavior for policy %s will be removed from a future version\n"+
					"  of CMake.\n"+
					"\n"+
					"  The cmake_policy command may be used to set the policy to NEW behavior for\n"+
					"  this third-party project, or the CMake variable CMAKE_POLICY_DEFAULT_%s may\n"+
					"  be set to NEW to affect all projects.", vals[1], vals[1]))
		}
		e.state.PolicySet(vals[1], vals[2])
	case "GET":
		if len(vals) < 3 {
			return e.fatalf("cmake_policy GET requires a policy and an output variable")
		}
		e.state.SetVar(vals[2], e.state.PolicyGet(vals[1]))
	case "PUSH":
		e.state.PushPolicyScope()
	case "POP":
		e.state.PopPolicyScope()
	}
	return nil
}

// ----------------------------------------------------------------------------
// project

func cmdProject(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("project called with incorrect number of arguments")
	}
	name := vals[0]
	e.state.SetVar("PROJECT_NAME", name)
	dir := e.state.Dir()
	// CMAKE_PROJECT_NAME names the top-level project for the whole tree. A
	// subproject added with add_subdirectory sets PROJECT_NAME but must leave
	// this one alone: it is how a library tells whether it is being built
	// standalone or as somebody else's dependency.
	topLevel := dir == e.state.RootDir()
	if topLevel {
		e.state.SetVar("CMAKE_PROJECT_NAME", name)
	}
	e.state.SetVar("PROJECT_SOURCE_DIR", dir.Source)
	e.state.SetVar("PROJECT_BINARY_DIR", dir.Binary)
	e.state.SetVar(name+"_SOURCE_DIR", dir.Source)
	e.state.SetVar(name+"_BINARY_DIR", dir.Binary)
	if topLevel {
		e.state.SetVar("CMAKE_PROJECT_SOURCE_DIR", dir.Source)
		e.state.SetVar("CMAKE_PROJECT_BINARY_DIR", dir.Binary)
	}

	// project(name [VERSION v] [DESCRIPTION d] [HOMEPAGE_URL u] [LANGUAGES l...])
	keyword := ""
	var languages []string
	for _, v := range vals[1:] {
		switch v {
		case "VERSION", "DESCRIPTION", "HOMEPAGE_URL", "LANGUAGES":
			keyword = v
			continue
		}
		switch keyword {
		case "VERSION":
			setProjectVersion(e.state, name, v, topLevel)
			keyword = ""
		case "DESCRIPTION":
			e.state.SetVar("PROJECT_DESCRIPTION", v)
			e.state.SetVar(name+"_DESCRIPTION", v)
			keyword = ""
		case "HOMEPAGE_URL":
			e.state.SetVar("PROJECT_HOMEPAGE_URL", v)
			e.state.SetVar(name+"_HOMEPAGE_URL", v)
			keyword = ""
		case "LANGUAGES":
			languages = append(languages, v)
		default:
			// The short form project(name C CXX) lists languages positionally.
			languages = append(languages, v)
		}
	}
	if len(languages) == 0 {
		languages = []string{"C", "CXX"}
	}
	for _, l := range languages {
		if l == "NONE" {
			continue
		}
		e.state.Languages[l] = true
	}
	return nil
}

// setProjectVersion publishes the five variables CMake derives from a version.
func setProjectVersion(s *State, project, version string, topLevel bool) {
	parts := versionComponents(version)
	get := func(i int) string {
		if i < len(parts) {
			return strconv.FormatInt(parts[i], 10)
		}
		return ""
	}
	prefixes := []string{"PROJECT", project}
	if topLevel {
		prefixes = append(prefixes, "CMAKE_PROJECT")
	}
	for _, prefix := range prefixes {
		s.SetVar(prefix+"_VERSION", version)
		s.SetVar(prefix+"_VERSION_MAJOR", get(0))
		s.SetVar(prefix+"_VERSION_MINOR", get(1))
		s.SetVar(prefix+"_VERSION_PATCH", get(2))
		s.SetVar(prefix+"_VERSION_TWEAK", get(3))
	}
}

// ----------------------------------------------------------------------------
// include / add_subdirectory

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

// ----------------------------------------------------------------------------
// math

func cmdMath(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 3 || vals[0] != "EXPR" {
		return e.fatalf("math called with incorrect number of arguments")
	}
	out := vals[1]
	result, err := evalMathExpr(vals[2])
	if err != nil {
		return e.fatalf("math cannot evaluate the expression: %q: %v.", vals[2], err)
	}
	format := "DECIMAL"
	for i := 3; i < len(vals)-1; i++ {
		if vals[i] == "OUTPUT_FORMAT" {
			format = vals[i+1]
		}
	}
	if format == "HEXADECIMAL" {
		e.state.SetVar(out, "0x"+strconv.FormatUint(uint64(result), 16))
	} else {
		e.state.SetVar(out, strconv.FormatInt(result, 10))
	}
	return nil
}

// ----------------------------------------------------------------------------
// properties

func cmdMarkAsAdvanced(_ context.Context, e *evaluator, args []Arg) error {
	for _, a := range Args(args) {
		if a == "CLEAR" || a == "FORCE" {
			continue
		}
		if entry, ok := e.state.Cache.Get(a); ok {
			entry.Advanced = true
		}
	}
	return nil
}

func cmdDefineProperty(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	for i := 0; i+1 < len(vals); i++ {
		if vals[i] == "PROPERTY" {
			e.state.DefinedProps[vals[i+1]] = true
			return nil
		}
	}
	return nil
}

// propertyKey builds the key under which a scoped property is stored.
func propertyKey(scope, target, name string) string {
	return scope + ":" + target + ":" + name
}

func cmdSetProperty(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("set_property called with incorrect number of arguments")
	}
	scope := vals[0]
	i := 1
	var names []string
	for ; i < len(vals); i++ {
		if vals[i] == "APPEND" || vals[i] == "APPEND_STRING" || vals[i] == "PROPERTY" {
			break
		}
		names = append(names, vals[i])
	}
	appendMode, appendString := false, false
	for ; i < len(vals); i++ {
		switch vals[i] {
		case "APPEND":
			appendMode = true
		case "APPEND_STRING":
			appendString = true
		case "PROPERTY":
			i++
			goto property
		}
	}
	return e.fatalf("set_property called without a PROPERTY keyword")

property:
	if i >= len(vals) {
		return e.fatalf("set_property called without a property name")
	}
	prop := vals[i]
	value := JoinList(vals[i+1:])

	apply := func(get func() string, set func(string)) {
		switch {
		case appendString:
			set(get() + value)
		case appendMode:
			old := get()
			if old == "" {
				set(value)
			} else if value != "" {
				set(old + ";" + value)
			}
		default:
			set(value)
		}
	}

	switch scope {
	case "GLOBAL":
		key := propertyKey("GLOBAL", "", prop)
		apply(func() string { return e.state.Properties[key] },
			func(v string) { e.state.Properties[key] = v })
	case "DIRECTORY":
		dir := e.state.Dir()
		if len(names) > 0 {
			dir = e.state.findDir(e.state.absPath(names[0]))
			if dir == nil {
				return e.fatalf("set_property DIRECTORY given unknown directory %q", names[0])
			}
		}
		apply(func() string { return dir.Properties[prop] },
			func(v string) { dir.Properties[prop] = v })
	case "TARGET":
		for _, n := range names {
			t, ok := e.state.Target(n)
			if !ok {
				return e.fatalf("set_property could not find TARGET %s.  Perhaps it has not yet been created.", n)
			}
			apply(func() string { v, _ := t.Property(prop); return v },
				func(v string) { t.SetProperty(prop, v) })
		}
	case "SOURCE":
		for _, n := range names {
			key := propertyKey("SOURCE", e.state.absPath(n), prop)
			apply(func() string { return e.state.Properties[key] },
				func(v string) { e.state.Properties[key] = v })
		}
	case "TEST", "CACHE", "INSTALL":
		for _, n := range names {
			key := propertyKey(scope, n, prop)
			apply(func() string { return e.state.Properties[key] },
				func(v string) { e.state.Properties[key] = v })
		}
	default:
		return e.fatalf("set_property given invalid scope %s", scope)
	}
	return nil
}

func cmdGetProperty(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 2 {
		return e.fatalf("get_property called with incorrect number of arguments")
	}
	out := vals[0]
	scope := vals[1]
	i := 2
	name := ""
	switch scope {
	case "TARGET", "SOURCE", "TEST", "CACHE", "INSTALL", "DIRECTORY":
		if scope != "DIRECTORY" || (i < len(vals) && vals[i] != "PROPERTY") {
			if i < len(vals) {
				name = vals[i]
				i++
			}
		}
	case "GLOBAL", "VARIABLE":
	default:
		return e.fatalf("get_property given invalid scope %s", scope)
	}

	prop := ""
	kind := "" // "", SET, DEFINED, BRIEF_DOCS, FULL_DOCS
	for ; i < len(vals); i++ {
		switch vals[i] {
		case "PROPERTY":
			if i+1 < len(vals) {
				prop = vals[i+1]
				i++
			}
		case "SET", "DEFINED", "BRIEF_DOCS", "FULL_DOCS":
			kind = vals[i]
		}
	}

	value, found := e.lookupProperty(scope, name, prop)
	switch kind {
	case "SET":
		e.state.SetVar(out, boolDigit(found))
	case "DEFINED":
		e.state.SetVar(out, boolDigit(e.state.DefinedProps[prop]))
	case "BRIEF_DOCS", "FULL_DOCS":
		e.state.SetVar(out, "NOTFOUND")
	default:
		e.state.SetVar(out, value)
	}
	return nil
}

// lookupProperty resolves a property in one of CMake's property scopes.
func (e *evaluator) lookupProperty(scope, name, prop string) (string, bool) {
	switch scope {
	case "GLOBAL":
		v, ok := e.state.Properties[propertyKey("GLOBAL", "", prop)]
		return v, ok
	case "VARIABLE":
		v, ok := e.state.Current.Get(prop)
		return v, ok
	case "TARGET":
		t, ok := e.state.Target(name)
		if !ok {
			return "", false
		}
		return t.Property(prop)
	case "DIRECTORY":
		dir := e.state.Dir()
		if name != "" {
			dir = e.state.findDir(e.state.absPath(name))
		}
		if dir == nil {
			return "", false
		}
		v, ok := dir.Properties[prop]
		return v, ok
	case "SOURCE":
		v, ok := e.state.Properties[propertyKey("SOURCE", e.state.absPath(name), prop)]
		return v, ok
	default:
		v, ok := e.state.Properties[propertyKey(scope, name, prop)]
		return v, ok
	}
}

// findDir locates a directory in the tree by its source path.
func (s *State) findDir(source string) *Directory {
	for _, d := range s.AllDirs {
		if d.Source == source {
			return d
		}
	}
	return nil
}

func cmdSetDirectoryProperties(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 || vals[0] != "PROPERTIES" {
		return e.fatalf("set_directory_properties called with incorrect arguments")
	}
	dir := e.state.Dir()
	for i := 1; i+1 < len(vals); i += 2 {
		dir.Properties[vals[i]] = vals[i+1]
	}
	return nil
}

func cmdGetDirectoryProperty(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 2 {
		return e.fatalf("get_directory_property called with incorrect number of arguments")
	}
	out := vals[0]
	i := 1
	dir := e.state.Dir()
	if vals[1] == "DIRECTORY" && len(vals) > 2 {
		dir = e.state.findDir(e.state.absPath(vals[2]))
		i = 3
	}
	if dir == nil || i >= len(vals) {
		e.state.SetVar(out, "")
		return nil
	}
	if vals[i] == "DEFINITION" && i+1 < len(vals) {
		v, _ := dir.Scope.Get(vals[i+1])
		e.state.SetVar(out, v)
		return nil
	}
	e.state.SetVar(out, dir.Properties[vals[i]])
	return nil
}

func cmdGetCMakeProperty(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 2 {
		return e.fatalf("get_cmake_property called with incorrect number of arguments")
	}
	out, prop := vals[0], vals[1]
	switch prop {
	case "VARIABLES":
		e.state.SetVar(out, JoinList(e.state.Current.AllNames()))
	case "CACHE_VARIABLES":
		e.state.SetVar(out, JoinList(e.state.Cache.Names()))
	case "COMMANDS":
		names := make([]string, 0, len(commands))
		for n := range commands {
			names = append(names, n)
		}
		sort.Strings(names)
		e.state.SetVar(out, JoinList(names))
	case "MACROS":
		names := make([]string, 0, len(e.state.Macros))
		for n := range e.state.Macros {
			names = append(names, n)
		}
		sort.Strings(names)
		e.state.SetVar(out, JoinList(names))
	default:
		v, ok := e.state.Properties[propertyKey("GLOBAL", "", prop)]
		if !ok {
			v = "NOTFOUND"
		}
		e.state.SetVar(out, v)
	}
	return nil
}

// ----------------------------------------------------------------------------
// languages, testing, directory-level flags

func cmdEnableLanguage(_ context.Context, e *evaluator, args []Arg) error {
	for _, l := range Args(args) {
		if l == "OPTIONAL" {
			continue
		}
		e.state.Languages[l] = true
	}
	return nil
}

func cmdEnableTesting(_ context.Context, e *evaluator, args []Arg) error {
	e.state.TestingEnabled = true
	return nil
}

func cmdAddDefinitions(_ context.Context, e *evaluator, args []Arg) error {
	dir := e.state.Dir()
	for _, a := range Args(args) {
		// add_definitions historically takes -D flags; the -D is stripped so
		// the value can be stored the same way add_compile_definitions does.
		if strings.HasPrefix(a, "-D") || strings.HasPrefix(a, "/D") {
			dir.Definitions = append(dir.Definitions, a[2:])
		} else {
			dir.Options = append(dir.Options, a)
		}
	}
	return nil
}

func cmdRemoveDefinitions(_ context.Context, e *evaluator, args []Arg) error {
	dir := e.state.Dir()
	for _, a := range Args(args) {
		want := strings.TrimPrefix(strings.TrimPrefix(a, "-D"), "/D")
		out := dir.Definitions[:0]
		for _, d := range dir.Definitions {
			if d != want {
				out = append(out, d)
			}
		}
		dir.Definitions = out
	}
	return nil
}

func cmdAddCompileDefinitions(_ context.Context, e *evaluator, args []Arg) error {
	e.state.Dir().Definitions = append(e.state.Dir().Definitions, Args(args)...)
	return nil
}

func cmdAddCompileOptions(_ context.Context, e *evaluator, args []Arg) error {
	e.state.Dir().Options = append(e.state.Dir().Options, Args(args)...)
	return nil
}

func cmdAddLinkOptions(_ context.Context, e *evaluator, args []Arg) error {
	e.state.Dir().LinkOptions = append(e.state.Dir().LinkOptions, Args(args)...)
	return nil
}

func cmdIncludeDirectories(_ context.Context, e *evaluator, args []Arg) error {
	dir := e.state.Dir()
	vals := Args(args)
	// The default is to append. CMAKE_INCLUDE_DIRECTORIES_BEFORE flips it, and
	// an explicit BEFORE or AFTER overrides both.
	before := isOn(e.state.GetVar("CMAKE_INCLUDE_DIRECTORIES_BEFORE"))
	var dirs []string
	for _, v := range vals {
		switch v {
		case "AFTER":
			before = false
			continue
		case "BEFORE":
			before = true
			continue
		case "SYSTEM":
			continue
		}
		dirs = append(dirs, e.state.absPath(v))
	}
	if before {
		dir.IncludeDirs = append(dirs, dir.IncludeDirs...)
	} else {
		dir.IncludeDirs = append(dir.IncludeDirs, dirs...)
	}
	return nil
}

func cmdLinkDirectories(_ context.Context, e *evaluator, args []Arg) error {
	dir := e.state.Dir()
	for _, v := range Args(args) {
		if v == "BEFORE" || v == "AFTER" {
			continue
		}
		dir.LinkDirs = append(dir.LinkDirs, e.state.absPath(v))
	}
	return nil
}

func cmdLinkLibraries(_ context.Context, e *evaluator, args []Arg) error {
	e.state.Dir().LinkLibs = append(e.state.Dir().LinkLibs, Args(args)...)
	return nil
}

// ----------------------------------------------------------------------------
// cmake_parse_arguments

func cmdParseArguments(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 4 {
		return e.fatalf("cmake_parse_arguments called with incorrect number of arguments")
	}
	if vals[0] == "PARSE_ARGV" {
		if len(vals) < 6 {
			return e.fatalf("cmake_parse_arguments PARSE_ARGV called with incorrect number of arguments")
		}
		start, err := strconv.Atoi(vals[1])
		if err != nil {
			return e.fatalf("cmake_parse_arguments PARSE_ARGV given non-numeric index %q", vals[1])
		}
		argc, _ := strconv.Atoi(e.state.GetVar("ARGC"))
		var rest []string
		for i := start; i < argc; i++ {
			rest = append(rest, e.state.GetVar("ARGV"+strconv.Itoa(i)))
		}
		return parseArgs(e.state, vals[2], SplitList(vals[3]), SplitList(vals[4]), SplitList(vals[5]), rest)
	}
	return parseArgs(e.state, vals[0], SplitList(vals[1]), SplitList(vals[2]), SplitList(vals[3]), vals[4:])
}

// parseArgs implements the keyword-splitting that every well-behaved CMake
// function uses to read its own arguments.
func parseArgs(s *State, prefix string, options, oneValue, multiValue, args []string) error {
	isOption := setOf(options)
	isOne := setOf(oneValue)
	isMulti := setOf(multiValue)

	// Every declared keyword starts out unset, except the flags, which start
	// FALSE. A caller can then test `if(ARG_FOO)` without a DEFINED guard.
	for _, o := range options {
		s.SetVar(prefix+"_"+o, "FALSE")
	}
	for _, k := range append(append([]string{}, oneValue...), multiValue...) {
		s.UnsetVar(prefix + "_" + k)
	}

	var unparsed, missing []string
	values := map[string][]string{}
	current := ""
	currentIsOne := false
	seen := map[string]bool{}

	for _, a := range args {
		switch {
		case isOption[a]:
			s.SetVar(prefix+"_"+a, "TRUE")
			current = ""
			seen[a] = true
		case isOne[a]:
			if seen[a] {
				// A repeated one-value keyword keeps the last value.
				values[a] = nil
			}
			current, currentIsOne = a, true
			seen[a] = true
			values[a] = nil
		case isMulti[a]:
			if !seen[a] {
				values[a] = []string{}
			}
			current, currentIsOne = a, false
			seen[a] = true
		case current == "":
			unparsed = append(unparsed, a)
		case currentIsOne:
			values[current] = append(values[current], a)
			current = ""
		default:
			values[current] = append(values[current], a)
		}
	}

	for _, k := range oneValue {
		if !seen[k] {
			continue
		}
		if len(values[k]) == 0 {
			missing = append(missing, k)
			continue
		}
		s.SetVar(prefix+"_"+k, values[k][0])
	}
	for _, k := range multiValue {
		if !seen[k] {
			continue
		}
		if len(values[k]) == 0 {
			missing = append(missing, k)
			continue
		}
		s.SetVar(prefix+"_"+k, JoinList(values[k]))
	}
	s.SetVar(prefix+"_UNPARSED_ARGUMENTS", JoinList(unparsed))
	s.SetVar(prefix+"_KEYWORDS_MISSING_VALUES", JoinList(missing))
	return nil
}

func setOf(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, v := range list {
		m[v] = true
	}
	return m
}

// ----------------------------------------------------------------------------
// small utility commands

func cmdSiteName(_ context.Context, e *evaluator, args []Arg) error {
	if len(args) == 0 {
		return nil
	}
	name := e.state.Env["COMPUTERNAME"]
	if name == "" {
		name = e.state.Env["HOSTNAME"]
	}
	if name == "" {
		name = "localhost"
	}
	e.state.Cache.Set(args[0].Val, name, CacheString, "Name of the computer/site where compile is being run", false)
	return nil
}

func cmdHostSystemInformation(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) < 3 || vals[0] != "RESULT" {
		return e.fatalf("cmake_host_system_information called with incorrect arguments")
	}
	out := vals[1]
	var results []string
	for i := 3; i < len(vals); i++ {
		results = append(results, hostInfo(e.state, vals[i]))
	}
	e.state.SetVar(out, JoinList(results))
	return nil
}

func hostInfo(s *State, key string) string {
	switch key {
	case "NUMBER_OF_LOGICAL_CORES", "NUMBER_OF_PHYSICAL_CORES":
		return strconv.Itoa(numCPU())
	case "HOSTNAME":
		if v := s.Env["COMPUTERNAME"]; v != "" {
			return v
		}
		return s.Env["HOSTNAME"]
	case "OS_NAME":
		return hostSystemName()
	case "IS_64BIT":
		return "1"
	default:
		return ""
	}
}

func cmdSeparateArguments(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("separate_arguments called with incorrect number of arguments")
	}
	out := vals[0]
	if len(vals) == 1 {
		// The one-argument form re-splits the variable's own value in place.
		e.state.SetVar(out, JoinList(splitCommandLine(e.state.GetVar(out), false)))
		return nil
	}
	mode := vals[1]
	if len(vals) < 3 {
		e.state.SetVar(out, "")
		return nil
	}
	text := vals[2]
	if len(vals) > 3 && vals[2] == "PROGRAM" {
		text = vals[3]
	}
	switch mode {
	case "UNIX_COMMAND", "PROGRAM":
		e.state.SetVar(out, JoinList(splitCommandLine(text, false)))
	case "WINDOWS_COMMAND":
		e.state.SetVar(out, JoinList(splitCommandLine(text, true)))
	case "NATIVE_COMMAND":
		e.state.SetVar(out, JoinList(splitCommandLine(text, isWindows())))
	default:
		e.state.SetVar(out, JoinList(splitCommandLine(text, false)))
	}
	return nil
}

// splitCommandLine splits a command line into arguments. The Windows rules
// differ from the UNIX ones in that a backslash only escapes a quote.
func splitCommandLine(s string, windows bool) []string {
	var out []string
	var cur strings.Builder
	inArg, inQuote := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && (!windows || s[i+1] == '"' || s[i+1] == '\\'):
			cur.WriteByte(s[i+1])
			i++
			inArg = true
		case c == '"':
			inQuote = !inQuote
			inArg = true
		case !inQuote && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out
}

// ----------------------------------------------------------------------------
// cmake_language

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
