package eval_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vertex-language/go-cmake/eval"
)

// The differential tests run a CMake script through this implementation and
// through the cmake binary on the host, and require the message() output to
// match exactly. Reading the documentation tells you what CMake is supposed to
// do; running it tells you what it does, and where those differ the binary is
// the specification.
//
// If no cmake is installed the tests skip rather than fail: a contributor
// without one should still be able to run the suite.

var (
	cmakeOnce sync.Once
	cmakePath string
)

// realCMake locates the reference implementation.
func realCMake(t *testing.T) string {
	t.Helper()
	cmakeOnce.Do(func() {
		if p, err := exec.LookPath("cmake"); err == nil {
			cmakePath = p
		}
	})
	if cmakePath == "" {
		t.Skip("no cmake on PATH; skipping differential test")
	}
	return cmakePath
}

// runReal executes a script with `cmake -P` and returns its combined output,
// normalised so that only the message() content is compared.
func runReal(t *testing.T, dir, script string) string {
	t.Helper()
	path := filepath.Join(dir, "script.cmake")
	if err := os.WriteFile(path, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(realCMake(t), "-P", "script.cmake")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// A script that fails in real CMake is still a valid comparison: the
		// output up to the failure is what matters.
		t.Logf("real cmake exited with %v", err)
	}
	return normalise(string(out))
}

// runOurs evaluates the same script through this package.
func runOurs(t *testing.T, dir, script string) string {
	t.Helper()
	path := filepath.Join(dir, "ours.cmake")
	if err := os.WriteFile(path, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	state := eval.NewState(filepath.ToSlash(dir), filepath.ToSlash(dir), os.Environ())
	state.Runner = eval.OSRunner()
	state.SetVar("CMAKE_COMMAND", cmakePath)
	var sb strings.Builder
	state.LogSink = func(mode, text string) {
		switch mode {
		case "":
			sb.WriteString(text + "\n")
		case "STATUS":
			sb.WriteString("-- " + text + "\n")
		case "AUTHOR_WARNING":
			sb.WriteString("CMake Warning (author)\n  " + text + "\n")
		case "DEPRECATION":
			sb.WriteString("CMake Warning (deprecated)\n  " + text + "\n")
		default:
			sb.WriteString(mode + ": " + text + "\n")
		}
	}
	err := eval.EvalScript(context.Background(), state, diskFS{}, path)
	if err != nil {
		sb.WriteString(err.Error() + "\n")
	}
	return normalise(sb.String())
}

// normalise strips the decoration that differs between the two front ends but
// carries no semantic content: blank lines, trailing whitespace, and the
// "CMake Error at <file>:<line>" preamble whose file name necessarily differs.
func normalise(s string) string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "CMake Error at ") || strings.HasPrefix(line, "CMake Error:") {
			out = append(out, "CMake Error")
			continue
		}
		// A warning's banner names the file and line, which necessarily differ
		// between the two runs; the text below it is what is being compared.
		if strings.HasPrefix(line, "CMake Warning (author)") {
			out = append(out, "CMake Warning (author)")
			continue
		}
		if strings.HasPrefix(line, "CMake Warning (deprecated)") {
			out = append(out, "CMake Warning (deprecated)")
			continue
		}
		if strings.HasPrefix(line, "Call Stack") || strings.HasPrefix(line, "  script.cmake") ||
			strings.HasPrefix(line, "  ours.cmake") || strings.HasPrefix(line, "This warning is for project developers") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// checkSame runs one script through both implementations and compares.
func checkSame(t *testing.T, name, script string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		dir := t.TempDir()
		want := runReal(t, dir, script)
		got := runOurs(t, dir, script)
		if got != want {
			t.Errorf("output mismatch\n--- real cmake ---\n%s\n--- go-cmake ---\n%s", want, got)
		}
	})
}

