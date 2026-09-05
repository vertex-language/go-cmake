package eval

import (
	"context"
	"strconv"
	"strings"
)

func init() {
	register("find_package", cmdFindPackage)
	register("find_library", cmdFindLibrary)
	register("find_program", cmdFindProgram)
	register("find_path", cmdFindPath)
	register("find_file", cmdFindFile)
	register("find_package_handle_standard_args", cmdFPHSA)
}

// findRequest is the common shape of the four find_* commands: a variable to
// write, some names to look for, and a set of directories to look in.
type findRequest struct {
	Variable string
	Names    []string
	Paths    []string
	Hints    []string
	Suffixes []string
	Doc      string
	Required bool
	NoCache  bool
}

// parseFindArgs reads the keyword arguments shared by the find_* commands.
func parseFindArgs(v []string) findRequest {
	r := findRequest{}
	if len(v) == 0 {
		return r
	}
	r.Variable = v[0]
	rest := v[1:]

	// The short form is find_library(VAR name path1 path2 ...): everything
	// after the first name is a path unless a keyword says otherwise.
	keyword := "NAMES"
	sawKeyword := false
	for i, a := range rest {
		switch a {
		case "NAMES", "HINTS", "PATHS", "PATH_SUFFIXES", "DOC", "NAMES_PER_DIR":
			keyword = a
			sawKeyword = true
			continue
		case "REQUIRED":
			r.Required = true
			continue
		case "NO_CACHE":
			r.NoCache = true
			continue
		case "NO_DEFAULT_PATH", "NO_PACKAGE_ROOT_PATH", "NO_CMAKE_PATH",
			"NO_CMAKE_ENVIRONMENT_PATH", "NO_SYSTEM_ENVIRONMENT_PATH",
			"NO_CMAKE_SYSTEM_PATH", "CMAKE_FIND_ROOT_PATH_BOTH",
			"ONLY_CMAKE_FIND_ROOT_PATH", "NO_CMAKE_FIND_ROOT_PATH",
			"NO_CMAKE_INSTALL_PREFIX", "VALIDATOR":
			continue
		}
		switch keyword {
		case "NAMES":
			// Without an explicit NAMES keyword only the first argument is a
			// name; the rest are search paths.
			if !sawKeyword && i > 0 {
				r.Paths = append(r.Paths, a)
			} else {
				r.Names = append(r.Names, a)
			}
		case "HINTS":
			r.Hints = append(r.Hints, a)
		case "PATHS":
			r.Paths = append(r.Paths, a)
		case "PATH_SUFFIXES":
			r.Suffixes = append(r.Suffixes, a)
		case "DOC":
			r.Doc = a
		}
	}
	return r
}

// searchDirs expands a request into the ordered list of directories to try,
// including each path suffix under each root.
func (e *evaluator) searchDirs(r findRequest, envVars, cmakeVars []string) []string {
	var roots []string
	add := func(list ...string) {
		for _, p := range list {
			if p != "" {
				roots = append(roots, slashPath(p))
			}
		}
	}
	add(r.Hints...)
	for _, name := range cmakeVars {
		add(SplitList(e.state.GetVar(name))...)
	}
	add(SplitList(e.state.GetVar("CMAKE_PREFIX_PATH"))...)
	for _, name := range envVars {
		add(splitPathList(e.state.Env[name])...)
	}
	add(r.Paths...)

	var out []string
	for _, root := range roots {
		out = append(out, root)
		for _, suffix := range r.Suffixes {
			out = append(out, root+"/"+suffix)
		}
	}
	return out
}

// splitPathList splits a PATH-style environment variable.
func splitPathList(v string) []string {
	if v == "" {
		return nil
	}
	sep := ":"
	if isWindows() {
		sep = ";"
	}
	return strings.Split(v, sep)
}

// notFound is the value CMake writes when a find fails: <VAR>-NOTFOUND, which
// tests false and prints usefully.
func notFound(v string) string { return v + "-NOTFOUND" }

// storeFindResult writes the result of a find_* command into the cache, unless
// NO_CACHE was requested. The cache is what makes a find sticky across
// configure runs and what lets a user override it from the command line.
func (e *evaluator) storeFindResult(r findRequest, value string, typ CacheEntryType) {
	if r.NoCache {
		e.state.SetVar(r.Variable, value)
		return
	}
	e.state.Cache.Set(r.Variable, value, typ, r.Doc, false)
	e.state.Current.Unset(r.Variable)
}

