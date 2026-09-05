package eval_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/eval"
	"github.com/vertex-language/go-cmake/parser"
	"github.com/vertex-language/go-cmake/token"
)

func TestEvalSimple(t *testing.T) {
	src := `
cmake_minimum_required(VERSION 3.10)
project(MyProj)

set(FOO "bar")
message(STATUS "FOO is ${FOO}")

add_executable(main main.cpp)
add_library(lib STATIC lib.cpp)
target_link_libraries(main PRIVATE lib)
`

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "CMakeLists.txt", []byte(src))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	state := eval.NewState(".", ".", nil)
	var msgs []string
	state.LogSink = func(mode, text string) {
		if mode != "" {
			msgs = append(msgs, mode+": "+text)
		} else {
			msgs = append(msgs, text)
		}
	}

	err = eval.Eval(context.Background(), f, state, newMemFS())
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}

	if state.GetVar("PROJECT_NAME") != "MyProj" {
		t.Errorf("PROJECT_NAME = %q, want MyProj", state.GetVar("PROJECT_NAME"))
	}

	if state.GetVar("FOO") != "bar" {
		t.Errorf("FOO = %q, want bar", state.GetVar("FOO"))
	}

	if len(msgs) != 1 || !strings.Contains(msgs[0], "FOO is bar") {
		t.Errorf("msgs = %v", msgs)
	}

	if tgt, ok := state.Targets["main"]; !ok {
		t.Errorf("target main not found")
	} else if tgt.Type != "EXECUTABLE" {
		t.Errorf("target main type = %s, want EXECUTABLE", tgt.Type)
	}

	if tgt, ok := state.Targets["lib"]; !ok {
		t.Errorf("target lib not found")
	} else if tgt.Type != "STATIC" {
		t.Errorf("target lib type = %s, want STATIC", tgt.Type)
	}
}
