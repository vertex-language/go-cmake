package generate

import (
	"runtime"
	"strconv"
	"strings"

	"github.com/vertex-language/go-cmake/eval"
)

// These are the small pieces the generator expressions need that have no home
// of their own.

func numericEqual(a, b string) bool {
	x, errA := strconv.ParseFloat(strings.TrimSpace(a), 64)
	y, errB := strconv.ParseFloat(strings.TrimSpace(b), 64)
	return errA == nil && errB == nil && x == y
}

func versionCompare(op, a, b string) bool {
	c := eval.CompareVersions(a, b)
	switch op {
	case "VERSION_LESS":
		return c < 0
	case "VERSION_GREATER":
		return c > 0
	case "VERSION_EQUAL":
		return c == 0
	case "VERSION_LESS_EQUAL":
		return c <= 0
	default:
		return c >= 0
	}
}

// platformID is the value CMake reports for CMAKE_SYSTEM_NAME.
func platformID() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "Darwin"
	case "linux":
		return "Linux"
	default:
		return strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
	}
}

// shellPath converts a path to the form the platform's shell expects, which on
// Windows means backslashes: $<SHELL_PATH:...> exists precisely because a
// forward-slash path is fine for the compiler and wrong for cmd.exe.
func shellPath(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

// makeCIdentifier converts a string into a valid C identifier.
