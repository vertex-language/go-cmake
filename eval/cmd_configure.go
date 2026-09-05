package eval

import (
	"context"
	"fmt"
	"strings"
)

func init() {
	register("configure_file", cmdConfigureFile)
}

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// cmdConfigureFile copies a file, substituting variable references as it goes.
// This is how a project gets its version number into a C header, and it is the
// one command whose output the build must depend on.
func cmdConfigureFile(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("configure_file called with incorrect number of arguments")
	}
	input := e.state.absPath(v[0])
	output := v[1]
	if !isAbsolutePath(output) {
		output = joinPath(e.state.Dir().Binary, output)
	}

	copyOnly, escapeQuotes, atOnly := false, false, false
	newline := ""
	for i := 2; i < len(v); i++ {
		switch v[i] {
		case "COPYONLY":
			copyOnly = true
		case "ESCAPE_QUOTES":
			escapeQuotes = true
		case "@ONLY":
			atOnly = true
		case "NEWLINE_STYLE":
			if i+1 < len(v) {
				newline = v[i+1]
				i++
			}
		case "NO_SOURCE_PERMISSIONS", "USE_SOURCE_PERMISSIONS", "FILE_PERMISSIONS":
		}
	}

	data, err := e.fs.ReadFile(input)
	if err != nil {
		return e.fatalf("configure_file Problem configuring file: could not read %s", input)
	}

	out := string(data)
	if !copyOnly {
		out = configureString(e.state, out, atOnly, escapeQuotes)
	}
	switch newline {
	case "CRLF", "WIN32":
		out = strings.ReplaceAll(normalizeNewlines(out), "\n", "\r\n")
	case "LF", "UNIX":
		out = normalizeNewlines(out)
	}

	// The output directory may not exist yet; configure_file creates it, which
	// is why a project can write into a binary subdirectory it never mkdir'd.
	if err := e.fs.MkdirAll(dirOf(output)); err != nil {
		return e.fatalf("configure_file could not create directory %s: %v", dirOf(output), err)
	}
	// An unchanged output is left alone so its timestamp does not advance and
	// trigger a rebuild of everything that includes it.
	if existing, err := e.fs.ReadFile(output); err == nil && string(existing) == out {
		e.state.ConfiguredFiles = append(e.state.ConfiguredFiles, output)
		return nil
	}
	if err := e.fs.WriteFile(output, []byte(out)); err != nil {
		return e.fatalf("configure_file could not write %s: %v", output, err)
	}
	e.state.ConfiguredFiles = append(e.state.ConfiguredFiles, output)
	return nil
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// configureString performs the substitutions configure_file and
// string(CONFIGURE) apply: @VAR@ always, ${VAR} unless @ONLY, and the
// #cmakedefine / #cmakedefine01 directives.
func configureString(s *State, text string, atOnly, escapeQuotes bool) string {
	var out strings.Builder
	out.Grow(len(text))
	for _, line := range splitLinesKeepEnds(text) {
		out.WriteString(configureLine(s, line, atOnly, escapeQuotes))
	}
	return out.String()
}

func splitLinesKeepEnds(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// configureLine handles one line, which is the granularity at which the
// #cmakedefine directives are defined.
func configureLine(s *State, line string, atOnly, escapeQuotes bool) string {
	trimmed := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trimmed)]

	if rest, ok := cutPrefixWord(trimmed, "#cmakedefine01"); ok {
		name, _ := firstWord(rest)
		val := "0"
		if isOn(s.GetVar(name)) {
			val = "1"
		}
		return indent + "#define " + name + " " + val + lineEnd(line)
	}
	if rest, ok := cutPrefixWord(trimmed, "#cmakedefine"); ok {
		name, tail := firstWord(rest)
		body := substituteRefs(s, tail, atOnly, escapeQuotes)
		if isOff(s.GetVar(name)) {
			// An undefined symbol becomes a comment rather than vanishing, so
			// that the generated header still shows what could have been set.
			return indent + "/* #undef " + name + " */" + lineEnd(line)
		}
		return indent + "#define " + name + strings.TrimRight(body, "\r\n") + lineEnd(line)
	}
	return substituteRefs(s, line, atOnly, escapeQuotes)
}

func lineEnd(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	default:
		return ""
	}
}

// cutPrefixWord matches a directive only when it is followed by a word break,
// so that #cmakedefine01 is not mistaken for #cmakedefine.
func cutPrefixWord(s, prefix string) (string, bool) {
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	rest := s[len(prefix):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n' && rest[0] != '\r' {
		return "", false
	}
	return rest, true
}

func firstWord(s string) (word, rest string) {
	s = strings.TrimLeft(s, " \t")
	i := 0
	for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
		i++
	}
	return s[:i], s[i:]
}

// substituteRefs replaces @VAR@ and, unless atOnly, ${VAR} references.
func substituteRefs(s *State, text string, atOnly, escapeQuotes bool) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		switch {
		case text[i] == '@':
			end := strings.IndexByte(text[i+1:], '@')
			if end < 0 {
				b.WriteByte(text[i])
				i++
				continue
			}
			name := text[i+1 : i+1+end]
			if !isVariableName(name) {
				b.WriteByte(text[i])
				i++
				continue
			}
			b.WriteString(escapeValue(s.GetVar(name), escapeQuotes))
			i += end + 2

		case !atOnly && text[i] == '$' && i+1 < len(text) && text[i+1] == '{':
			end := strings.IndexByte(text[i+2:], '}')
			if end < 0 {
				b.WriteByte(text[i])
				i++
				continue
			}
			name := text[i+2 : i+2+end]
			b.WriteString(escapeValue(s.GetVar(name), escapeQuotes))
			i += end + 3

		default:
			b.WriteByte(text[i])
			i++
		}
	}
	return b.String()
}

func escapeValue(v string, escapeQuotes bool) string {
	if escapeQuotes {
		return strings.ReplaceAll(v, `"`, `\"`)
	}
	return v
}

// isVariableName reports whether a run of text between @ signs looks like a
// variable name. Anything else is left alone so that an email address or a
// decorator in the input file survives configuration.
func isVariableName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || c == '.' || c == '-' || c == '+' || c == '/' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}
