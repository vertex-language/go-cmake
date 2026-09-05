// Package expr implements CMake argument expansion.
//
// CMake has three kinds of argument expansion that happen at different times:
//
//  1. Variable references: ${VAR}, $ENV{VAR}, $CACHE{VAR}, nested ${outer_${inner}_var}
//  2. Escape sequences: \n \t \r \\ \; and \<char> for identity
//  3. Semicolon list splitting: unquoted args are split on ';' into multiple values
//
// Generator expressions ($<...>) are parsed into a [GenexNode] tree here but
// evaluated later by the target package once the full target graph is known.
//
// The primary entry points are [ExpandArg] and [ExpandArgs].
package expr

import (
	"strings"

	"github.com/vertex-language/go-cmake/ast"
)

// Lookup resolves a variable reference. kind is one of "normal", "env", or "cache".
// Returns the value and whether the variable was set (empty string + false means unset).
type Lookup func(kind, name string) (value string, ok bool)

// ExpandArg expands a single CMake argument node into one or more string values.
//
//   - BracketArg: returned as a single verbatim string (no expansion)
//   - QuotedArg: variable refs and escapes expanded, always one result
//   - UnquotedArg: variable refs and escapes expanded, then split on ';'
//   - InlineSpace, InlineComment, etc.: skipped (return nil)
func ExpandArg(arg ast.Arg, lookup Lookup) []string {
	switch a := arg.(type) {
	case *ast.BracketArg:
		return []string{a.Content()}

	case *ast.QuotedArg:
		s := expandString(a.Inner(), lookup, false)
		return []string{s}

	case *ast.UnquotedArg:
		s := expandString(a.Lit, lookup, true)
		// Split on unescaped semicolons.
		parts := SplitList(s)
		// Filter empty strings (unquoted empty args vanish).
		out := parts[:0]
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return out

	default:
		// InlineSpace, InlineComment, InlineBracketComment — not semantic args.
		return nil
	}
}

// ExpandArgs expands a slice of Arg nodes and concatenates all results.
// Whitespace/comment nodes in the slice are silently skipped.
func ExpandArgs(args []ast.Arg, lookup Lookup) []string {
	var out []string
	for _, a := range args {
		out = append(out, ExpandArg(a, lookup)...)
	}
	return out
}

// ExpandString expands a raw string (the inner text of a quoted or unquoted arg)
// performing variable reference and escape sequence substitution.
// If splitSemicolon is true (unquoted context), the result is semicolon-split.
func ExpandString(s string, lookup Lookup) string {
	return expandString(s, lookup, false)
}

// ExpandUnquoted expands a raw unquoted string and returns the resulting list elements.
func ExpandUnquoted(s string, lookup Lookup) []string {
	expanded := expandString(s, lookup, true)
	return SplitList(expanded)
}

// ----------------------------------------------------------------------------
// Internal expansion