func TestDiffConditions(t *testing.T) {
	checkSame(t, "constants", `
foreach(v 1 0 ON OFF YES NO TRUE FALSE Y N IGNORE NOTFOUND "" 2 -1 0.0 abc x-NOTFOUND)
  if(${v})
    message(STATUS "[${v}] true")
  else()
    message(STATUS "[${v}] false")
  endif()
endforeach()
`)

	checkSame(t, "precedence", `
set(A ON)
set(B OFF)
set(C ON)
if(A OR B AND C)
  message(STATUS "or-and: true")
else()
  message(STATUS "or-and: false")
endif()
if(NOT A STREQUAL "ON")
  message(STATUS "not-streq: true")
else()
  message(STATUS "not-streq: false")
endif()
if((A OR B) AND NOT C)
  message(STATUS "parens: true")
else()
  message(STATUS "parens: false")
endif()
`)

	checkSame(t, "comparisons", `
set(X 10)
set(Y 9)
foreach(op LESS GREATER EQUAL LESS_EQUAL GREATER_EQUAL)
  if(X ${op} Y)
    message(STATUS "${op}: yes")
  else()
    message(STATUS "${op}: no")
  endif()
endforeach()
if(X STRLESS Y)
  message(STATUS "strless: yes")
else()
  message(STATUS "strless: no")
endif()
`)

	checkSame(t, "versions", `
foreach(pair "1.2.3;1.2" "1.2;1.2.0" "1.10;1.9" "2.0;10.0" "1.2.3.4;1.2.3.4")
  list(GET pair 0 a)
  list(GET pair 1 b)
  if(a VERSION_LESS b)
    message(STATUS "${a} < ${b}")
  elseif(a VERSION_EQUAL b)
    message(STATUS "${a} == ${b}")
  else()
    message(STATUS "${a} > ${b}")
  endif()
endforeach()
`)

	checkSame(t, "quoted-args-cmp0054", `
cmake_minimum_required(VERSION 3.10)
set(FOO BAR)
set(BAR VALUE)
if(FOO STREQUAL BAR)
  message(STATUS "unquoted: equal")
else()
  message(STATUS "unquoted: differ")
endif()
if("FOO" STREQUAL "BAR")
  message(STATUS "quoted: equal")
else()
  message(STATUS "quoted: differ")
endif()
`)

	checkSame(t, "matches", `
set(S "libfoo-1.2.3.so")
if(S MATCHES "^lib([a-z]+)-([0-9]+)\\.([0-9]+)")
  message(STATUS "name=${CMAKE_MATCH_1} major=${CMAKE_MATCH_2} minor=${CMAKE_MATCH_3} count=${CMAKE_MATCH_COUNT}")
else()
  message(STATUS "no match")
endif()
`)

	checkSame(t, "in-list", `
set(L a b c)
if("b" IN_LIST L)
  message(STATUS "b in list")
endif()
if("z" IN_LIST L)
  message(STATUS "z in list")
else()
  message(STATUS "z not in list")
endif()
`)

	checkSame(t, "defined", `
set(SET_VAR 1)
set(EMPTY_VAR "")
foreach(n SET_VAR EMPTY_VAR MISSING_VAR)
  if(DEFINED ${n})
    message(STATUS "${n} defined")
  else()
    message(STATUS "${n} not defined")
  endif()
endforeach()
`)
}

func TestDiffForeach(t *testing.T) {
	checkSame(t, "range", `
foreach(i RANGE 3)
  message(STATUS "a${i}")
endforeach()
foreach(i RANGE 2 6)
  message(STATUS "b${i}")
endforeach()
foreach(i RANGE 0 10 3)
  message(STATUS "c${i}")
endforeach()
foreach(i RANGE 5 1 -2)
  message(STATUS "d${i}")
endforeach()
`)

	checkSame(t, "in-lists-items", `
set(L1 a b)
set(L2 c d)
set(EMPTY "")
foreach(x IN LISTS L1 L2 EMPTY ITEMS e f)
  message(STATUS "x=${x}")
endforeach()
`)

	checkSame(t, "zip-lists", `
set(A 1 2 3)
set(B x y)
foreach(a b IN ZIP_LISTS A B)
  message(STATUS "a=${a} b=${b}")
endforeach()
`)

	checkSame(t, "break-continue", `
foreach(i RANGE 1 10)
  if(i EQUAL 3)
    continue()
  endif()
  if(i EQUAL 6)
    break()
  endif()
  message(STATUS "i=${i}")
endforeach()
`)

	checkSame(t, "scoping", `
set(x outer)
foreach(x a b)
  message(STATUS "in loop: ${x}")
endforeach()
message(STATUS "after loop: ${x}")
`)

	checkSame(t, "nested", `
foreach(i RANGE 1 2)
  foreach(j RANGE 1 2)
    message(STATUS "${i}.${j}")
  endforeach()
endforeach()
`)
}

