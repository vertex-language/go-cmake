package expr_test

import (
	"reflect"
	"testing"

	"github.com/vertex-language/go-cmake/expr"
)

func lookup(kind, name string) (string, bool) {
	vars := map[string]string{
		"FOO":   "hello",
		"BAR":   "world",
		"LIST":  "a;b;c",
		"EMPTY": "",
	}
	env := map[string]string{
		"PATH": "/usr/bin:/bin",
	}
	cache := map[string]string{
		"CMAKE_BUILD_TYPE": "Release",
	}
	switch kind {
	case "normal":
		v, ok := vars[name]
		return v, ok
	case "env":
		v, ok := env[name]
		return v, ok
	case "cache":
		v, ok := cache[name]
		return v, ok
	}
	return "", false
}

func TestExpandStringLiteral(t *testing.T) {
	if got := expr.ExpandString("hello world", lookup); got != "hello world" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringVarRef(t *testing.T) {
	if got := expr.ExpandString("${FOO}", lookup); got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringMissingVar(t *testing.T) {
	if got := expr.ExpandString("${MISSING}", lookup); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestExpandStringVarInString(t *testing.T) {
	if got := expr.ExpandString("say ${FOO}!", lookup); got != "say hello!" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringNestedVar(t *testing.T) {
	// ${outer_${inner}} where inner="FOO", outer_hello="world" — but we only have FOO=hello
	// Simpler: ${${BAR}} where BAR="FOO" should give "hello"
	lk := func(kind, name string) (string, bool) {
		m := map[string]string{"BAR": "FOO", "FOO": "hello"}
		v, ok := m[name]
		return v, ok
	}
	if got := expr.ExpandString("${${BAR}}", lk); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestExpandStringEnvRef(t *testing.T) {
	if got := expr.ExpandString("$ENV{PATH}", lookup); got != "/usr/bin:/bin" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringCacheRef(t *testing.T) {
	if got := expr.ExpandString("$CACHE{CMAKE_BUILD_TYPE}", lookup); got != "Release" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringEscapeN(t *testing.T) {
	if got := expr.ExpandString(`\n`, lookup); got != "\n" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringEscapeT(t *testing.T) {
	if got := expr.ExpandString(`\t`, lookup); got != "\t" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringEscapeIdentity(t *testing.T) {
	if got := expr.ExpandString(`\$`, lookup); got != "$" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringGenexPreserved(t *testing.T) {
	// Generator expressions should be preserved verbatim.
	s := "$<TARGET_FILE:foo>"
	if got := expr.ExpandString(s, lookup); got != s {
		t.Errorf("got %q, want %q", got, s)
	}
}

func TestExpandStringGenexNested(t *testing.T) {
	s := "$<$<CONFIG:Release>:YES>"
	if got := expr.ExpandString(s, lookup); got != s {
		t.Errorf("got %q, want %q", got, s)
	}
}

func TestSplitList(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"a;b;c", []string{"a", "b", "c"}},
		{"a", []string{"a"}},
		{"", nil},
		{"a\\;b", []string{"a;b"}},
		{"a;b\\;c;d", []string{"a", "b;c", "d"}},
		{"a;;b", []string{"a", "", "b"}},
	}
	for _, tc := range tests {
		got := expr.ExpandUnquoted(tc.input, func(_, _ string) (string, bool) { return "", false })
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExpandUnquoted(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestJoinList(t *testing.T) {
	if got := expr.JoinList([]string{"a", "b", "c"}); got != "a;b;c" {
		t.Errorf("got %q", got)
	}
}

func TestExpandStringMultipleVars(t *testing.T) {
	if got := expr.ExpandString("${FOO} ${BAR}", lookup); got != "hello world" {
		t.Errorf("got %q", got)
	}
}
