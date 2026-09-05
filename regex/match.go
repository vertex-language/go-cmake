package regex

import "strings"

// Match returns the leftmost match and its groups, or nil if there is none.
// Element 0 is the whole match; a group that did not participate is empty.
func (r *Regexp) Match(s string) []string {
	m := r.re.FindStringSubmatchIndex(s)
	if m == nil {
		return nil
	}
	return groupsOf(s, m)
}

// MatchString reports whether the expression matches anywhere in s.
func (r *Regexp) MatchString(s string) bool { return r.re.MatchString(s) }

// MatchAll returns every match, in the order CMake produces them.
//
// This is not Go's FindAll. CMake finds one match, cuts the subject after it,
// and searches the remainder from the beginning -- so '^' re-anchors at every
// step, and MATCHALL of "^a" against "aaa" yields three matches rather than
// one. An empty match advances by a single character, so a pattern that can
// match nothing produces one result per position rather than being skipped.
func (r *Regexp) MatchAll(s string) [][]string {
	var out [][]string
	r.walk(s, func(_ string, groups []string) { out = append(out, groups) })
	return out
}

// Replace rewrites every match, in the same order and by the same rule.
func (r *Regexp) Replace(s string, with Replacement) string {
	var b strings.Builder
	rest := r.walk(s, func(before string, groups []string) {
		b.WriteString(before)
		with.expand(&b, groups)
	})
	b.WriteString(rest)
	return b.String()
}

// walk drives the find-and-cut loop both operations share. It calls visit with
// the text skipped before each match and that match's groups, and returns
// whatever is left over.
func (r *Regexp) walk(s string, visit func(before string, groups []string)) string {
	// Text that has been passed over but not yet handed to a caller: it belongs
	// in front of whatever match comes next.
	var pending strings.Builder
	for {
		m := r.re.FindStringSubmatchIndex(s)
		if m == nil {
			return pending.String() + s
		}
		start, end := m[0], m[1]
		pending.WriteString(s[:start])
		visit(pending.String(), groupsOf(s, m))
		pending.Reset()
		if start != end {
			s = s[end:]
			continue
		}
		// An empty match consumes nothing, so something else has to move or the
		// loop never ends: the character it sat in front of is carried forward
		// and the search resumes after it. When there is no such character the
		// subject is exhausted and this was the last match.
		if start >= len(s) {
			return ""
		}
		pending.WriteString(s[start : start+1])
		s = s[start+1:]
	}
}

// groupsOf turns index pairs into strings, with a non-participating group
// reported as empty rather than absent -- which is what CMake stores.
func groupsOf(s string, m []int) []string {
	out := make([]string, len(m)/2)
	for i := range out {
		if m[2*i] >= 0 {
			out[i] = s[m[2*i]:m[2*i+1]]
		}
	}
	return out
}

// Replacement is a parsed REGEX REPLACE replacement expression.
type Replacement []replPart

// replPart is either literal text or a group reference.
type replPart struct {
	text  string
	group int // -1 for literal text
}

// ParseReplacement reads a replacement expression.
//
// The escapes CMake allows here are \0 through \9, \\ and \n, and nothing
// else: \t and \r are rejected by name even though the CMake language has
// them, because by the time this string arrives the language has already had
// its turn at it. A "$1" is ordinary text, unlike in Go.
func ParseReplacement(s string) (Replacement, error) {
	var out Replacement
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, replPart{text: lit.String(), group: -1})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			lit.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			return nil, &ReplacementError{Reason: "replace-expression ends in a backslash"}
		}
		switch c := s[i+1]; {
		case c >= '0' && c <= '9':
			flush()
			out = append(out, replPart{group: int(c - '0')})
		case c == '\\':
			lit.WriteByte('\\')
		case c == 'n':
			lit.WriteByte('\n')
		default:
			return nil, &ReplacementError{Reason: `Unknown escape "\` + string(rune(c)) + `" in replace-expression`}
		}
		i++
	}
	flush()
	return out, nil
}

// ReplacementError is a replacement expression CMake refuses.
type ReplacementError struct{ Reason string }

func (e *ReplacementError) Error() string { return e.Reason }

// expand writes one match through the replacement. A group the pattern does
// not have, or one that did not participate, contributes nothing.
func (r Replacement) expand(b *strings.Builder, groups []string) {
	for _, part := range r {
		if part.group < 0 {
			b.WriteString(part.text)
			continue
		}
		if part.group < len(groups) {
			b.WriteString(groups[part.group])
		}
	}
}
