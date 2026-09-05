package eval_test

import "testing"

// A second batch of differential cases, aimed at the corners where a plausible
// reading of the documentation and the actual behaviour of cmake part company.

func TestDiffExpansion(t *testing.T) {
	checkSame(t, "unquoted-list-splitting", `
set(L a b c)
function(count)
  message(STATUS "argc=${ARGC}")
endfunction()
count(${L})
count("${L}")
`)

	checkSame(t, "empty-unquoted-vanishes", `
set(E "")
function(count)
  message(STATUS "argc=${ARGC} argv=[${ARGV}]")
endfunction()
count(${E})
count("${E}")
count(a ${E} b)
`)

	checkSame(t, "semicolons-in-quoted", `
set(V "a;b")
message(STATUS "direct=[${V}]")
foreach(x ${V})
  message(STATUS "loop=[${x}]")
endforeach()
foreach(x "${V}")
  message(STATUS "quoted-loop=[${x}]")
endforeach()
`)

	checkSame(t, "undefined-expands-empty", `
message(STATUS "[${NOT_SET_ANYWHERE}]")
set(X "pre${NOT_SET_ANYWHERE}post")
message(STATUS "[${X}]")
`)

	checkSame(t, "escapes", `
message(STATUS "dollar:[\${NOT_A_REF}]")
message(STATUS "backslash:[\\]")
message(STATUS "quote:[\"]")
set(S "semi\;colon")
list(LENGTH S n)
message(STATUS "n=${n} S=[${S}]")
`)

	checkSame(t, "bracket-arguments", `
message(STATUS [==[
raw ${VAR} \n [[nested]] text
]==])
set(B [[literal ${X}]])
message(STATUS "[${B}]")
`)

	checkSame(t, "cache-vs-normal", `
set(V cached CACHE STRING "doc")
message(STATUS "1=[${V}]")
set(V normal)
message(STATUS "2=[${V}] cache=[$CACHE{V}]")
unset(V)
message(STATUS "3=[${V}]")
`)

	checkSame(t, "cache-force", `
set(V first CACHE STRING "doc")
set(V second CACHE STRING "doc")
message(STATUS "no-force=[${V}]")
set(V third CACHE STRING "doc" FORCE)
message(STATUS "force=[${V}]")
`)
}

func TestDiffIfEdgeCases(t *testing.T) {
	checkSame(t, "bare-string-is-variable-name", `
set(A B)
set(B C)
if(A)
  message(STATUS "A truthy")
else()
  message(STATUS "A falsy")
endif()
if(UNDEFINED_THING)
  message(STATUS "undefined truthy")
else()
  message(STATUS "undefined falsy")
endif()
`)

	checkSame(t, "notfound-suffix", `
set(X SOMETHING-NOTFOUND)
if(X)
  message(STATUS "notfound truthy")
else()
  message(STATUS "notfound falsy")
endif()
`)

	checkSame(t, "elseif-chain", `
foreach(n 1 2 3 4)
  if(n EQUAL 1)
    message(STATUS "one")
  elseif(n EQUAL 2)
    message(STATUS "two")
  elseif(n EQUAL 3)
    message(STATUS "three")
  else()
    message(STATUS "many")
  endif()
endforeach()
`)

	checkSame(t, "nested-if-in-foreach", `
foreach(i RANGE 1 3)
  if(i EQUAL 2)
    foreach(j a b)
      message(STATUS "${i}-${j}")
    endforeach()
  else()
    message(STATUS "${i}-none")
  endif()
endforeach()
`)

	checkSame(t, "numeric-strings", `
foreach(v 007 1e3 0x10 " 5 " 5.5)
  if(${v})
    message(STATUS "[${v}] true")
  else()
    message(STATUS "[${v}] false")
  endif()
endforeach()
`)

	checkSame(t, "empty-condition", `
if()
  message(STATUS "empty true")
else()
  message(STATUS "empty false")
endif()
`)

	checkSame(t, "double-negation", `
set(V ON)
if(NOT NOT V)
  message(STATUS "double-not true")
else()
  message(STATUS "double-not false")
endif()
`)
}

