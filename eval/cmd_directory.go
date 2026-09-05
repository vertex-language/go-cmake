package eval

// Directory-level build settings. Each of these writes a property of the
// directory being processed, which every target declared there afterwards
// inherits -- and, because the settings are copied when a subdirectory is
// added, so does every subdirectory added after the call.

import (
	"context"
	"strings"
)

func init() {
	register("add_definitions", cmdAddDefinitions)
	register("remove_definitions", cmdRemoveDefinitions)
	register("add_compile_definitions", cmdAddCompileDefinitions)
	register("add_compile_options", cmdAddCompileOptions)
	register("add_link_options", cmdAddLinkOptions)
	register("include_directories", cmdIncludeDirectories)
	register("link_directories", cmdLinkDirectories)
	register("link_libraries", cmdLinkLibraries)
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
