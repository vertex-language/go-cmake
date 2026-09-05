package generate

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/vertex-language/go-cmake/eval"
)

// These are the small pieces the generator expressions need that have no home
// of their own.

var regexCache = map[string]*regexp.Regexp{}

func compileRegex(pattern string) (*regexp.Regexp, error) {
	if re, ok := regexCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache[pattern] = re
	return re, nil
}

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
func makeCIdentifier(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out != "" && out[0] >= '0' && out[0] <= '9' {
		return "_" + out
	}
	return out
}