func TestDiffMoreString(t *testing.T) {
	checkSame(t, "regex-anchors-and-classes", `
foreach(s "abc" "ABC" "a1c" "")
  if(s MATCHES "^[a-z]+$")
    message(STATUS "[${s}] lower")
  else()
    message(STATUS "[${s}] not-lower")
  endif()
endforeach()
`)

	checkSame(t, "regex-replace-multiple", `
string(REGEX REPLACE "o" "0" r "foo boo")
message(STATUS "${r}")
string(REGEX REPLACE "^(.)(.*)$" "\\2\\1" rot "hello")
message(STATUS "${rot}")
string(REGEX REPLACE "[aeiou]" "" novowel "beautiful")
message(STATUS "${novowel}")
`)

	checkSame(t, "regex-on-list", `
set(L one two three)
string(REGEX MATCHALL "t[a-z]+" m "${L}")
message(STATUS "m=${m}")
`)

	checkSame(t, "substring-edges", `
string(SUBSTRING "abc" 0 0 empty)
string(SUBSTRING "abc" 3 -1 past)
string(SUBSTRING "abc" 1 100 over)
message(STATUS "[${empty}] [${past}] [${over}]")
`)

	checkSame(t, "timestamp-deterministic", `
set(ENV{SOURCE_DATE_EPOCH} 1000000000)
string(TIMESTAMP t "%Y-%m-%d %H:%M:%S" UTC)
message(STATUS "${t}")
string(TIMESTAMP y "%Y" UTC)
message(STATUS "${y}")
`)

	checkSame(t, "uuid", `
string(UUID u NAMESPACE "6ba7b810-9dad-11d1-80b4-00c04fd430c8" NAME "test" TYPE MD5)
message(STATUS "${u}")
string(UUID U NAMESPACE "6ba7b810-9dad-11d1-80b4-00c04fd430c8" NAME "test" TYPE MD5 UPPER)
message(STATUS "${U}")
`)

	checkSame(t, "configure-string", `
set(NAME world)
set(N 42)
string(CONFIGURE "hello @NAME@ and \${NAME} and @N@" out)
message(STATUS "${out}")
string(CONFIGURE "only @NAME@ not \${NAME}" out2 @ONLY)
message(STATUS "${out2}")
`)

	checkSame(t, "json", `
set(doc [=[{"name":"x","nums":[1,2,3],"nested":{"k":true},"f":1.5}]=])
string(JSON v GET "${doc}" name)
message(STATUS "name=${v}")
string(JSON v GET "${doc}" nums 1)
message(STATUS "nums1=${v}")
string(JSON v GET "${doc}" nested k)
message(STATUS "k=${v}")
string(JSON v GET "${doc}" f)
message(STATUS "f=${v}")
string(JSON v LENGTH "${doc}" nums)
message(STATUS "len=${v}")
string(JSON v TYPE "${doc}" nums)
message(STATUS "type=${v}")
string(JSON v TYPE "${doc}" nested k)
message(STATUS "ktype=${v}")
string(JSON v LENGTH "${doc}")
message(STATUS "objlen=${v}")
string(JSON v MEMBER "${doc}" 0)
message(STATUS "member0=${v}")
`)
}

