package regex_test

import (
	"strings"
	"testing"

	"github.com/vertex-language/go-cmake/regex"
)

// Every expectation in this file was produced by running the pattern through
// the cmake binary and writing down what came back. Where a case is marked
// "Go would say", that is the answer this package existed to avoid: the
// pattern compiles in Go's regexp too, and means something else.

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern string
		subject string
		want    string // the whole match, or "-" for no match
		note    string
	}{
		// Repetition counts are not repetition counts. Go would match "aa".
		{`a{2}`, "aa", "-", "{n} is three ordinary characters"},
		{`x{1}`, "x{1}", "x{1}", ""},
		{`[0-9]{4}`, "2024", "-", "the year-matching pattern people expect to work"},
		{`a{`, "a{", "a{", ""},

		// A backslash introduces no class, ever. Go would match "123", "ab_c",
		// "a b" and "foo".
		{`\d+`, "abc123", "-", `\d is a d`},
		{`\d`, "xdx", "d", ""},
		{`\w+`, "ab_c", "-", `\w is a w`},
		{`\s`, "a b", "-", `\s is an s`},
		{`\bfoo`, "a foo", "-", `\b is a b`},
		{`\tx`, "\tx", "-", `\t is a t; the language, not the regex, makes tabs`},
		{`\tx`, "tx", "tx", ""},
		{`(a)\1`, "aa", "-", `\1 is a 1, not a backreference`},
		{`a\.b`, "axb", "-", ""},
		{`a\.b`, "a.b", "a.b", ""},
		{`a\+b`, "a+b", "a+b", ""},
		{`\(x\)`, "(x)", "(x)", ""},

		// POSIX classes are not classes either. Go would match "123".
		{`[[:digit:]]+`, "ab123", "-", "[:digit:] is a set of eight characters"},
		{`[[:alnum:]]+`, "..ab12..", "-", ""},

		// Bracket expressions follow the old rules, which Go rejects outright.
		{`[]ab]`, "]", "]", "a leading ] is a member"},
		{`[^]ab]`, "]", "-", ""},
		{`[^]ab]`, "z", "z", ""},
		{`[a-]`, "-", "-", "a trailing - is a member"},
		{`[-a]`, "-", "-", "a leading - is a member"},
		{`[a^]`, "^", "^", ""},
		{`[/\]`, "x\\y", "\\", "the idiom for either kind of separator"},
		{`[/\]`, "x/y", "/", ""},
		{`[\t]`, "\t", "-", "a backslash in a class is a backslash"},
		{`[\t]`, "t", "t", ""},
		{`[\t]`, "\\", "\\", ""},
		{`[\-a]`, "-", "-", `\ to a is a range that excludes -`},
		{`[a-c]+`, "abcd", "abc", ""},
		{`[^a]+`, "aaabbb", "bbb", ""},
		{`[^]]+`, "ab]", "ab", ""},

		// Anchors and the dot.
		{`a.b`, "a\nb", "a\nb", "'.' matches a newline, unlike Go's default"},
		{`a$`, "a\n", "-", "'$' does not match before a trailing newline"},
		{`^b`, "a\nb", "-", "'^' is not multi-line"},
		{`a$`, "ba", "a", ""},
		{`^a`, "ab", "a", ""},
		{`a^b`, "a^b", "-", "'^' is an anchor everywhere, so this cannot match"},
		{`a$b`, "a$b", "-", ""},

		// The operators that are operators.
		{`(ab)+`, "ababab", "ababab", ""},
		{`cat|dog`, "hotdog", "dog", ""},
		{`^(ab|cd)$`, "abd", "-", ""},
		{`^ab+d$`, "abbd", "abbd", ""},
		{`^ab+d$`, "ababd", "-", ""},
		{`colou?r`, "color", "color", ""},
		{`<.*>`, "<a><b>", "<a><b>", "greedy, and there is no way to ask otherwise"},
		{`()a`, "a", "a", ""},
		{`(|a)b`, "b", "b", ""},
		{`(ab)*c`, "c", "c", ""},

		// Case matters.
		{`ABC`, "abc", "-", ""},
	} {
		re, err := regex.Compile(c.pattern)
		if err != nil {
			t.Errorf("%q: %v", c.pattern, err)
			continue
		}
		got := "-"
		if m := re.Match(c.subject); m != nil {
			got = m[0]
		}
		if got != c.want {
			t.Errorf("%q against %q = %q, want %q  (%s)", c.pattern, c.subject, got, c.want, c.note)
		}
	}
}

// TestMatchIsNotZeroLengthAverse records that a pattern matching nothing does
// match: CMake reports the empty match rather than looking for a longer one.
func TestEmptyMatch(t *testing.T) {
	re, err := regex.Compile(`q*`)
	if err != nil {
		t.Fatal(err)
	}
	m := re.Match("ab")
	if m == nil || m[0] != "" {
		t.Errorf("q* against \"ab\" = %v, want an empty match", m)
	}
}

func TestGroups(t *testing.T) {
	re, err := regex.Compile(`((a)(b))`)
	if err != nil {
		t.Fatal(err)
	}
	m := re.Match("ab")
	if got := strings.Join(m, ","); got != "ab,ab,a,b" {
		t.Errorf("groups = %q", got)
	}

	// A group that did not participate is empty, not missing.
	re, err = regex.Compile(`(a)|(z)`)
	if err != nil {
		t.Fatal(err)
	}
	if m := re.Match("a"); len(m) != 3 || m[2] != "" {
		t.Errorf("groups = %q, want the second one empty", m)
	}
}