func TestDiffWhile(t *testing.T) {
	checkSame(t, "countdown", `
set(n 3)
while(n GREATER 0)
  message(STATUS "n=${n}")
  math(EXPR n "${n} - 1")
endwhile()
`)
}

func TestDiffFunctionsAndMacros(t *testing.T) {
	checkSame(t, "function-args", `
function(f a b)
  message(STATUS "a=${a} b=${b} ARGC=${ARGC} ARGV=${ARGV} ARGN=${ARGN} ARGV0=${ARGV0}")
endfunction()
f(one two three four)
`)

	checkSame(t, "function-scope", `
set(v outer)
function(f)
  message(STATUS "read: ${v}")
  set(v inner)
  message(STATUS "local: ${v}")
  set(w propagated PARENT_SCOPE)
endfunction()
f()
message(STATUS "after: ${v} w=${w}")
`)

	checkSame(t, "macro-no-scope", `
set(v outer)
macro(m)
  set(v changed)
endmacro()
m()
message(STATUS "after macro: ${v}")
`)

	checkSame(t, "macro-argn-is-textual", `
macro(m)
  message(STATUS "ARGN=${ARGN} ARGC=${ARGC}")
  if(DEFINED ARGV0)
    message(STATUS "ARGV0 is a defined variable")
  else()
    message(STATUS "ARGV0 is not a defined variable")
  endif()
endmacro()
m(a b)
`)

	checkSame(t, "return-from-function", `
function(f)
  message(STATUS "before")
  return()
  message(STATUS "after")
endfunction()
f()
message(STATUS "done")
`)

	checkSame(t, "recursion", `
function(fact n out)
  if(n LESS_EQUAL 1)
    set(${out} 1 PARENT_SCOPE)
  else()
    math(EXPR m "${n} - 1")
    fact(${m} sub)
    math(EXPR r "${n} * ${sub}")
    set(${out} ${r} PARENT_SCOPE)
  endif()
endfunction()
fact(6 result)
message(STATUS "6! = ${result}")
`)
}

func TestDiffList(t *testing.T) {
	checkSame(t, "basics", `
set(L a b c d e)
list(LENGTH L n)
message(STATUS "len=${n}")
list(GET L 0 2 -1 got)
message(STATUS "got=${got}")
list(JOIN L "+" joined)
message(STATUS "joined=${joined}")
list(SUBLIST L 1 3 sub)
message(STATUS "sub=${sub}")
list(FIND L c idx)
message(STATUS "idx=${idx}")
list(FIND L zz missing)
message(STATUS "missing=${missing}")
`)

	checkSame(t, "mutation", `
set(L a b c)
list(APPEND L d)
list(PREPEND L z)
list(INSERT L 2 X Y)
message(STATUS "L=${L}")
list(REMOVE_ITEM L X b)
message(STATUS "L=${L}")
list(REMOVE_AT L 0)
message(STATUS "L=${L}")
list(REVERSE L)
message(STATUS "L=${L}")
set(D a b a c b)
list(REMOVE_DUPLICATES D)
message(STATUS "D=${D}")
`)

	checkSame(t, "pop", `
set(L a b c)
list(POP_FRONT L first)
list(POP_BACK L last)
message(STATUS "first=${first} last=${last} rest=${L}")
set(E "")
list(POP_BACK E gone)
message(STATUS "gone=[${gone}]")
`)

	checkSame(t, "sort", `
set(L banana Apple cherry apple Banana)
list(SORT L)
message(STATUS "default=${L}")
set(L banana Apple cherry apple Banana)
list(SORT L CASE INSENSITIVE)
message(STATUS "nocase=${L}")
set(L file10 file9 file1)
list(SORT L COMPARE NATURAL)
message(STATUS "natural=${L}")
set(L a b c)
list(SORT L ORDER DESCENDING)
message(STATUS "desc=${L}")
`)

	checkSame(t, "filter-transform", `
set(L foo.c bar.h baz.c)
list(FILTER L INCLUDE REGEX "\\.c$")
message(STATUS "c=${L}")
set(L a b c)
list(TRANSFORM L TOUPPER)
message(STATUS "upper=${L}")
set(L a b c)
list(TRANSFORM L APPEND "!" AT 0 2)
message(STATUS "at=${L}")
set(L one two three)
list(TRANSFORM L PREPEND "x_" OUTPUT_VARIABLE OUT)
message(STATUS "out=${OUT} orig=${L}")
`)

	checkSame(t, "empty-elements", `
set(L "a;;b")
list(LENGTH L n)
message(STATUS "n=${n} L=${L}")
set(E "")
list(LENGTH E n)
message(STATUS "empty n=${n}")
`)
}

