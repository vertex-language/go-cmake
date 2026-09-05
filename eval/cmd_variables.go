package eval

// Commands that set variables: the language's assignment statements, plus
// message(), which is the only way a script says anything at all.

import (
	"context"
	"strconv"
	"strings"
)

func init() {
	register("set", cmdSet)
	register("unset", cmdUnset)
	register("message", cmdMessage)
	register("option", cmdOption)
	register("math", cmdMath)
}

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
