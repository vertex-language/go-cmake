package eval

import (
	"runtime"
	"strings"
)

// CMake lays out an error or a warning the same way it lays out its own
// documentation: the text is indented two spaces and filled to a fixed column,
// and a line that already begins with a space is left exactly as written.
//
// Reproducing that is not decoration. A build log is something people diff,
// grep, and paste into bug reports, and a message that wraps differently makes
// two runs of the same project look like two different problems. It also has a
// consequence for the messages this package builds: they are written in the
// unwrapped form CMake writes them in, with single newlines, because the
// formatter is what turns those into the blank lines that appear on screen.
//
// The rules below were read off the binary rather than the source: the column,
// the two spaces that follow a sentence-ending period, and the blank line that
// each newline in the text turns into.

const (
	// diagWidth is the column the text is filled to, indent included.
	diagWidth = 77
	// diagIndent goes in front of every line of a diagnostic's body.
	diagIndent = "  "
)

// Diagnostic renders one message the way CMake writes it: the banner, a colon,
// and the body beneath it. The banner is the "CMake Error at file:line (cmd)"
// part, without its colon. The result ends in a newline; what comes after it --
// a blank line, or the footer an author warning carries -- is the caller's.
func Diagnostic(banner, text string) string {
	return banner + ":" + formatBody(text)
}

// formatBody lays out the text under a banner.
//
// The text is read as a sequence of lines. Consecutive lines that begin with a
// space are preformatted -- they keep their own spacing and are only indented
// -- and every other line is a paragraph, filled to the column width. Each
// paragraph and each preformatted block starts on a line of its own, which is
// where the blank lines in CMake's output come from: a newline in the message
// becomes an empty paragraph, and an empty paragraph is a blank line.
func formatBody(text string) string {
	var b strings.Builder
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); {
		if strings.HasPrefix(lines[i], " ") {
			b.WriteString("\n")
			for ; i < len(lines) && strings.HasPrefix(lines[i], " "); i++ {
				b.WriteString(diagIndent)
				b.WriteString(strings.TrimRight(lines[i], " \t"))
				b.WriteString("\n")
			}
			continue
		}
		b.WriteString("\n")
		if lines[i] != "" {
			b.WriteString(diagIndent)
			fillColumn(&b, lines[i])
		}
		b.WriteString("\n")
		i++
	}
	return b.String()
}

// fillColumn writes one paragraph, breaking it into lines that fit.
//
// A word that would overflow starts a new line, and a word following one that
// ended in a period is separated by two spaces rather than one -- which is why
// CMake's own "This warning is for project developers.  Use -Wno-author"
// carries a double space in the middle.
func fillColumn(b *strings.Builder, text string) {
	width := diagWidth - len(diagIndent)
	column := 0
	newSentence := false
	firstLine := true

	for l := 0; l < len(text); {
		r := l
		for r < len(text) && text[r] != ' ' {
			r++
		}
		word := text[l:r]

		gap := 1
		if newSentence {
			gap = 2
		}
		switch {
		case len(word) < width-column-(gap-1):
			if word != "" {
				if column > 0 {
					b.WriteString(strings.Repeat(" ", gap))
					column += gap
				} else if !firstLine {
					b.WriteString(diagIndent)
				}
				b.WriteString(word)
				newSentence = strings.HasSuffix(word, ".")
			}
			column += len(word)
		default:
			b.WriteString("\n")
			firstLine = false
			column = 0
			if word != "" {
				b.WriteString(diagIndent)
				b.WriteString(word)
				column = len(word)
				newSentence = strings.HasSuffix(word, ".")
			}
		}

		l = r
		for l < len(text) && text[l] == ' ' {
			l++
		}
	}
}

// ReportPath names a file the way CMake names it in a diagnostic: relative to
// the source directory when the file is inside it, and absolute when it is not.
//
// It is a small thing that matters a lot to read: a project's own files are
// recognised by their short names, and "sub/CMakeLists.txt" says where to look
// in a way that a hundred-character path does not. A file from outside the tree
// keeps its full path, because a short name for it would be a name the reader
// cannot find.
func ReportPath(sourceDir, file string) string {
	if sourceDir == "" || file == "" {
		return file
	}
	dir := slashPath(sourceDir)
	f := slashPath(file)
	prefix := strings.TrimSuffix(dir, "/") + "/"
	under := strings.HasPrefix(f, prefix)
	if !under && runtime.GOOS == "windows" {
		// Windows paths differ in case without differing in meaning, and the
		// two halves come from different places: one from the command line, one
		// from the file that included it.
		under = strings.HasPrefix(strings.ToLower(f), strings.ToLower(prefix))
	}
	if !under {
		return f
	}
	return f[len(prefix):]
}
