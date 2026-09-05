package eval_test

import "testing"

// The CMake 2.x commands are still here because projects written against them
// are still here. Each is checked against the real cmake the same way
// everything else is, since being bug-compatible with a twenty-year-old command
// is the whole reason for implementing it.

func TestDiffLegacyCommands(t *testing.T) {
	checkSame(t, "remove", `
set(L a b c d)
remove(L b d)
message(STATUS "L=${L}")
set(E "")
remove(E x)
message(STATUS "E=[${E}]")
`)

	checkSame(t, "variable_requires-satisfied", `
set(FEATURE ON)
set(DEPENDENCY ON)
variable_requires(FEATURE RESULT DEPENDENCY)
message(STATUS "result=${RESULT}")
`)

	checkSame(t, "variable_requires-not-tested", `
set(FEATURE OFF)
variable_requires(FEATURE RESULT MISSING_THING)
message(STATUS "result=[${RESULT}]")
`)

	checkSame(t, "build_command", `
build_command(CMD)
if(CMD MATCHES "--build")
  message(STATUS "names a build")
else()
  message(STATUS "does not name a build")
endif()
`)
}

// exec_program is execute_process with the arguments in one string, which is
// the part worth testing: they have to be split the way a shell would.
func TestDiffExecProgram(t *testing.T) {
	checkSame(t, "output-and-status", `
exec_program("${CMAKE_COMMAND}" ARGS "-E echo one two" OUTPUT_VARIABLE out RETURN_VALUE rc)
message(STATUS "out=[${out}] rc=${rc}")
`)
}
