package eval

// The property commands. CMake keeps properties in several scopes -- global,
// directory, target, source, test, cache -- and these commands are the one
// way in and out of all of them.

import (
	"context"
	"sort"
)

func init() {
	register("mark_as_advanced", cmdMarkAsAdvanced)
	register("define_property", cmdDefineProperty)
	register("set_property", cmdSetProperty)
	register("get_property", cmdGetProperty)
	register("set_directory_properties", cmdSetDirectoryProperties)
	register("get_directory_property", cmdGetDirectoryProperty)
	register("get_cmake_property", cmdGetCMakeProperty)
}

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