// alreadyFound reports whether a previous run already resolved this variable,
// in which case the search is skipped entirely.
func (e *evaluator) alreadyFound(name string) bool {
	if entry, ok := e.state.Cache.Get(name); ok && !isOff(entry.Value) {
		return true
	}
	if v, ok := e.state.Current.Get(name); ok && !isOff(v) {
		return true
	}
	return false
}

func cmdFindProgram(_ context.Context, e *evaluator, args []Arg) error {
	r := parseFindArgs(Args(args))
	if r.Variable == "" {
		return e.fatalf("find_program called with incorrect number of arguments")
	}
	if e.alreadyFound(r.Variable) {
		return nil
	}
	dirs := e.searchDirs(r, []string{"PATH"}, []string{"CMAKE_PROGRAM_PATH"})
	exts := []string{""}
	if isWindows() {
		exts = []string{".com", ".exe", ".bat", ".cmd", ""}
	}
	for _, dir := range dirs {
		for _, name := range r.Names {
			for _, ext := range exts {
				candidate := slashPath(joinPath(dir, name+ext))
				if fi, err := e.fs.Stat(candidate); err == nil && !fi.IsDir() {
					e.storeFindResult(r, candidate, CacheFilepath)
					return nil
				}
			}
		}
	}
	e.storeFindResult(r, notFound(r.Variable), CacheFilepath)
	if r.Required {
		return e.fatalf("Could NOT find %s (missing: %s)", r.Variable, strings.Join(r.Names, " "))
	}
	return nil
}

func cmdFindLibrary(_ context.Context, e *evaluator, args []Arg) error {
	r := parseFindArgs(Args(args))
	if r.Variable == "" {
		return e.fatalf("find_library called with incorrect number of arguments")
	}
	if e.alreadyFound(r.Variable) {
		return nil
	}
	dirs := e.searchDirs(r, []string{"LIB", "LD_LIBRARY_PATH"}, []string{"CMAKE_LIBRARY_PATH"})
	// Every root is also searched in its lib subdirectories, which is where a
	// CMAKE_PREFIX_PATH entry actually points at a library.
	var expanded []string
	for _, d := range dirs {
		expanded = append(expanded, d, d+"/lib", d+"/lib64")
	}
	for _, dir := range expanded {
		for _, name := range r.Names {
			for _, cand := range libraryFileNames(name) {
				full := slashPath(joinPath(dir, cand))
				if fi, err := e.fs.Stat(full); err == nil && !fi.IsDir() {
					e.storeFindResult(r, full, CacheFilepath)
					return nil
				}
			}
		}
	}
	e.storeFindResult(r, notFound(r.Variable), CacheFilepath)
	if r.Required {
		return e.fatalf("Could NOT find %s (missing: %s)", r.Variable, strings.Join(r.Names, " "))
	}
	return nil
}

// libraryFileNames expands a bare library name into the file names it could
// have on this platform, in the order CMake prefers them.
func libraryFileNames(name string) []string {
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, ".") {
		return []string{name}
	}
	if isWindows() {
		return []string{name + ".lib", name + ".dll.a", "lib" + name + ".a", name + ".a"}
	}
	return []string{"lib" + name + ".so", "lib" + name + ".dylib", "lib" + name + ".a", name + ".so", name + ".a"}
}

func cmdFindPath(_ context.Context, e *evaluator, args []Arg) error {
	return e.findPathOrFile(args, "find_path", true)
}

func cmdFindFile(_ context.Context, e *evaluator, args []Arg) error {
	return e.findPathOrFile(args, "find_file", false)
}

// findPathOrFile implements the two commands that differ only in what they
// report: find_path yields the containing directory, find_file the file.
func (e *evaluator) findPathOrFile(args []Arg, command string, wantDir bool) error {
	r := parseFindArgs(Args(args))
	if r.Variable == "" {
		return e.fatalf("%s called with incorrect number of arguments", command)
	}
	if e.alreadyFound(r.Variable) {
		return nil
	}
	dirs := e.searchDirs(r, []string{"INCLUDE", "CPATH"}, []string{"CMAKE_INCLUDE_PATH"})
	var expanded []string
	for _, d := range dirs {
		expanded = append(expanded, d, d+"/include")
	}
	for _, dir := range expanded {
		for _, name := range r.Names {
			full := slashPath(joinPath(dir, name))
			if _, err := e.fs.Stat(full); err == nil {
				if wantDir {
					e.storeFindResult(r, slashPath(dir), CachePath)
				} else {
					e.storeFindResult(r, full, CacheFilepath)
				}
				return nil
			}
		}
	}
	typ := CachePath
	if !wantDir {
		typ = CacheFilepath
	}
	e.storeFindResult(r, notFound(r.Variable), typ)
	if r.Required {
		return e.fatalf("Could NOT find %s (missing: %s)", r.Variable, strings.Join(r.Names, " "))
	}
	return nil
}

