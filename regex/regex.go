// Package regex implements CMake's regular expressions.
//
// CMake does not use POSIX, PCRE, or Go's engine. It carries a descendant of
// Henry Spencer's 1986 matcher, and its grammar is smaller than any of them:
// the only operators are ^ $ . [ ] * + ? | ( ), and a backslash always means
// "the following character, literally".
//
// That last rule is why this package exists. Handing a CMake pattern to Go's
// regexp package compiles, so nothing looks wrong -- but \d, \w, \s and \b
// become character classes CMake does not have, {2} becomes a repetition CMake
// reads as three literal characters, [[:digit:]] becomes a POSIX class, and
// a+? becomes a lazy quantifier that CMake refuses outright. Every one of those
// is a silent wrong answer in the middle of somebody's build, and a wrong
// answer is worse than a failure because nobody goes looking for it.
//
// So a pattern is translated here rather than passed through: each construct is
// read with CMake's meaning and re-emitted with the same meaning in Go's
// dialect. Everything CMake treats as an ordinary character is escaped, and the
// constructs CMake rejects are rejected with the message it prints.
//
// The behaviour recorded here was obtained by running patterns through the
// cmake binary, not by reading its documentation. Where the two disagree the
// binary wins; the comments below note the places they do.
package regex

import (
	"strings"
	"sync"

	"regexp"
)

// Regexp is a compiled CMake regular expression.
type Regexp struct {
	re      *regexp.Regexp
	pattern string
}

// Pattern returns the CMake pattern this was compiled from, which is what the
// error messages quote back to the user.
func (r *Regexp) Pattern() string { return r.pattern }

// Groups is the number of capturing groups.
func (r *Regexp) Groups() int { return r.re.NumSubexp() }

var (
	cacheMu sync.Mutex
	cache   = map[string]*Regexp{}
)

// Compile translates a CMake regular expression and compiles it.
//
// Compiled patterns are cached: a project's regular expressions are a small
// fixed set applied to a great many strings, and if(MATCHES) inside a loop is
// an ordinary thing to write.
func Compile(pattern string) (*Regexp, error) {
	cacheMu.Lock()
	if r, ok := cache[pattern]; ok {
		cacheMu.Unlock()
		return r, nil
	}
	cacheMu.Unlock()

	translated, err := translate(pattern)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(translated)
	if err != nil {
		// Reaching here means the translation is wrong rather than the pattern:
		// everything CMake accepts has an equivalent Go accepts. Report the
		// pattern the user wrote, since the translated form would only confuse.
		return nil, &Error{Pattern: pattern, Reason: err.Error()}
	}
	r := &Regexp{re: re, pattern: pattern}
	cacheMu.Lock()
	cache[pattern] = r
	cacheMu.Unlock()
	return r, nil
}

// Error is a pattern CMake's engine refuses. The reason text is the one CMake
// prints, so a project that hits it sees the message it would have seen.
type Error struct {
	Pattern string
	Reason  string
}

func (e *Error) Error() string { return e.Reason }

// The refusals, in CMake's own words.
const (
	reasonTrailingBackslash = "Trailing backslash"
	reasonFollowsNothing    = "?+* follows nothing"
	reasonNested            = "Nested *?+"
	reasonEmptyOperand      = "*+ operand could be empty"
	reasonUnmatchedBracket  = "Unmatched []"
	reasonUnmatchedParen    = "Unmatched parentheses"
	reasonInvalidRange      = "Invalid range in []"
)

// what the previous token was, which is all the state the refusals need.
type prev int

const (
	prevNothing prev = iota // start of pattern, or just after ( or |
	prevAtom                // something a quantifier may be applied to
	prevAnchor              // ^ or $, which quantify to an empty operand
	prevQuant               // * + ?, which may not be quantified again
)