// expandString walks s character by character, expanding ${VAR}, $ENV{VAR},
// $CACHE{VAR}, escape sequences, and producing a single expanded string.
// genexes are preserved verbatim as $<...> (they are opaque at this stage).
func expandString(s string, lookup Lookup, _ bool) string {
	if !strings.ContainsAny(s, "$\\") {
		return s // fast path: nothing to expand
	}

	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		ch := s[i]
		switch ch {
		case '\\':
			i++
			if i >= len(s) {
				b.WriteByte('\\')
				break
			}
			next := s[i]
			i++
			switch next {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case ';':
				// \; inside variable references becomes literal ';';
				// outside variable refs it encodes ';' without splitting.
				// We encode it as the special marker that splitList understands.
				b.WriteString("\\;")
			default:
				// Identity escape: \X → X for any non-alphanumeric X.
				b.WriteByte(next)
			}

		case '$':
			if i+1 >= len(s) {
				b.WriteByte('$')
				i++
				break
			}
			switch s[i+1] {
			case '{':
				// Normal variable reference: ${name}
				val, end := expandVarRef(s, i+2, "}", lookup, "normal")
				b.WriteString(val)
				i = end

			case 'E':
				// $ENV{name}
				if strings.HasPrefix(s[i:], "$ENV{") {
					val, end := expandVarRef(s, i+5, "}", lookup, "env")
					b.WriteString(val)
					i = end
				} else {
					b.WriteByte('$')
					i++
				}

			case 'C':
				// $CACHE{name}
				if strings.HasPrefix(s[i:], "$CACHE{") {
					val, end := expandVarRef(s, i+7, "}", lookup, "cache")
					b.WriteString(val)
					i = end
				} else {
					b.WriteByte('$')
					i++
				}

			case '<':
				// A generator expression is opaque to *evaluation*, not to
				// variable substitution: CMake expands ${VAR} inside it during
				// configure and leaves only the $<...> structure for the
				// generator. Copying the whole thing verbatim would leave a
				// literal "${CMAKE_CURRENT_SOURCE_DIR}" in the compile line.
				// So "$<" is emitted as two ordinary characters and the loop
				// continues, expanding what is inside.
				b.WriteString("$<")
				i += 2

			default:
				b.WriteByte('$')
				i++
			}

		default:
			b.WriteByte(ch)
			i++
		}
	}
	return b.String()
}

// expandVarRef resolves a variable reference starting after the opening delimiter.
// s[start:] is the content after the '{'. closer is "}".
// Returns the resolved value and the index just past the closing '}'.
func expandVarRef(s string, start int, closer string, lookup Lookup, kind string) (string, int) {
	// The variable name itself may contain nested ${...} references.
	// We expand the name first (inside-out evaluation).
	var name strings.Builder
	i := start
	depth := 1
	for i < len(s) && depth > 0 {
		ch := s[i]
		if ch == '$' && i+1 < len(s) && s[i+1] == '{' {
			// Nested reference inside variable name.
			inner, end := expandVarRef(s, i+2, "}", lookup, "normal")
			name.WriteString(inner)
			i = end
			continue
		}
		if ch == '}' {
			depth--
			if depth == 0 {
				i++ // consume '}'
				break
			}
		}
		if ch == '{' {
			depth++
		}
		name.WriteByte(ch)
		i++
	}

	resolvedName := name.String()
	val, _ := lookup(kind, resolvedName)
	return val, i
}

// scanGenex finds the end of a generator expression starting at s[start]
// where s[start] == '$' and s[start+1] == '<'.
// Returns the index just past the closing '>'.
// Handles nesting of $<...> expressions.
func scanGenex(s string, start int) int {
	if start >= len(s) || s[start] != '$' {
		return start + 1
	}
	i := start + 2 // skip '$<'
	depth := 1
	for i < len(s) && depth > 0 {
		switch s[i] {
		case '$':
			if i+1 < len(s) && s[i+1] == '<' {
				depth++
				i += 2
				continue
			}
		case '>':
			depth--
			i++
			continue
		}
		i++
	}
	return i
}

// SplitList splits a CMake list value into its elements on unescaped
// semicolons; the sequence \; is a literal semicolon within an element. An
// empty string is an empty list, not a list holding one empty element.
//
// This and [JoinList] are the whole of CMake's list type: a list is a string,
// and these two functions are what make it behave like a sequence. They live
// here, with the rest of the argument syntax, so that there is one answer to
// what a semicolon means.
func SplitList(s string) []string {
	if s == "" {
		return nil
	}
	var parts []string
	var cur strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == ';' {
			// Escaped semicolon — becomes literal ';' in element.
			cur.WriteByte(';')
			i += 2
			continue
		}
		if s[i] == ';' {
			parts = append(parts, cur.String())
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	parts = append(parts, cur.String())
	return parts
}

// JoinList joins a slice of strings into a CMake list (semicolon-separated).
func JoinList(elems []string) string {
	return strings.Join(elems, ";")
}