func TestDiffString(t *testing.T) {
	checkSame(t, "basics", `
string(TOUPPER "MixedCase" up)
string(TOLOWER "MixedCase" low)
string(LENGTH "hello" len)
string(SUBSTRING "abcdefgh" 2 3 sub)
string(SUBSTRING "abcdefgh" 5 -1 tail)
string(STRIP "  padded  " stripped)
message(STATUS "${up} ${low} ${len} ${sub} ${tail} [${stripped}]")
`)

	checkSame(t, "find-replace", `
string(FIND "abcabc" "b" i)
string(FIND "abcabc" "b" j REVERSE)
string(FIND "abcabc" "z" k)
string(REPLACE "b" "X" r "abcabc")
message(STATUS "${i} ${j} ${k} ${r}")
`)

	checkSame(t, "regex", `
string(REGEX MATCH "([0-9]+)\\.([0-9]+)" m "version 3.14 here")
message(STATUS "m=${m} 1=${CMAKE_MATCH_1} 2=${CMAKE_MATCH_2}")
string(REGEX MATCHALL "[0-9]+" all "a1b22c333")
message(STATUS "all=${all}")
string(REGEX REPLACE "([a-z])([0-9])" "\\2\\1" swapped "a1 b2")
message(STATUS "swapped=${swapped}")
`)

	checkSame(t, "append-concat-join", `
set(s "start")
string(APPEND s "-end")
string(PREPEND s "pre-")
message(STATUS "${s}")
string(CONCAT c a b c)
string(JOIN "," j x y z)
message(STATUS "${c} ${j}")
`)

	checkSame(t, "compare", `
string(COMPARE EQUAL "a" "a" r1)
string(COMPARE LESS "a" "b" r2)
string(COMPARE NOTEQUAL "a" "b" r3)
message(STATUS "${r1} ${r2} ${r3}")
`)

	checkSame(t, "hashes", `
string(MD5 h1 "hello")
string(SHA1 h2 "hello")
string(SHA256 h3 "hello")
message(STATUS "${h1}")
message(STATUS "${h2}")
message(STATUS "${h3}")
`)

	checkSame(t, "make-c-identifier", `
string(MAKE_C_IDENTIFIER "foo-bar.baz 1" id)
string(MAKE_C_IDENTIFIER "9lives" id2)
message(STATUS "${id} ${id2}")
`)

	checkSame(t, "repeat", `
string(REPEAT "ab" 3 r)
string(REPEAT "x" 0 e)
message(STATUS "[${r}] [${e}]")
`)
}

func TestDiffMath(t *testing.T) {
	checkSame(t, "arithmetic", `
foreach(expr "1 + 2" "10 - 3 * 2" "(1 + 2) * 3" "7 / 2" "7 % 3" "-5 + 1" "1 << 4" "255 >> 4" "6 & 3" "6 | 3" "6 ^ 3" "~0")
  math(EXPR r "${expr}")
  message(STATUS "${expr} = ${r}")
endforeach()
`)

	checkSame(t, "hex", `
math(EXPR r "0xff + 1")
message(STATUS "${r}")
math(EXPR h "255" OUTPUT_FORMAT HEXADECIMAL)
message(STATUS "${h}")
`)
}

