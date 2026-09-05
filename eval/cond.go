package eval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vertex-language/go-cmake/regex"
)

// EvalCondition evaluates an if()/elseif()/while() condition.
//
// The implementation mirrors CMake's own cmConditionEvaluator: the argument
// list is reduced in five passes of increasing precedence, each pass replacing
// the tokens it consumed with a literal "1" or "0". This is not merely one way
// to write the evaluator; the reduction order *is* the language's precedence,
// and expressions like `if(NOT A STREQUAL B)` only parse correctly because the
// binary pass runs before the NOT pass.
func (s *State) EvalCondition(name string, args []Arg, fs FS) (bool, error) {
	original := args
	args = append([]Arg(nil), args...)
	var err error
	if args, err = s.condParens(args, fs); err != nil {
		return false, err
	}
	if args, err = s.condUnary(args, fs); err != nil {
		return false, err
	}
	if args, err = s.condBinary(args, fs); err != nil {
		return false, condArgs(name, original, err)
	}
	if args, err = s.condNot(args); err != nil {
		return false, err
	}
	if args, err = s.condAndOr(args); err != nil {
		return false, err
	}
	switch len(args) {
	case 0:
		return false, nil
	case 1:
		return s.boolValue(args[0]), nil
	default:
		// CMake echoes the arguments as they were written, not as they were
		// reduced: the reduced form is an artefact of the evaluator and tells
		// the reader nothing about the line they need to fix.
		return false, fmt.Errorf("%s given arguments:\n  %s\nUnknown arguments specified",
			name, quoteArgs(original))
	}
}

// condArgs prefixes an error with the arguments the condition was given, which
// is the only part of a bad if() a reader can act on.
func condArgs(name string, original []Arg, err error) error {
	var bad *condRegexError
	if !errors.As(err, &bad) {
		return err
	}
	return fmt.Errorf("%s given arguments:\n  %s\n%s", name, quoteArgs(original), err)
}

// quoteArgs renders a condition's arguments the way CMake echoes them back in
// a diagnostic: each one quoted, separated by spaces.
func quoteArgs(args []Arg) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = `"` + a.Val + `"`
	}
	return strings.Join(parts, " ")
}

// truth is the result of a reduction step: a synthetic unquoted "0"/"1".
func truth(b bool) Arg {
	if b {
		return Arg{Val: "1"}
	}
	return Arg{Val: "0"}
}

// ----------------------------------------------------------------------------
// Level 0: parentheses

func (s *State) condParens(args []Arg, fs FS) ([]Arg, error) {
	for {
		open := -1
		closed := -1
		for i, a := range args {
			if a.Quoted {
				continue
			}
			if a.Val == "(" {
				open = i
			} else if a.Val == ")" {
				if open < 0 {
					return nil, fmt.Errorf("mismatched parenthesis in condition")
				}
				closed = i
				break
			}
		}
		if closed < 0 {
			if open >= 0 {
				return nil, fmt.Errorf("mismatched parenthesis in condition")
			}
			return args, nil
		}
		v, err := s.EvalCondition("if", args[open+1:closed], fs)
		if err != nil {
			return nil, err
		}
		out := append([]Arg(nil), args[:open]...)
		out = append(out, truth(v))
		out = append(out, args[closed+1:]...)
		args = out
	}
}

// ----------------------------------------------------------------------------
// Level 1: unary predicates

var unaryOps = map[string]bool{
	"EXISTS": true, "COMMAND": true, "DEFINED": true, "POLICY": true,
	"TARGET": true, "TEST": true, "IS_DIRECTORY": true, "IS_SYMLINK": true,
	"IS_ABSOLUTE": true, "IS_READABLE": true, "IS_WRITABLE": true,
	"IS_EXECUTABLE": true,
}