// ----------------------------------------------------------------------------
// find_package

func cmdFindPackage(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("find_package called with incorrect number of arguments")
	}
	name := v[0]
	required := containsStr(v, "REQUIRED")
	quiet := containsStr(v, "QUIET")
	version := ""
	if len(v) > 1 && looksLikeVersion(v[1]) {
		version = v[1]
	}
	exact := containsStr(v, "EXACT")
	var components []string
	keyword := ""
	for _, a := range v[1:] {
		switch a {
		case "COMPONENTS", "OPTIONAL_COMPONENTS", "REQUIRED", "QUIET", "EXACT",
			"MODULE", "CONFIG", "NO_MODULE", "GLOBAL", "BYPASS_PROVIDER",
			"NAMES", "CONFIGS", "HINTS", "PATHS", "PATH_SUFFIXES", "REGISTRY_VIEW":
			keyword = a
			continue
		}
		if keyword == "COMPONENTS" || keyword == "OPTIONAL_COMPONENTS" {
			components = append(components, a)
		}
	}

	// Module mode first: a Find<Name>.cmake on CMAKE_MODULE_PATH is a script
	// this evaluator can actually run, and running it is far more faithful than
	// guessing at what it would have found.
	if !containsStr(v, "CONFIG") && !containsStr(v, "NO_MODULE") {
		if path := e.findModuleFile("Find" + name + ".cmake"); path != "" {
			e.state.SetVar(name+"_FIND_REQUIRED", boolVar(required))
			e.state.SetVar(name+"_FIND_QUIETLY", boolVar(quiet))
			e.state.SetVar(name+"_FIND_VERSION", version)
			e.state.SetVar(name+"_FIND_COMPONENTS", JoinList(components))
			for _, c := range components {
				e.state.SetVar(name+"_FIND_REQUIRED_"+c, "1")
			}
			err := e.evalFile(ctx, path)
			if _, ok := err.(returnSignal); ok {
				err = nil
			}
			return err
		}
	}

	// Config mode: look for <Name>Config.cmake in the usual places.
	if path := e.findConfigFile(ctx, name, version, exact); path != "" {
		e.state.SetVar(name+"_FOUND", "1")
		e.state.SetVar(name+"_DIR", slashPath(dirOf(path)))
		e.state.SetVar(name+"_CONFIG", slashPath(path))
		err := e.evalFile(ctx, path)
		if _, ok := err.(returnSignal); ok {
			err = nil
		}
		return err
	}

	e.state.SetVar(name+"_FOUND", "0")
	e.state.SetVar(name+"_NOTFOUND_MESSAGE",
		"Could not find a package configuration file provided by \""+name+"\"")
	if required {
		return e.fatalf("Could not find a package configuration file provided by %q"+
			" with any of the following names:\n  %sConfig.cmake\n  %s-config.cmake",
			name, name, strings.ToLower(name))
	}
	if !quiet {
		e.state.log("WARNING", "Could NOT find "+name)
	}
	return nil
}

// looksLikeVersion reports whether an argument is a version number rather than
// a keyword, which is how find_package's optional second argument is told apart.
func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) && s[i] != '.' {
			return false
		}
	}
	return isDigit(s[0])
}