// TestRefusals covers the patterns CMake will not compile. Three of them --
// the lazy quantifiers and the non-capturing group -- compile in Go and mean
// something CMake has no way to express, so accepting them would be worse than
// failing.
func TestRefusals(t *testing.T) {
	for _, c := range []struct{ pattern, reason string }{
		{`a.*?b`, "Nested *?+"},
		{`a+?`, "Nested *?+"},
		{`a**`, "Nested *?+"},
		{`(?:ab)+`, "?+* follows nothing"},
		{`?a`, "?+* follows nothing"},
		{`(+a)`, "?+* follows nothing"},
		{`a|*b`, "?+* follows nothing"},
		{`^*`, "*+ operand could be empty"},
		{`a$*`, "*+ operand could be empty"},
		{`[abc`, "Unmatched []"},
		{`[]`, "Unmatched []"},
		{`[^]`, "Unmatched []"},
		{`(ab`, "Unmatched parentheses"},
		{`ab)`, "Unmatched parentheses"},
		{`[z-a]`, "Invalid range in []"},
		{`\`, "Trailing backslash"},
	} {
		_, err := regex.Compile(c.pattern)
		if err == nil {
			t.Errorf("%q compiled; cmake refuses it with %q", c.pattern, c.reason)
			continue
		}
		if err.Error() != c.reason {
			t.Errorf("%q: error is %q, want %q", c.pattern, err, c.reason)
		}
	}
}

// TestMatchAll covers the loop, which is the part Go's FindAll does
// differently: CMake cuts the subject and searches the remainder from its
// start, so an anchor applies again each time.
func TestMatchAll(t *testing.T) {
	for _, c := range []struct{ pattern, subject, want string }{
		{`^a`, "aaa", "a|a|a"},  // Go's FindAll would say "a"
		{`a*`, "bab", "|a||"},   // Go's FindAll would say "|a|"
		{`a`, "bab", "a"},       //
		{`aa`, "aaaa", "aa|aa"}, // matches do not overlap
		{`a$`, "aa", "a"},       //
		{`$`, "ab", ""},         // one empty match, at the end
		{`(a)(b)`, "abab", "ab|ab"},
		{`zzz`, "ab", ""},
	} {
		re, err := regex.Compile(c.pattern)
		if err != nil {
			t.Errorf("%q: %v", c.pattern, err)
			continue
		}
		var got []string
		for _, m := range re.MatchAll(c.subject) {
			got = append(got, m[0])
		}
		if strings.Join(got, "|") != c.want {
			t.Errorf("MatchAll(%q, %q) = %q, want %q", c.pattern, c.subject, got, c.want)
		}
	}
}

func TestReplace(t *testing.T) {
	for _, c := range []struct{ pattern, with, subject, want string }{
		{`^a`, "-", "aaa", "---"},    // Go would say "-aa"
		{`a*`, "-", "bab", "-b--b-"}, // Go would say "-b-b-"
		{`x*`, "-", "ab", "-a-b-"},
		{`(a)(b)`, `\2\1`, "ab", "ba"},
		{`ab`, `<\0>`, "ab", "<ab>"},
		{`ab`, `x\\y`, "ab", `x\y`},
		{`ab`, "x\\ny", "ab", "x\ny"},
		{`(a)|(z)`, `<\2>`, "a", "<>"},
		{`ab`, `$1`, "ab", "$1"}, // Go would expand it
	} {
		re, err := regex.Compile(c.pattern)
		if err != nil {
			t.Errorf("%q: %v", c.pattern, err)
			continue
		}
		with, err := regex.ParseReplacement(c.with)
		if err != nil {
			t.Errorf("%q: %v", c.with, err)
			continue
		}
		if got := re.Replace(c.subject, with); got != c.want {
			t.Errorf("Replace(%q, %q, %q) = %q, want %q", c.pattern, c.with, c.subject, got, c.want)
		}
	}
}

func TestReplacementRefusals(t *testing.T) {
	for _, c := range []struct{ with, reason string }{
		{`x\ty`, `Unknown escape "\t" in replace-expression`},
		{`x\ry`, `Unknown escape "\r" in replace-expression`},
		{`x\qy`, `Unknown escape "\q" in replace-expression`},
		{`x\;y`, `Unknown escape "\;" in replace-expression`},
		{`x\`, "replace-expression ends in a backslash"},
	} {
		_, err := regex.ParseReplacement(c.with)
		if err == nil {
			t.Errorf("%q was accepted; cmake refuses it", c.with)
			continue
		}
		if err.Error() != c.reason {
			t.Errorf("%q: error is %q, want %q", c.with, err, c.reason)
		}
	}
}

// TestMultiByteSubjectsSurvive covers the reassembly: CMake's engine works on
// bytes, so a translated pattern has to put a multi-byte character back
// together rather than widening each byte into a character of its own.
func TestMultiByteSubjectsSurvive(t *testing.T) {
	re, err := regex.Compile(`caf\é`)
	if err != nil {
		t.Fatal(err)
	}
	if m := re.Match("a café here"); m == nil || m[0] != "café" {
		t.Errorf("match = %v", m)
	}
}