func (s *State) condUnary(args []Arg, fs FS) ([]Arg, error) {
	// Right to left, so that `NOT EXISTS x` and nested unaries reduce correctly.
	for i := len(args) - 2; i >= 0; i-- {
		if args[i].Quoted || !unaryOps[args[i].Val] {
			continue
		}
		op, operand := args[i].Val, args[i+1]
		var v bool
		switch op {
		case "EXISTS":
			v = s.statExists(operand.Val, fs, func(fi os.FileInfo) bool { return true })
		case "IS_DIRECTORY":
			v = s.statExists(operand.Val, fs, func(fi os.FileInfo) bool { return fi.IsDir() })
		case "IS_SYMLINK":
			v = s.statExists(operand.Val, fs, func(fi os.FileInfo) bool {
				return fi.Mode()&os.ModeSymlink != 0
			})
		case "IS_READABLE":
			v = s.statExists(operand.Val, fs, func(fi os.FileInfo) bool { return fi.Mode().Perm()&0444 != 0 })
		case "IS_WRITABLE":
			v = s.statExists(operand.Val, fs, func(fi os.FileInfo) bool { return fi.Mode().Perm()&0222 != 0 })
		case "IS_EXECUTABLE":
			v = s.statExists(operand.Val, fs, func(fi os.FileInfo) bool { return !fi.IsDir() })
		case "IS_ABSOLUTE":
			v = isAbsolutePath(operand.Val)
		case "COMMAND":
			name := strings.ToLower(operand.Val)
			_, builtin := commands[name]
			_, fn := s.Functions[name]
			_, mac := s.Macros[name]
			v = builtin || fn || mac || controlKeywords[name]
		case "DEFINED":
			v = s.isDefined(operand.Val)
		case "POLICY":
			v = knownPolicy(operand.Val)
		case "TARGET":
			_, v = s.Targets[operand.Val]
		case "TEST":
			for _, t := range s.Tests {
				if t.Name == operand.Val {
					v = true
					break
				}
			}
		}
		out := append([]Arg(nil), args[:i]...)
		out = append(out, truth(v))
		out = append(out, args[i+2:]...)
		args = out
	}
	return args, nil
}

// isDefined implements DEFINED, including its CACHE{} and ENV{} forms.
func (s *State) isDefined(name string) bool {
	switch {
	case strings.HasPrefix(name, "ENV{") && strings.HasSuffix(name, "}"):
		_, ok := s.Env[name[4:len(name)-1]]
		return ok
	case strings.HasPrefix(name, "CACHE{") && strings.HasSuffix(name, "}"):
		_, ok := s.Cache.Get(name[6 : len(name)-1])
		return ok
	default:
		if _, ok := s.Current.Get(name); ok {
			return true
		}
		_, ok := s.Cache.Get(name)
		return ok
	}
}

func (s *State) statExists(path string, fs FS, pred func(os.FileInfo) bool) bool {
	if path == "" {
		return false
	}
	fi, err := fs.Stat(s.absPath(path))
	if err != nil {
		return false
	}
	return pred(fi)
}

// absPath resolves a possibly-relative path against CMAKE_CURRENT_SOURCE_DIR,
// which is what every CMake path predicate does with a relative argument.
func (s *State) absPath(p string) string {
	if p == "" || isAbsolutePath(p) {
		return p
	}
	base := s.GetVar("CMAKE_CURRENT_SOURCE_DIR")
	if base == "" {
		base = s.SourceDir
	}
	return joinPath(base, p)
}