// findModuleFile looks for a Find<Name>.cmake on CMAKE_MODULE_PATH.
func (e *evaluator) findModuleFile(name string) string {
	for _, dir := range SplitList(e.state.GetVar("CMAKE_MODULE_PATH")) {
		candidate := joinPath(dir, name)
		if fi, err := e.fs.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}

// findConfigFile searches for a package's config file in the directories CMake
// checks, honouring <Name>_DIR and CMAKE_PREFIX_PATH.
func (e *evaluator) findConfigFile(ctx context.Context, name, version string, exact bool) string {
	names := []string{name + "Config.cmake", strings.ToLower(name) + "-config.cmake"}

	var roots []string
	if dir := e.state.GetVar(name + "_DIR"); dir != "" && !isOff(dir) {
		roots = append(roots, dir)
	}
	if root := e.state.GetVar(name + "_ROOT"); root != "" {
		roots = append(roots, root)
	}
	if root := e.state.Env[name+"_ROOT"]; root != "" {
		roots = append(roots, root)
	}
	roots = append(roots, SplitList(e.state.GetVar("CMAKE_PREFIX_PATH"))...)
	roots = append(roots, splitPathList(e.state.Env["CMAKE_PREFIX_PATH"])...)

	// Under each prefix, CMake looks in the lib/cmake and share/cmake layouts
	// that packages actually install into.
	suffixes := []string{
		"", "cmake", name,
		"lib/cmake/" + name, "lib/" + name + "/cmake", "lib/cmake",
		"lib64/cmake/" + name, "share/cmake/" + name, "share/" + name + "/cmake",
		"share/" + name,
	}
	for _, root := range roots {
		for _, suffix := range suffixes {
			dir := joinPath(root, suffix)
			for _, n := range names {
				candidate := joinPath(dir, n)
				if fi, err := e.fs.Stat(candidate); err == nil && !fi.IsDir() {
					if version == "" || e.configVersionOK(ctx, dir, name, version, exact) {
						return candidate
					}
				}
			}
		}
	}
	return ""
}

// configVersionOK consults a package's ConfigVersion file.
//
// That file is an ordinary CMake script whose job is to set
// PACKAGE_VERSION_COMPATIBLE and PACKAGE_VERSION_EXACT after inspecting the
// PACKAGE_FIND_VERSION variables. Running it is the only way to get the right
// answer, because the compatibility rule lives in the script: the same version
// number satisfies a request under write_basic_package_version_file's
// SameMajorVersion policy and fails it under ExactVersion. Scraping the
// declared version out of the text cannot tell those apart.
func (e *evaluator) configVersionOK(ctx context.Context, dir, name, want string, exact bool) bool {
	for _, n := range []string{name + "ConfigVersion.cmake", strings.ToLower(name) + "-config-version.cmake"} {
		path := joinPath(dir, n)
		if fi, err := e.fs.Stat(path); err != nil || fi.IsDir() {
			continue
		}
		compatible, isExact, declared, ran := e.runConfigVersionFile(ctx, path, want)
		if ran && (compatible || isExact) {
			if exact {
				return isExact
			}
			return compatible
		}
		if ran && declared == "" {
			// The script ran but claimed nothing, which is a rejection.
			return false
		}
		// The script could not be run; fall back to comparing the version it
		// declares, which is still better than accepting anything.
		if declared == "" {
			declared = extractSetValue(readFileString(e.fs, path), "PACKAGE_VERSION")
		}
		if declared == "" {
			return true
		}
		constraint := "ANY_NEWER_VERSION"
		if exact {
			constraint = "EXACT"
		}
		return VersionSatisfies(declared, want, constraint)
	}
	// No version file means the package makes no claim, and CMake accepts it.
	return true
}

// runConfigVersionFile evaluates a ConfigVersion script in a scope of its own
// and reports what it decided. A failure to run is reported rather than
// treated as a rejection, so that a script using a construct this
// implementation does not support falls back to a version comparison instead of
// making the package vanish.
func (e *evaluator) runConfigVersionFile(ctx context.Context, path, want string) (compatible, exact bool, declared string, ran bool) {
	outer := e.state.Current
	scope := NewScope(FunctionScope, outer)
	e.state.Current = scope
	defer func() { e.state.Current = outer }()

	scope.Set("PACKAGE_FIND_NAME", "")
	scope.Set("PACKAGE_FIND_VERSION", want)
	parts := versionComponents(want)
	for i, suffix := range []string{"MAJOR", "MINOR", "PATCH", "TWEAK"} {
		v := ""
		if i < len(parts) {
			v = strconv.FormatInt(parts[i], 10)
		}
		scope.Set("PACKAGE_FIND_VERSION_"+suffix, v)
	}
	scope.Set("PACKAGE_FIND_VERSION_COUNT", strconv.Itoa(len(parts)))
	scope.Set("CMAKE_CURRENT_LIST_DIR", dirOf(path))

	if err := e.evalFile(ctx, path); err != nil {
		if _, isReturn := err.(returnSignal); !isReturn {
			return false, false, scope.mustGet("PACKAGE_VERSION"), false
		}
	}
	return isOn(scope.mustGet("PACKAGE_VERSION_COMPATIBLE")),
		isOn(scope.mustGet("PACKAGE_VERSION_EXACT")),
		scope.mustGet("PACKAGE_VERSION"),
		true
}

// readFileString reads a file, yielding "" when it cannot be read.
func readFileString(fs FS, path string) string {
	data, err := fs.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// extractSetValue pulls the literal value out of a `set(NAME "value")` line.
// It is the fallback for a ConfigVersion script that could not be run: the
// declared version is still readable from the text even when the surrounding
// logic is not.
func extractSetValue(src, name string) string {
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(t), "set(") {
			continue
		}
		inner := t[4:]
		if i := strings.LastIndexByte(inner, ')'); i >= 0 {
			inner = inner[:i]
		}
		fields := splitCommandLine(inner, false)
		if len(fields) >= 2 && fields[0] == name {
			return fields[1]
		}
	}
	return ""
}

