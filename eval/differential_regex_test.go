package eval_test

import "testing"

// The regex package is checked against recorded answers; these run the same
// ground through the whole path -- scanner, evaluator, and the commands that
// publish CMAKE_MATCH_<n> -- against the cmake binary itself, because a correct
// matcher wired up wrongly is still a wrong build.
//
// Patterns are written as bracket arguments so the language's own escape
// processing stays out of the way: what is inside [=[ ]=] is exactly what the
// regular expression engine sees.

func TestDiffRegexDialect(t *testing.T) {
	checkSame(t, "constructs-cmake-does-not-have", `
foreach(p [=[a{2}]=] [=[x{1}]=] [=[[0-9]{4}]=] [=[\d+]=] [=[\w+]=] [=[\s]=]
          [=[[[:digit:]]+]=] [=[(a)\1]=] [=[\tx]=])
  string(REGEX MATCH "${p}" m "aa 2024 abc_123 tx x{1}")
  message(STATUS "[${p}] -> [${m}]")
endforeach()
`)

	checkSame(t, "bracket-expressions", `
foreach(p [=[[]ab]]=] [=[[^]ab]]=] [=[[a-]]=] [=[[-a]]=] [=[[a^]]=]
          [=[[/\]]=] [=[[\t]]=] [=[[a-c]+]=] [=[[^a]+]=])
  string(REGEX MATCH "${p}" m "]a-^/\tbc")
  message(STATUS "[${p}] -> [${m}]")
endforeach()
`)

	checkSame(t, "anchors-and-the-dot", `
string(REGEX MATCH [=[a.b]=] m "a
b")
message(STATUS "dot-newline=[${m}]")
string(REGEX MATCH [=[a$]=] m "a
")
message(STATUS "dollar-before-newline=[${m}]")
string(REGEX MATCH [=[^b]=] m "a
b")
message(STATUS "caret-multiline=[${m}]")
foreach(p [=[a^b]=] [=[a$b]=] [=[^ab+d$]=] [=[<.*>]=] [=[cat|dog]=] [=[()a]=] [=[(|a)b]=])
  string(REGEX MATCH "${p}" m "a^b <x><y> hotdog abbd")
  message(STATUS "[${p}] -> [${m}]")
endforeach()
`)

	checkSame(t, "refusals", `
foreach(p [=[a.*?b]=] [=[a+?]=] [=[a**]=] [=[(?:ab)+]=] [=[?a]=] [=[^*]=]
          [=[[abc]=] [=[(ab]=] [=[ab)]=] [=[[z-a]]=])
  string(REGEX MATCH "${p}" m "ab")
  message(STATUS "compiled [${p}]")
endforeach()
`)
}

// TestDiffRegexMatchVariables covers CMAKE_MATCH_<n> and CMAKE_MATCH_COUNT,
// which projects read directly -- version parsing is written this way in
// nearly every find module.
func TestDiffRegexMatchVariables(t *testing.T) {
	checkSame(t, "counts", `
macro(show tag)
  if(DEFINED CMAKE_MATCH_COUNT)
    set(d "def")
  else()
    set(d "undef")
  endif()
  message(STATUS "${tag}: count=${d}[${CMAKE_MATCH_COUNT}] 0=[${CMAKE_MATCH_0}] 1=[${CMAKE_MATCH_1}] 2=[${CMAKE_MATCH_2}]")
endmacro()
show(fresh)
string(REGEX MATCH [=[(a)(b)]=] m "ab")
show(two-groups)
string(REGEX MATCH [=[zzz]=] m "ab")
show(after-failure)
string(REGEX MATCH [=[ab]=] m "ab")
show(no-groups)
string(REGEX MATCH [=[(a)(z?)b]=] m "ab")
show(empty-second-group)
string(REGEX MATCH [=[q*]=] m "ab")
show(empty-whole-match)
if("1.2.3" MATCHES [=[^([0-9]+)\.([0-9]+)\.([0-9]+)$]=])
  show(version)
endif()
`)

	checkSame(t, "matchall-publishes-the-last", `
string(REGEX MATCHALL [=[(a)(b)]=] m "abab")
message(STATUS "[${m}] 1=[${CMAKE_MATCH_1}] n=[${CMAKE_MATCH_COUNT}]")
`)
}