func TestDiffMoreList(t *testing.T) {
	checkSame(t, "get-out-of-range", `
set(L a b)
list(GET L 5 out)
message(STATUS "unreachable ${out}")
`)

	checkSame(t, "append-to-undefined", `
list(APPEND NEW_LIST first)
message(STATUS "[${NEW_LIST}]")
list(APPEND NEW_LIST second third)
message(STATUS "[${NEW_LIST}]")
`)

	checkSame(t, "insert-negative", `
set(L a b c)
list(INSERT L -1 X)
message(STATUS "${L}")
`)

	checkSame(t, "join-empty", `
set(L "")
list(JOIN L "," j)
message(STATUS "[${j}]")
set(L a)
list(JOIN L "," j)
message(STATUS "[${j}]")
`)

	checkSame(t, "sort-file-basename", `
set(L /z/a.txt /a/z.txt /m/m.txt)
list(SORT L COMPARE FILE_BASENAME)
message(STATUS "${L}")
`)

	checkSame(t, "transform-regex-selector", `
set(L foo.c bar.h baz.c)
list(TRANSFORM L APPEND "!" REGEX "\\.c$")
message(STATUS "${L}")
`)

	checkSame(t, "transform-replace", `
set(L a1 b2 c3)
list(TRANSFORM L REPLACE "([a-z])([0-9])" "\\2-\\1")
message(STATUS "${L}")
`)
}

func TestDiffMoreMath(t *testing.T) {
	checkSame(t, "precedence", `
foreach(expr "2 + 3 * 4" "(2 + 3) * 4" "100 / 10 / 2" "10 - 3 - 2" "1 | 2 & 3" "2 * 3 % 4")
  math(EXPR r "${expr}")
  message(STATUS "${expr} = ${r}")
endforeach()
`)

	checkSame(t, "big-numbers", `
math(EXPR r "1000000 * 1000000")
message(STATUS "${r}")
math(EXPR n "0 - 9223372036854775807")
message(STATUS "${n}")
`)

	checkSame(t, "division-by-zero", `
math(EXPR r "1 / 0")
message(STATUS "unreachable ${r}")
`)
}

func TestDiffIncludeAndSubdir(t *testing.T) {
	checkSame(t, "include-sets-list-vars", `
file(WRITE mod.cmake [[
message(STATUS "in module")
set(FROM_MODULE yes PARENT_SCOPE)
set(FROM_MODULE_DIRECT yes)
]])
include(${CMAKE_CURRENT_SOURCE_DIR}/mod.cmake)
message(STATUS "direct=[${FROM_MODULE_DIRECT}]")
`)

	checkSame(t, "include-optional-missing", `
include(does_not_exist_at_all OPTIONAL RESULT_VARIABLE r)
message(STATUS "r=${r}")
`)

	checkSame(t, "include-missing-is-fatal", `
message(STATUS "before")
include(definitely_not_there)
message(STATUS "after")
`)

	checkSame(t, "include-guard", `
file(WRITE guarded.cmake [[
include_guard(GLOBAL)
message(STATUS "loaded once")
]])
include(${CMAKE_CURRENT_SOURCE_DIR}/guarded.cmake)
include(${CMAKE_CURRENT_SOURCE_DIR}/guarded.cmake)
message(STATUS "done")
`)

	checkSame(t, "module-path", `
file(MAKE_DIRECTORY mods)
file(WRITE mods/MyMod.cmake "message(STATUS \"from module path\")\n")
list(APPEND CMAKE_MODULE_PATH "${CMAKE_CURRENT_SOURCE_DIR}/mods")
include(MyMod)
`)
}

func TestDiffConfigureFile(t *testing.T) {
	checkSame(t, "substitution-and-cmakedefine", `
set(VERSION 1.2.3)
set(HAVE_FEATURE ON)
set(NO_FEATURE OFF)
set(COUNT 7)
file(WRITE in.h.in [[
#define VERSION "@VERSION@"
#define ALSO "${VERSION}"
#cmakedefine HAVE_FEATURE
#cmakedefine NO_FEATURE
#cmakedefine01 HAVE_FEATURE
#cmakedefine01 NO_FEATURE
#cmakedefine COUNT @COUNT@
plain @NOT_A_VAR@ text
]])
configure_file(in.h.in out.h)
file(READ out.h content)
message(STATUS "${content}")
`)

	checkSame(t, "at-only", `
set(V value)
file(WRITE t.in "at=@V@ dollar=\${V}\n")
configure_file(t.in t.out @ONLY)
file(READ t.out c)
message(STATUS "${c}")
`)

	checkSame(t, "copyonly", `
set(V value)
file(WRITE c.in "raw @V@ \${V}\n")
configure_file(c.in c.out COPYONLY)
file(READ c.out c)
message(STATUS "${c}")
`)
}