// VersionSatisfies reports whether a found version meets a requested one under
// one of the compatibility rules CMake's package version files implement.
func VersionSatisfies(found, required, constraint string) bool {
	f, r := versionComponents(found), versionComponents(required)
	switch constraint {
	case "EXACT":
		return versionsEqual(f, r)
	case "SAME_MAJOR":
		if len(f) == 0 || len(r) == 0 {
			return false
		}
		return f[0] == r[0] && CompareVersions(found, required) >= 0
	case "SAME_MINOR":
		if len(f) < 2 || len(r) < 2 {
			return false
		}
		return f[0] == r[0] && f[1] == r[1] && CompareVersions(found, required) >= 0
	default: // ANY_NEWER_VERSION
		return CompareVersions(found, required) >= 0
	}
}

// versionsEqual compares two component lists, treating missing trailing
// components as zero so that 1.2 and 1.2.0 are the same version.
func versionsEqual(a, b []int64) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int64
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return false
		}
	}
	return true
}

// cmdFPHSA implements find_package_handle_standard_args, which every
// Find<Name>.cmake module ends with. Providing it here means a project's own
// find modules work without shipping CMake's module directory.
func cmdFPHSA(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("find_package_handle_standard_args called with incorrect number of arguments")
	}
	name := v[0]
	var required []string
	foundVar := name + "_FOUND"
	versionVar := ""
	message := ""
	keyword := ""
	// The old signature is (Name "message" var1 var2 ...); the new one uses
	// REQUIRED_VARS. Both are still in wide use.
	legacy := len(v) > 1 && v[1] != "REQUIRED_VARS" && v[1] != "DEFAULT_MSG" &&
		v[1] != "FOUND_VAR" && v[1] != "VERSION_VAR" && v[1] != "HANDLE_COMPONENTS" &&
		v[1] != "FAIL_MESSAGE" && v[1] != "CONFIG_MODE" && v[1] != "NAME_MISMATCHED"
	for i := 1; i < len(v); i++ {
		switch v[i] {
		case "REQUIRED_VARS", "FOUND_VAR", "VERSION_VAR", "FAIL_MESSAGE", "REASON_FAILURE_MESSAGE":
			keyword = v[i]
			continue
		case "DEFAULT_MSG":
			keyword = "REQUIRED_VARS"
			continue
		case "HANDLE_COMPONENTS", "CONFIG_MODE", "NAME_MISMATCHED", "HANDLE_VERSION_RANGE":
			keyword = ""
			continue
		}
		switch keyword {
		case "REQUIRED_VARS":
			required = append(required, v[i])
		case "FOUND_VAR":
			foundVar = v[i]
			keyword = ""
		case "VERSION_VAR":
			versionVar = v[i]
			keyword = ""
		case "FAIL_MESSAGE", "REASON_FAILURE_MESSAGE":
			message = v[i]
			keyword = ""
		default:
			if legacy && i >= 2 {
				required = append(required, v[i])
			} else if legacy && i == 1 {
				message = v[i]
			}
		}
	}

	var missing []string
	for _, name := range required {
		if isOff(e.state.GetVar(name)) {
			missing = append(missing, name)
		}
	}
	found := len(missing) == 0
	e.state.SetVar(foundVar, boolVar(found))
	e.state.SetVar(strings.ToUpper(name)+"_FOUND", boolVar(found))
	e.state.SetVar(name+"_FOUND", boolVar(found))

	version := ""
	if versionVar != "" {
		version = e.state.GetVar(versionVar)
	}
	if found {
		if !isOn(e.state.GetVar(name + "_FIND_QUIETLY")) {
			text := "Found " + name + ": " + e.state.GetVar(firstOr(required, ""))
			if version != "" {
				text += " (found version \"" + version + "\")"
			}
			e.state.log("STATUS", text)
		}
		return nil
	}
	if message == "" {
		message = "Could NOT find " + name + " (missing: " + strings.Join(missing, " ") + ")"
	}
	if isOn(e.state.GetVar(name + "_FIND_REQUIRED")) {
		return e.fatalf("%s", message)
	}
	if !isOn(e.state.GetVar(name + "_FIND_QUIETLY")) {
		e.state.log("STATUS", message)
	}
	return nil
}

func firstOr(list []string, def string) string {
	if len(list) > 0 {
		return list[0]
	}
	return def
}