func isAbsolutePath(p string) bool {
	if p == "" {
		return false
	}
	if p[0] == '/' || p[0] == '\\' || p[0] == '~' {
		return true
	}
	// Windows drive letter: C:/ or C:\ — but a bare "C:" is not absolute to CMake.
	if len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		c := p[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}

// ----------------------------------------------------------------------------
// Level 2: binary predicates

var binaryOps = map[string]bool{
	"MATCHES": true, "LESS": true, "GREATER": true, "EQUAL": true,
	"LESS_EQUAL": true, "GREATER_EQUAL": true,
	"STRLESS": true, "STRGREATER": true, "STREQUAL": true,
	"STRLESS_EQUAL": true, "STRGREATER_EQUAL": true,
	"VERSION_LESS": true, "VERSION_GREATER": true, "VERSION_EQUAL": true,
	"VERSION_LESS_EQUAL": true, "VERSION_GREATER_EQUAL": true,
	"IN_LIST": true, "IS_NEWER_THAN": true, "PATH_EQUAL": true,
}

func (s *State) condBinary(args []Arg, fs FS) ([]Arg, error) {
	for i := 1; i+1 < len(args); {
		if args[i].Quoted || !binaryOps[args[i].Val] {
			i++
			continue
		}
		op := args[i].Val
		lhs, rhs := args[i-1], args[i+1]
		v, err := s.applyBinary(op, lhs, rhs, fs)
		if err != nil {
			return nil, err
		}
		out := append([]Arg(nil), args[:i-1]...)
		out = append(out, truth(v))
		out = append(out, args[i+2:]...)
		args = out
		i = 1
	}
	return args, nil
}

func (s *State) applyBinary(op string, lhs, rhs Arg, fs FS) (bool, error) {
	switch op {
	case "MATCHES":
		// The right operand of MATCHES is the regex itself, never dereferenced.
		return s.matchRegex(s.varOrString(lhs), rhs.Val)

	case "LESS", "GREATER", "EQUAL", "LESS_EQUAL", "GREATER_EQUAL":
		l, lok := parseNumberLoose(s.varOrString(lhs))
		r, rok := parseNumberLoose(s.varOrString(rhs))
		if !lok || !rok {
			// CMake yields false rather than erroring on non-numeric operands.
			return false, nil
		}
		switch op {
		case "LESS":
			return l < r, nil
		case "GREATER":
			return l > r, nil
		case "EQUAL":
			return l == r, nil
		case "LESS_EQUAL":
			return l <= r, nil
		default:
			return l >= r, nil
		}

	case "STRLESS", "STRGREATER", "STREQUAL", "STRLESS_EQUAL", "STRGREATER_EQUAL":
		l, r := s.varOrString(lhs), s.varOrString(rhs)
		switch op {
		case "STRLESS":
			return l < r, nil
		case "STRGREATER":
			return l > r, nil
		case "STREQUAL":
			return l == r, nil
		case "STRLESS_EQUAL":
			return l <= r, nil
		default:
			return l >= r, nil
		}

	case "VERSION_LESS", "VERSION_GREATER", "VERSION_EQUAL",
		"VERSION_LESS_EQUAL", "VERSION_GREATER_EQUAL":
		c := CompareVersions(s.varOrString(lhs), s.varOrString(rhs))
		switch op {
		case "VERSION_LESS":
			return c < 0, nil
		case "VERSION_GREATER":
			return c > 0, nil
		case "VERSION_EQUAL":
			return c == 0, nil
		case "VERSION_LESS_EQUAL":
			return c <= 0, nil
		default:
			return c >= 0, nil
		}

	case "IN_LIST":
		// The right operand must name a variable holding the list.
		if !s.isDefined(rhs.Val) {
			return false, nil
		}
		needle := s.varOrString(lhs)
		for _, e := range SplitList(s.GetVar(rhs.Val)) {
			if e == needle {
				return true, nil
			}
		}
		return false, nil

	case "IS_NEWER_THAN":
		l, lerr := fs.Stat(s.absPath(s.varOrString(lhs)))
		r, rerr := fs.Stat(s.absPath(s.varOrString(rhs)))
		// CMake reports true when either file is missing, so that a rule guarded
		// by IS_NEWER_THAN runs when its output has not been produced yet.
		if lerr != nil || rerr != nil {
			return true, nil
		}
		return !l.ModTime().Before(r.ModTime()), nil

	case "PATH_EQUAL":
		return normalizePath(s.varOrString(lhs)) == normalizePath(s.varOrString(rhs)), nil
	}
	return false, nil
}

// matchRegex applies a MATCHES test and publishes CMAKE_MATCH_<n>.
func (s *State) matchRegex(subject, pattern string) (bool, error) {
	re, err := regex.Compile(pattern)
	if err != nil {
		return false, &condRegexError{pattern: pattern}
	}
	m := re.Match(subject)
	setMatchVars(s, m)
	return m != nil, nil
}

// condRegexError is a pattern if() could not compile. It is reported with the
// condition's arguments echoed above it, the way CMake reports every other
// complaint about an if(), so it has to travel up to where those are still
// known rather than being formatted where it is raised.
type condRegexError struct{ pattern string }

func (e *condRegexError) Error() string {
	return "Regular expression \"" + e.pattern + "\" cannot compile"
}

func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return slashPath(filepath.Clean(p))
}

// ----------------------------------------------------------------------------
// Level 3: NOT

func (s *State) condNot(args []Arg) ([]Arg, error) {
	// Left to right, taking the single token that follows as the operand. This
	// is why `if(NOT NOT V)` is an error rather than a double negation: the
	// first NOT consumes the second as a value, and the leftover V has nothing
	// to combine with.
	for i := 0; i+1 < len(args); i++ {
		if args[i].Quoted || args[i].Val != "NOT" {
			continue
		}
		v := s.boolValue(args[i+1])
		out := append([]Arg(nil), args[:i]...)
		out = append(out, truth(!v))
		out = append(out, args[i+2:]...)
		args = out
	}
	return args, nil
}

// ----------------------------------------------------------------------------
// Level 4: AND / OR
//
// CMake gives AND and OR equal precedence and evaluates them left to right,
// so `if(A OR B AND C)` means `((A OR B) AND C)` — not what C programmers
// expect, and a frequent source of surprise in real CMakeLists.txt files.

func (s *State) condAndOr(args []Arg) ([]Arg, error) {
	for i := 1; i+1 < len(args); {
		if args[i].Quoted || (args[i].Val != "AND" && args[i].Val != "OR") {
			i++
			continue
		}
		l := s.boolValue(args[i-1])
		r := s.boolValue(args[i+1])
		v := l && r
		if args[i].Val == "OR" {
			v = l || r
		}
		out := append([]Arg(nil), args[:i-1]...)
		out = append(out, truth(v))
		out = append(out, args[i+2:]...)
		args = out
		i = 1
	}
	return args, nil
}

// ----------------------------------------------------------------------------
// Value interpretation

