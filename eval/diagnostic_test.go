package eval

import "testing"

// Each want here is the exact output of the cmake binary for the same message,
// captured with `cmake -P` and copied in whole, trailing blank line included.

func TestDiagnosticLayout(t *testing.T) {
	const banner = "CMake Error at c.cmake:1 (message)"
	for _, c := range []struct{ name, text, want string }{
		{
			"filled to the column, with two spaces after a sentence",
			"alpha beta. gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau upsilon phi chi psi omega",
			"CMake Error at c.cmake:1 (message):\n" +
				"  alpha beta.  gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi\n" +
				"  omicron pi rho sigma tau upsilon phi chi psi omega\n",
		},
		{
			"a newline becomes a blank line",
			"a\nb",
			"CMake Error at c.cmake:1 (message):\n  a\n\n  b\n",
		},
		{
			"and two newlines become three",
			"a\n\nb",
			"CMake Error at c.cmake:1 (message):\n  a\n\n\n\n  b\n",
		},
		{
			"a trailing newline leaves an empty paragraph",
			"a\n",
			"CMake Error at c.cmake:1 (message):\n  a\n\n\n",
		},
		{
			"a leading one does the same",
			"\na",
			"CMake Error at c.cmake:1 (message):\n\n\n  a\n",
		},
		{
			"an indented line keeps its own spacing",
			"a\n  b\nc",
			"CMake Error at c.cmake:1 (message):\n  a\n\n    b\n\n  c\n",
		},
		{
			"a block of them is one unit",
			"head:\n\n  indented one\n  indented two\n\ntail words here",
			"CMake Error at c.cmake:1 (message):\n  head:\n\n\n\n    indented one\n    indented two\n\n\n\n  tail words here\n",
		},
		{
			"the shape every if() complaint has",
			`if given arguments:` + "\n" + `  "a" "MATCHES" "a**"` + "\n" + `Regular expression "a**" cannot compile`,
			"CMake Error at c.cmake:1 (message):\n  if given arguments:\n\n    \"a\" \"MATCHES\" \"a**\"\n\n  Regular expression \"a**\" cannot compile\n",
		},
	} {
		if got := Diagnostic(banner, c.text); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// TestDiagnosticWrapsAtTheSameWord pins the column itself: nineteen
// three-letter words fit on a line and the twentieth does not.
func TestDiagnosticWrapsAtTheSameWord(t *testing.T) {
	var words []string
	for i := 1; i <= 39; i++ {
		words = append(words, string(rune('w'))+string(rune('0'+i/10))+string(rune('0'+i%10)))
	}
	got := Diagnostic("b", join(words, " "))
	want := "b:\n" +
		"  w01 w02 w03 w04 w05 w06 w07 w08 w09 w10 w11 w12 w13 w14 w15 w16 w17 w18 w19\n" +
		"  w20 w21 w22 w23 w24 w25 w26 w27 w28 w29 w30 w31 w32 w33 w34 w35 w36 w37 w38\n" +
		"  w39\n"
	if got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
