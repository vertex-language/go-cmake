package eval

import (
	"context"
	"path"
	"path/filepath"
	"strings"
)

func init() {
	register("get_filename_component", cmdGetFilenameComponent)
	register("cmake_path", cmdCMakePath)
}

// cmdGetFilenameComponent decomposes a path. It predates cmake_path() and is
// still what most projects in the wild use.
func cmdGetFilenameComponent(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 3 {
		return e.fatalf("get_filename_component called with incorrect number of arguments")
	}
	out, input, mode := v[0], v[1], v[2]
	p := slashPath(input)

	var result string
	switch mode {
	case "DIRECTORY", "PATH":
		result = dirOf(p)
	case "NAME":
		result = BaseName(p)
	case "EXT":
		// EXT is the longest extension: "a.tar.gz" yields ".tar.gz".
		name := BaseName(p)
		if i := strings.IndexByte(name, '.'); i >= 0 {
			result = name[i:]
		}
	case "LAST_EXT":
		name := BaseName(p)
		if i := strings.LastIndexByte(name, '.'); i > 0 {
			result = name[i:]
		}
	case "NAME_WE":
		name := BaseName(p)
		if i := strings.IndexByte(name, '.'); i >= 0 {
			name = name[:i]
		}
		result = name
	case "NAME_WLE":
		name := BaseName(p)
		if i := strings.LastIndexByte(name, '.'); i > 0 {
			name = name[:i]
		}
		result = name
	case "ABSOLUTE", "REALPATH":
		base := e.state.GetVar("CMAKE_CURRENT_SOURCE_DIR")
		for i := 3; i+1 < len(v); i++ {
			if v[i] == "BASE_DIR" {
				base = v[i+1]
			}
		}
		if isAbsolutePath(p) {
			result = slashPath(path.Clean(p))
		} else {
			result = slashPath(joinPath(base, p))
		}
	case "PROGRAM":
		// The program form splits a command line: the executable, then its
		// arguments into an optional second variable.
		parts := splitCommandLine(input, isWindows())
		if len(parts) > 0 {
			result = parts[0]
		}
		for i := 3; i+1 < len(v); i++ {
			if v[i] == "PROGRAM_ARGS" {
				e.state.SetVar(v[i+1], JoinList(parts[1:]))
			}
		}
	default:
		return e.fatalf("get_filename_component unknown component %s", mode)
	}

	if containsStr(v, "CACHE") {
		e.state.Cache.Set(out, result, CacheString, "", true)
		return nil
	}
	e.state.SetVar(out, result)
	return nil
}

func dirOf(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	i := strings.LastIndexByte(p, '/')
	switch {
	case i < 0:
		return ""
	case i == 0:
		return "/"
	default:
		return p[:i]
	}
}