func TestDiffCmakePath(t *testing.T) {
	checkSame(t, "get", `
set(p "/a/b/c.tar.gz")
cmake_path(GET p FILENAME fn)
cmake_path(GET p EXTENSION ext)
cmake_path(GET p EXTENSION LAST_ONLY lext)
cmake_path(GET p STEM stem)
cmake_path(GET p PARENT_PATH parent)
message(STATUS "${fn} | ${ext} | ${lext} | ${stem} | ${parent}")
`)

	checkSame(t, "queries", `
set(abs "/x/y")
set(rel "x/y")
cmake_path(IS_ABSOLUTE abs a)
cmake_path(IS_ABSOLUTE rel b)
cmake_path(HAS_EXTENSION abs c)
message(STATUS "${a} ${b} ${c}")
`)

	checkSame(t, "append-normal", `
set(p "/a/b")
cmake_path(APPEND p "c" "d" OUTPUT_VARIABLE out)
message(STATUS "${out}")
set(q "/a/./b/../c")
cmake_path(NORMAL_PATH q OUTPUT_VARIABLE n)
message(STATUS "${n}")
`)
}

func TestDiffSeparateArguments(t *testing.T) {
	checkSame(t, "unix-and-windows", `
separate_arguments(u UNIX_COMMAND "a \"b c\" d")
message(STATUS "u=${u}")
separate_arguments(w WINDOWS_COMMAND "a \"b c\" d")
message(STATUS "w=${w}")
set(v "x y z")
separate_arguments(v)
message(STATUS "v=${v}")
`)
}

func TestDiffPolicies(t *testing.T) {
	checkSame(t, "policy-get-set", `
cmake_minimum_required(VERSION 3.20)
cmake_policy(GET CMP0054 p54)
message(STATUS "CMP0054=${p54}")
if(POLICY CMP0054)
  message(STATUS "CMP0054 known")
endif()
if(POLICY CMP9999)
  message(STATUS "CMP9999 known")
else()
  message(STATUS "CMP9999 unknown")
endif()
cmake_policy(SET CMP0054 OLD)
cmake_policy(GET CMP0054 p54b)
message(STATUS "after set=${p54b}")
`)

	checkSame(t, "policy-push-pop", `
cmake_minimum_required(VERSION 3.20)
cmake_policy(SET CMP0054 NEW)
cmake_policy(PUSH)
cmake_policy(SET CMP0054 OLD)
cmake_policy(GET CMP0054 inner)
message(STATUS "inner=${inner}")
cmake_policy(POP)
cmake_policy(GET CMP0054 outer)
message(STATUS "outer=${outer}")
`)
}

func TestDiffBlock(t *testing.T) {
	checkSame(t, "block-scope", `
cmake_minimum_required(VERSION 3.25)
set(v outer)
block()
  message(STATUS "read=${v}")
  set(v inner)
  message(STATUS "inner=${v}")
endblock()
message(STATUS "after=${v}")
`)

	checkSame(t, "block-propagate", `
cmake_minimum_required(VERSION 3.25)
block(PROPAGATE out)
  set(out escaped)
  set(hidden nope)
endblock()
message(STATUS "out=[${out}] hidden=[${hidden}]")
`)
}

func TestDiffExecuteProcess(t *testing.T) {
	checkSame(t, "echo-via-cmake", `
execute_process(
  COMMAND "${CMAKE_COMMAND}" -E echo hello
  OUTPUT_VARIABLE out
  RESULT_VARIABLE res
  OUTPUT_STRIP_TRAILING_WHITESPACE)
message(STATUS "out=[${out}] res=${res}")
`)
}