func TestDiffVariables(t *testing.T) {
	checkSame(t, "set-forms", `
set(A single)
set(B one two three)
set(C)
message(STATUS "A=${A} B=${B} C=[${C}]")
set(D "with;semicolons")
list(LENGTH D n)
message(STATUS "D=${D} n=${n}")
`)

	checkSame(t, "nested-refs", `
set(inner NAME)
set(NAME value)
message(STATUS "${${inner}}")
set(prefix FOO)
set(FOO_BAR found)
message(STATUS "${${prefix}_BAR}")
`)

	checkSame(t, "unset", `
set(V 1)
message(STATUS "before: [${V}]")
unset(V)
message(STATUS "after: [${V}]")
`)

	checkSame(t, "env", `
set(ENV{GO_CMAKE_TEST} hello)
message(STATUS "env=$ENV{GO_CMAKE_TEST}")
if(DEFINED ENV{GO_CMAKE_TEST})
  message(STATUS "defined")
endif()
unset(ENV{GO_CMAKE_TEST})
message(STATUS "after unset=[$ENV{GO_CMAKE_TEST}]")
`)

	checkSame(t, "escapes-and-quoting", `
message(STATUS "tab:[	] newline-literal:[\n]")
set(S "a\;b")
list(LENGTH S n)
message(STATUS "n=${n}")
message(STATUS [[bracket ${NOT_EXPANDED} literal]])
`)
}

func TestDiffParseArguments(t *testing.T) {
	checkSame(t, "keywords", `
function(f)
  cmake_parse_arguments(ARG "FLAG;OTHER" "NAME;PATH" "SOURCES;DEPS" ${ARGN})
  message(STATUS "FLAG=${ARG_FLAG} OTHER=${ARG_OTHER}")
  message(STATUS "NAME=${ARG_NAME} PATH=[${ARG_PATH}]")
  message(STATUS "SOURCES=${ARG_SOURCES} DEPS=[${ARG_DEPS}]")
  message(STATUS "UNPARSED=${ARG_UNPARSED_ARGUMENTS}")
  message(STATUS "MISSING=${ARG_KEYWORDS_MISSING_VALUES}")
endfunction()
f(FLAG NAME hello SOURCES a.c b.c leftover DEPS)
`)
}

func TestDiffProperties(t *testing.T) {
	checkSame(t, "global", `
set_property(GLOBAL PROPERTY MY_PROP first)
get_property(v GLOBAL PROPERTY MY_PROP)
message(STATUS "v=${v}")
set_property(GLOBAL APPEND PROPERTY MY_PROP second)
get_property(v GLOBAL PROPERTY MY_PROP)
message(STATUS "v=${v}")
get_property(isset GLOBAL PROPERTY MY_PROP SET)
get_property(unset GLOBAL PROPERTY NOT_THERE SET)
message(STATUS "set=${isset} unset=${unset}")
`)
}

func TestDiffFileCommands(t *testing.T) {
	checkSame(t, "write-read", `
file(WRITE out.txt "line one\nline two\n")
file(READ out.txt content)
message(STATUS "len=${content}")
file(APPEND out.txt "line three\n")
file(STRINGS out.txt lines)
message(STATUS "lines=${lines}")
list(LENGTH lines n)
message(STATUS "n=${n}")
`)

	checkSame(t, "path-components", `
set(P "/a/b/c/file.tar.gz")
get_filename_component(d "${P}" DIRECTORY)
get_filename_component(n "${P}" NAME)
get_filename_component(e "${P}" EXT)
get_filename_component(le "${P}" LAST_EXT)
get_filename_component(we "${P}" NAME_WE)
get_filename_component(wle "${P}" NAME_WLE)
message(STATUS "${d} | ${n} | ${e} | ${le} | ${we} | ${wle}")
`)
}

func TestDiffErrors(t *testing.T) {
	checkSame(t, "fatal-error", `
message(STATUS "before")
message(FATAL_ERROR "stop here")
message(STATUS "after")
`)

	checkSame(t, "unknown-command", `
message(STATUS "before")
this_command_does_not_exist(a b)
message(STATUS "after")
`)
}