// TestDiffRegexIteration is the part Go's own FindAll and ReplaceAll get
// differently: CMake searches the remainder of the subject from its start, so
// an anchor applies again at every step and an empty match is never skipped.
func TestDiffRegexIteration(t *testing.T) {
	checkSame(t, "matchall", `
foreach(p [=[^a]=] [=[a*]=] [=[a]=] [=[aa]=] [=[a$]=] [=[$]=])
  string(REGEX MATCHALL "${p}" m "aaaa")
  list(LENGTH m n)
  message(STATUS "[${p}] -> ${n} [${m}]")
endforeach()
string(REGEX MATCHALL [=[a*]=] m "bab")
message(STATUS "empty-matches=[${m}]")
`)

	checkSame(t, "replace", `
string(REGEX REPLACE [=[^a]=] "-" o "aaa")
message(STATUS "anchored=[${o}]")
string(REGEX REPLACE [=[a*]=] "-" o "bab")
message(STATUS "empty=[${o}]")
string(REGEX REPLACE [=[x*]=] "-" o "ab")
message(STATUS "never-matches=[${o}]")
string(REGEX REPLACE [=[(a)(b)]=] [=[\2\1]=] o "abab")
message(STATUS "swap=[${o}]")
string(REGEX REPLACE [=[ab]=] [=[<\0>]=] o "ab")
message(STATUS "whole=[${o}]")
string(REGEX REPLACE [=[(a)|(z)]=] [=[<\2>]=] o "a")
message(STATUS "absent-group=[${o}]")
string(REGEX REPLACE [=[ab]=] "$1" o "ab")
message(STATUS "dollar-is-literal=[${o}]")
string(REGEX REPLACE [=[ab]=] [=[x\\y]=] o "ab")
message(STATUS "backslash=[${o}]")
`)

	checkSame(t, "replacement-refusals", `
string(REGEX REPLACE [=[ab]=] [=[x\ty]=] o "ab")
message(STATUS "accepted=[${o}]")
`)

	checkSame(t, "replacement-trailing-backslash", `
string(REGEX REPLACE [=[ab]=] [=[x\]=] o "ab")
message(STATUS "accepted=[${o}]")
`)
}

// TestDiffRegexAcrossCommands covers the other commands that take a pattern:
// they all reach the same engine, and each one has its own error wording.
func TestDiffRegexAcrossCommands(t *testing.T) {
	checkSame(t, "list-filter", `
set(l a1 b2 a3 "" a{2})
list(FILTER l INCLUDE REGEX [=[a[0-9]]=])
message(STATUS "[${l}]")
set(l aa bb)
list(FILTER l EXCLUDE REGEX [=[^a]=])
message(STATUS "[${l}]")
`)

	checkSame(t, "list-transform", `
set(l "a.b" "c.d")
list(TRANSFORM l REPLACE [=[\.]=] "/")
message(STATUS "[${l}]")
set(l ax bx)
list(TRANSFORM l TOUPPER REGEX [=[^a]=])
message(STATUS "[${l}]")
`)

	checkSame(t, "if-matches", `
foreach(v "a{2}" "aa" "1.2" "x")
  if("${v}" MATCHES [=[^[0-9]+\.[0-9]+$]=])
    message(STATUS "[${v}] version, 1=[${CMAKE_MATCH_1}]")
  elseif("${v}" MATCHES [=[a{2}]=])
    message(STATUS "[${v}] literal braces")
  else()
    message(STATUS "[${v}] neither")
  endif()
endforeach()
`)

	checkSame(t, "if-matches-bad-pattern", `
if("a" MATCHES [=[a**]=])
  message(STATUS "matched")
endif()
`)

	checkSame(t, "file-strings", `
file(WRITE in.txt "alpha
beta
a{2}
")
file(STRINGS in.txt out REGEX [=[^a]=])
message(STATUS "[${out}]")
file(STRINGS in.txt out2 REGEX [=[a{2}]=])
message(STATUS "[${out2}]")
`)

	checkSame(t, "list-filter-bad-pattern", `
set(l a)
list(FILTER l INCLUDE REGEX [=[a**]=])
`)

	checkSame(t, "string-match-bad-pattern", `
string(REGEX MATCH [=[a**]=] m "a")
`)
}
