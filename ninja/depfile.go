package ninja

import (
	"strings"
	"unicode"
)

// ParseDepfile parses a Makefile-format depfile (as produced by -MD -MF).
// Format: "target: dep1 dep2 \ \n  dep3"
func ParseDepfile(data []byte) (target string, deps []string, err error) {
	s := string(data)
	s = strings.ReplaceAll(s, "\\\n", " ")

	idx := strings.Index(s, ":")
	if idx == -1 {
		return "", nil, nil
	}

	target = strings.TrimSpace(s[:idx])
	rest := s[idx+1:]

	fields := strings.FieldsFunc(rest, func(r rune) bool {
		return unicode.IsSpace(r)
	})

	for _, f := range fields {
		if f != "" {
			deps = append(deps, f)
		}
	}

	return target, deps, nil
}