// boolValue resolves a single condition argument to a boolean, dereferencing
// it as a variable name if it is not itself a recognised constant.
func (s *State) boolValue(a Arg) bool {
	if v, ok := constBool(a.Val); ok {
		return v
	}
	// CMP0054: a quoted argument is never treated as a variable name.
	if a.Quoted {
		return false
	}
	if v, ok := s.Current.Get(a.Val); ok {
		return !isOff(v)
	}
	if e, ok := s.Cache.Get(a.Val); ok {
		return !isOff(e.Value)
	}
	return false
}

// varOrString dereferences an argument for a comparison operand: a defined
// variable yields its value, anything else is used literally.
func (s *State) varOrString(a Arg) string {
	if a.Quoted {
		return a.Val
	}
	if v, ok := s.Current.Get(a.Val); ok {
		return v
	}
	if e, ok := s.Cache.Get(a.Val); ok {
		return e.Value
	}
	return a.Val
}

// constBool reports whether v is one of CMake's boolean constants.
func constBool(v string) (val, isConst bool) {
	if isOn(v) {
		return true, true
	}
	if isOff(v) {
		return false, true
	}
	if n, ok := parseNumber(v); ok {
		return n != 0, true
	}
	return false, false
}

// IsOn reports whether a value is one of CMake's true constants.
func IsOn(v string) bool { return isOn(v) }

// IsOff reports whether a value is one of CMake's false constants.
func IsOff(v string) bool { return isOff(v) }

func isOn(v string) bool {
	switch strings.ToUpper(v) {
	case "1", "ON", "YES", "TRUE", "Y":
		return true
	}
	if n, ok := parseNumber(v); ok {
		return n != 0
	}
	return false
}

func isOff(v string) bool {
	u := strings.ToUpper(v)
	switch u {
	case "", "0", "OFF", "NO", "FALSE", "N", "IGNORE", "NOTFOUND":
		return true
	}
	if strings.HasSuffix(u, "-NOTFOUND") {
		return true
	}
	if n, ok := parseNumber(v); ok {
		return n == 0
	}
	return false
}

// parseNumber parses a CMake numeric literal for a truth test. CMake reaches
// this through strtod, which skips leading whitespace but then requires the
// rest of the argument to be consumed: " 1" is the number one, while "5 " is
// not a number at all and falls through to being read as a variable name.
func parseNumber(v string) (float64, bool) {
	t := strings.TrimLeft(v, " \t\n\r\v\f")
	n, used := scanNumber(t)
	return n, used == len(t) && used > 0
}

// parseNumberLoose parses the leading number of a string, which is what the
// numeric comparison operators do: they read their operands with a scanf-style
// conversion that stops at the first character it cannot use, so "5abc" is 5.
func parseNumberLoose(v string) (float64, bool) {
	t := strings.TrimLeft(v, " \t\n\r\v\f")
	n, used := scanNumber(t)
	return n, used > 0
}

// scanNumber reads a numeric prefix and reports how many bytes it consumed.
func scanNumber(t string) (float64, int) {
	if t == "" {
		return 0, 0
	}
	neg := false
	i := 0
	if t[i] == '+' || t[i] == '-' {
		neg = t[i] == '-'
		i++
	}
	if strings.HasPrefix(t[i:], "0x") || strings.HasPrefix(t[i:], "0X") {
		j := i + 2
		for j < len(t) && isHexDigit(t[j]) {
			j++
		}
		if j > i+2 {
			n, err := strconv.ParseInt(t[i+2:j], 16, 64)
			if err == nil {
				if neg {
					return float64(-n), j
				}
				return float64(n), j
			}
		}
	}
	// Find the longest prefix that parses as a float.
	best, bestEnd := 0.0, 0
	for j := i + 1; j <= len(t); j++ {
		n, err := strconv.ParseFloat(t[:j], 64)
		if err == nil {
			best, bestEnd = n, j
		}
	}
	return best, bestEnd
}

// CompareVersions compares two dotted version strings component-wise, treating
// missing components as zero. It returns -1, 0, or 1.
func CompareVersions(a, b string) int {
	av, bv := versionComponents(a), versionComponents(b)
	n := len(av)
	if len(bv) > n {
		n = len(bv)
	}
	for i := 0; i < n; i++ {
		var x, y int64
		if i < len(av) {
			x = av[i]
		}
		if i < len(bv) {
			y = bv[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

// versionComponents extracts the leading numeric components of a version
// string, stopping at the first component that is not a number.
func versionComponents(v string) []int64 {
	var out []int64
	for _, part := range strings.Split(v, ".") {
		// Take the leading digits; "1.2rc3" compares as 1.2.
		end := 0
		for end < len(part) && part[end] >= '0' && part[end] <= '9' {
			end++
		}
		if end == 0 {
			break
		}
		n, err := strconv.ParseInt(part[:end], 10, 64)
		if err != nil {
			break
		}
		out = append(out, n)
		if end != len(part) {
			break
		}
	}
	return out
}