// cmdCMakePath implements the modern, purely lexical path command. Unlike
// get_filename_component it never touches the filesystem, which is why it can
// reason about paths for a target platform that is not the host.
func cmdCMakePath(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("cmake_path called with incorrect number of arguments")
	}
	op, name := v[0], v[1]

	// Every sub-command but SET and APPEND reads the named variable's value.
	value := slashPath(e.state.GetVar(name))

	set := func(out, val string) {
		e.state.SetVar(out, val)
	}
	// OUTPUT_VARIABLE redirects a query that would otherwise print to a
	// positional output variable.
	outVar := func(def string) string {
		for i := 2; i+1 < len(v); i++ {
			if v[i] == "OUTPUT_VARIABLE" {
				return v[i+1]
			}
		}
		return def
	}

	switch op {
	case "SET":
		if len(v) < 3 {
			return e.fatalf("cmake_path SET called with incorrect number of arguments")
		}
		set(name, slashPath(v[2]))
	case "APPEND":
		parts := []string{value}
		for _, p := range v[2:] {
			if p == "OUTPUT_VARIABLE" {
				break
			}
			parts = append(parts, p)
		}
		set(outVar(name), path.Join(parts...))
	case "GET":
		if len(v) < 4 {
			return e.fatalf("cmake_path GET called with incorrect number of arguments")
		}
		component, out := v[2], v[len(v)-1]
		switch component {
		case "ROOT_NAME":
			set(out, rootName(value))
		case "ROOT_DIRECTORY":
			if isAbsolutePath(value) {
				set(out, "/")
			} else {
				set(out, "")
			}
		case "ROOT_PATH":
			if isAbsolutePath(value) {
				set(out, rootName(value)+"/")
			} else {
				set(out, "")
			}
		case "FILENAME":
			set(out, BaseName(value))
		case "EXTENSION":
			name := BaseName(value)
			if containsStr(v, "LAST_ONLY") {
				if i := strings.LastIndexByte(name, '.'); i > 0 {
					set(out, name[i:])
					return nil
				}
				set(out, "")
				return nil
			}
			if i := strings.IndexByte(name, '.'); i > 0 {
				set(out, name[i:])
			} else {
				set(out, "")
			}
		case "STEM":
			name := BaseName(value)
			if containsStr(v, "LAST_ONLY") {
				if i := strings.LastIndexByte(name, '.'); i > 0 {
					name = name[:i]
				}
			} else if i := strings.IndexByte(name, '.'); i > 0 {
				name = name[:i]
			}
			set(out, name)
		case "RELATIVE_PART":
			set(out, strings.TrimPrefix(strings.TrimPrefix(value, rootName(value)), "/"))
		case "PARENT_PATH":
			set(out, dirOf(value))
		default:
			return e.fatalf("cmake_path GET unknown component %s", component)
		}
	case "HAS_ROOT_NAME":
		set(v[2], boolVarOnOff(rootName(value) != ""))
	case "HAS_ROOT_DIRECTORY", "HAS_ROOT_PATH":
		set(v[2], boolVarOnOff(hasRootDirectory(value)))
	case "HAS_FILENAME":
		set(v[2], boolVarOnOff(BaseName(value) != ""))
	case "HAS_EXTENSION":
		set(v[2], boolVarOnOff(strings.Contains(BaseName(value), ".")))
	case "HAS_STEM":
		set(v[2], boolVarOnOff(BaseName(value) != ""))
	case "HAS_PARENT_PATH":
		set(v[2], boolVarOnOff(dirOf(value) != ""))
	case "IS_ABSOLUTE":
		set(v[2], boolVarOnOff(pathIsAbsolute(value)))
	case "IS_RELATIVE":
		set(v[2], boolVarOnOff(!pathIsAbsolute(value)))
	case "NORMAL_PATH":
		set(outVar(name), path.Clean(value))
	case "ABSOLUTE_PATH":
		base := e.state.GetVar("CMAKE_CURRENT_SOURCE_DIR")
		for i := 2; i+1 < len(v); i++ {
			if v[i] == "BASE_DIRECTORY" {
				base = v[i+1]
			}
		}
		if isAbsolutePath(value) {
			set(outVar(name), path.Clean(value))
		} else {
			set(outVar(name), slashPath(joinPath(base, value)))
		}
	case "RELATIVE_PATH":
		if len(v) < 4 {
			return e.fatalf("cmake_path RELATIVE_PATH called with incorrect number of arguments")
		}
		base := ""
		for i := 2; i+1 < len(v); i++ {
			if v[i] == "BASE_DIRECTORY" {
				base = v[i+1]
			}
		}
		rel, err := filepath.Rel(base, value)
		if err != nil {
			set(outVar(name), value)
		} else {
			set(outVar(name), slashPath(rel))
		}
	case "REMOVE_FILENAME":
		set(outVar(name), dirOf(value)+"/")
	case "REPLACE_FILENAME":
		if len(v) < 3 {
			return e.fatalf("cmake_path REPLACE_FILENAME called with incorrect number of arguments")
		}
		set(outVar(name), path.Join(dirOf(value), v[2]))
	case "REMOVE_EXTENSION":
		n := BaseName(value)
		if i := strings.IndexByte(n, '.'); i > 0 {
			n = n[:i]
		}
		set(outVar(name), path.Join(dirOf(value), n))
	case "REPLACE_EXTENSION":
		if len(v) < 3 {
			return e.fatalf("cmake_path REPLACE_EXTENSION called with incorrect number of arguments")
		}
		n := BaseName(value)
		if i := strings.IndexByte(n, '.'); i > 0 {
			n = n[:i]
		}
		ext := v[2]
		if ext != "" && !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		set(outVar(name), path.Join(dirOf(value), n+ext))
	case "NATIVE_PATH":
		out := v[len(v)-1]
		if isWindows() {
			set(out, strings.ReplaceAll(value, "/", "\\"))
		} else {
			set(out, value)
		}
	case "CONVERT":
		if len(v) < 4 {
			return e.fatalf("cmake_path CONVERT called with incorrect number of arguments")
		}
		input := v[1]
		switch v[2] {
		case "TO_CMAKE_PATH_LIST":
			sep := ":"
			if isWindows() {
				sep = ";"
			}
			parts := strings.Split(input, sep)
			for i := range parts {
				parts[i] = slashPath(parts[i])
			}
			e.state.SetVar(v[3], JoinList(parts))
		case "TO_NATIVE_PATH_LIST":
			sep := ":"
			if isWindows() {
				sep = ";"
			}
			parts := SplitList(input)
			for i := range parts {
				if isWindows() {
					parts[i] = strings.ReplaceAll(parts[i], "/", "\\")
				}
			}
			e.state.SetVar(v[3], strings.Join(parts, sep))
		}
	case "COMPARE":
		if len(v) < 5 {
			return e.fatalf("cmake_path COMPARE called with incorrect number of arguments")
		}
		eq := path.Clean(slashPath(v[1])) == path.Clean(slashPath(v[3]))
		if v[2] == "NOT_EQUAL" {
			eq = !eq
		}
		e.state.SetVar(v[4], boolVarOnOff(eq))
	default:
		return e.fatalf("cmake_path does not recognize sub-command %s", op)
	}
	return nil
}

// pathIsAbsolute implements cmake_path's notion of an absolute path, which is
// the one the target platform's filesystem uses rather than the one the host
// happens to run. On Windows a leading slash names the root of the *current*
// drive, so "/x/y" is rooted but not absolute -- it does not identify a file
// on its own. if(IS_ABSOLUTE) predates this distinction and still accepts it.
func pathIsAbsolute(p string) bool {
	if isWindows() {
		return rootName(p) != "" && hasRootDirectory(p)
	}
	return hasRootDirectory(p)
}

// hasRootDirectory reports whether a path begins at a root, ignoring any drive.
func hasRootDirectory(p string) bool {
	p = strings.TrimPrefix(p, rootName(p))
	return p != "" && (p[0] == '/' || p[0] == '\\')
}

// rootName returns the Windows drive prefix of a path, or "" if there is none.
func rootName(p string) string {
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return p[:2]
		}
	}
	return ""
}