// translate rewrites a CMake pattern as an equivalent Go one.
func translate(pattern string) (string, error) {
	var b strings.Builder

	// CMake's '.' matches a newline; Go's does not unless asked. Every other
	// flag stays at Go's default, which already agrees: '^' and '$' anchor the
	// whole string, and '$' does not match before a trailing newline.
	b.WriteString("(?s)")

	fail := func(reason string) (string, error) {
		return "", &Error{Pattern: pattern, Reason: reason}
	}

	depth := 0
	last := prevNothing
	for i := 0; i < len(pattern); {
		switch c := pattern[i]; c {
		case '\\':
			// A backslash is never anything but "the next character". There are
			// no escape sequences: \t is a t, \d is a d, \1 is a 1.
			if i+1 >= len(pattern) {
				return fail(reasonTrailingBackslash)
			}
			b.WriteString(quoteByte(pattern[i+1]))
			i += 2
			last = prevAtom

		case '[':
			width, class, err := translateClass(pattern, i)
			if err != nil {
				return "", &Error{Pattern: pattern, Reason: err.Error()}
			}
			b.WriteString(class)
			i += width
			last = prevAtom

		case '(':
			b.WriteByte('(')
			depth++
			i++
			last = prevNothing

		case ')':
			if depth == 0 {
				return fail(reasonUnmatchedParen)
			}
			b.WriteByte(')')
			depth--
			i++
			last = prevAtom

		case '|':
			b.WriteByte('|')
			i++
			last = prevNothing

		case '*', '+', '?':
			switch last {
			case prevQuant:
				return fail(reasonNested)
			case prevNothing:
				return fail(reasonFollowsNothing)
			case prevAnchor:
				// ^* and a$* are refused: the thing being repeated matches no
				// characters, so repeating it could loop forever.
				return fail(reasonEmptyOperand)
			}
			b.WriteByte(c)
			i++
			last = prevQuant

		case '^', '$':
			b.WriteByte(c)
			i++
			last = prevAnchor

		case '.':
			b.WriteByte('.')
			i++
			last = prevAtom

		default:
			b.WriteString(quoteByte(c))
			i++
			last = prevAtom
		}
	}
	if depth != 0 {
		return fail(reasonUnmatchedParen)
	}
	return b.String(), nil
}

// translateClass rewrites one bracket expression, returning how much of the
// pattern it consumed.
//
// CMake's rules here are the old ones: a ']' is a member when it comes first,
// a '-' is a member when it comes first or last, and a backslash is a member
// like any other character -- "[/\]" matches a slash or a backslash, which is
// the idiom every Windows-aware project uses.
func translateClass(pattern string, start int) (int, string, error) {
	var b strings.Builder
	b.WriteByte('[')

	i := start + 1
	if i < len(pattern) && pattern[i] == '^' {
		b.WriteByte('^')
		i++
	}

	for first := true; ; first = false {
		if i >= len(pattern) {
			return 0, "", &Error{Pattern: pattern, Reason: reasonUnmatchedBracket}
		}
		if pattern[i] == ']' && !first {
			break
		}
		lo := pattern[i]
		i++
		// A '-' is a range only with something on both sides of it; before the
		// closing bracket, or at the end of the pattern, it is a member.
		if i+1 < len(pattern) && pattern[i] == '-' && pattern[i+1] != ']' {
			hi := pattern[i+1]
			if hi < lo {
				return 0, "", &Error{Pattern: pattern, Reason: reasonInvalidRange}
			}
			b.WriteString(quoteClassByte(lo))
			b.WriteByte('-')
			b.WriteString(quoteClassByte(hi))
			i += 2
			continue
		}
		b.WriteString(quoteClassByte(lo))
	}
	b.WriteByte(']')
	return i + 1 - start, b.String(), nil
}

// quoteByte emits one byte as a Go pattern matching exactly that byte.
//
// The byte, not the rune: CMake's engine works on bytes, so a multi-byte
// character has to be reassembled from its parts rather than widened into a
// rune, which string(byte) would do.
func quoteByte(c byte) string {
	if strings.IndexByte(`\.+*?()|[]{}^$`, c) >= 0 {
		return `\` + string(rune(c))
	}
	return string([]byte{c})
}

// quoteClassByte is the same for a byte inside a bracket expression, where a
// different set of characters is special.
func quoteClassByte(c byte) string {
	if strings.IndexByte(`\]^-[&~`, c) >= 0 {
		return `\` + string(rune(c))
	}
	return string([]byte{c})
}
