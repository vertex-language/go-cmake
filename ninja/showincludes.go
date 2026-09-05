package ninja

import "strings"

// MSVC does not write a depfile. Under /showIncludes it prints one line per
// header it opened, and the build tool is expected to read them off the
// compiler's own output. Ninja does the same thing, and it has to: without it a
// change to a header would never rebuild anything, because the build file names
// only the source.
//
// The prefix is localised. Matching the English text alone would work on an
// English install and silently lose every dependency elsewhere, which is the
// worst kind of bug: the build succeeds and produces a stale binary. The shape
// of the line is stable even where the words are not —
//
//	Note: including file: C:\path\to\header.h
//	<word>: <words>: <indent><path>
//
// so the path is taken as whatever follows the second colon, checked against
// the shape of an absolute path.

// showIncludePath returns the header named by a /showIncludes line, or "".
func showIncludePath(line string) string {
	line = strings.TrimRight(line, "\r")
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return ""
	}
	first := strings.IndexByte(trimmed, ':')
	if first < 0 {
		return ""
	}
	rest := trimmed[first+1:]
	second := strings.IndexByte(rest, ':')
	if second < 0 {
		return ""
	}
	path := strings.TrimSpace(rest[second+1:])
	if isAbsoluteWindowsOrPosix(path) {
		return path
	}
	return ""
}

// isAbsoluteWindowsOrPosix reports whether a string looks like an absolute path
// in either convention. A relative path here would mean the line was something
// other than an include note.
func isAbsoluteWindowsOrPosix(p string) bool {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`)
}

// parseShowIncludes collects the header paths from a compiler's output.
func parseShowIncludes(out string) []string {
	var deps []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		p := showIncludePath(line)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		deps = append(deps, p)
	}
	return deps
}

// filterShowIncludes removes the dependency lines from output shown to the
// user, who asked to compile a program and not to read a header list.
func filterShowIncludes(out string) string {
	if out == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(out, "\n") {
		if showIncludePath(line) != "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	s := strings.TrimRight(b.String(), "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}
