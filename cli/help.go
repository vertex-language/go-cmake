package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/vertex-language/go-cmake/eval"
)

// CMake's --help family is what shell completions, editor plugins, and
// documentation tooling call. Answering "unknown option" to any of them turns a
// missing manual into a broken install, so each one is answered: with the real
// list where this implementation knows it, and with a plain statement where it
// does not.

// helpModes maps a --help-* option to what answers it. A nil answer means the
// information exists in CMake's shipped documentation, which this
// implementation does not carry.
var helpModes = map[string]func(w io.Writer) int{
	"--help-command-list": func(w io.Writer) int {
		for _, name := range allCommandNames() {
			fmt.Fprintln(w, name)
		}
		return 0
	},
	"--help-manual-list": func(w io.Writer) int {
		// The one manual this implementation does carry is its own grammar.
		fmt.Fprintln(w, "cmake-language")
		return 0
	},
	"--help-policy-list": func(w io.Writer) int {
		for _, name := range eval.PolicyNames() {
			fmt.Fprintln(w, name)
		}
		return 0
	},
}

// helpWithoutDocs are the --help-* options this implementation recognises but
// cannot answer, because answering means reproducing CMake's manual pages.
var helpWithoutDocs = []string{
	"--help-full", "--help-manual", "--help-command", "--help-commands",
	"--help-diagnostic", "--help-diagnostic-list", "--help-diagnostics",
	"--help-module", "--help-module-list", "--help-modules",
	"--help-policy", "--help-policies",
	"--help-property", "--help-property-list", "--help-properties",
	"--help-variable", "--help-variable-list", "--help-variables",
}

// runHelp handles the --help family. It reports whether the argument was one.
func runHelp(e Env, args []string) (int, bool) {
	arg := args[0]
	name, _, _ := strings.Cut(arg, "=")

	if answer, ok := helpModes[name]; ok {
		return answer(e.Out), true
	}
	for _, n := range helpWithoutDocs {
		if name != n {
			continue
		}
		fmt.Fprintf(e.Err, "CMake Error: %s is recognised but this implementation ships no\n"+
			"  manual pages. Use the cmake documentation at cmake.org/documentation.\n", name)
		return 1, true
	}
	return 0, false
}

// allCommandNames is every command a CMakeLists.txt may call: the table plus
// the control-flow constructs the evaluator handles itself. Reporting only the
// table would omit `if` and `foreach`, which is not what anyone asking for a
// command list wants.
func allCommandNames() []string {
	names := append([]string{}, eval.Commands()...)
	names = append(names,
		"if", "elseif", "else", "endif",
		"foreach", "endforeach", "while", "endwhile",
		"function", "endfunction", "macro", "endmacro",
		"block", "endblock", "break", "continue", "return")
	sort.Strings(names)
	return names
}

// printVersion handles --version and its json form.
func printVersion(w io.Writer, arg string) int {
	_, format, _ := strings.Cut(arg, "=")
	if format == "json-v1" {
		major, minor, patch := splitVersion(Version)
		fmt.Fprintf(w, `{
  "major" : %s,
  "minor" : %s,
  "patch" : %s,
  "string" : %q,
  "suite" : "go-cmake"
}
`, major, minor, patch, Version)
		return 0
	}
	fmt.Fprintf(w, "cmake version %s\n\n", Version)
	fmt.Fprintln(w, "CMake suite implemented in Go (github.com/vertex-language/go-cmake).")
	return 0
}

func splitVersion(v string) (major, minor, patch string) {
	parts := strings.SplitN(v, ".", 3)
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return parts[0], parts[1], parts[2]
}
