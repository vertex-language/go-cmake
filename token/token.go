// Package token defines the lexical token types and source-position
// machinery for the CMake language.
package token

import (
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// Kind
// ---------------------------------------------------------------------------

// Kind is the set of lexical token types in the CMake language.
type Kind int

const (
	ILLEGAL Kind = iota
	EOF

	// Punctuation
	LPAREN // (
	RPAREN // )

	// Literals
	IDENTIFIER   // add_executable
	QUOTED_ARG   // "..."
	UNQUOTED_ARG // foo;bar or NO_SPACE
	BRACKET_ARG  // [[...]] or [==[...]==]

	// Whitespace (preserved for round-trip printing)
	NEWLINE // \n
	SPACE   // [ \t]+

	// Comments
	LINE_COMMENT    // # ...
	BRACKET_COMMENT // #[[...]] or #[==[...]==]
)

var kindNames = [...]string{
	ILLEGAL:         "ILLEGAL",
	EOF:             "EOF",
	LPAREN:          "LPAREN",
	RPAREN:          "RPAREN",
	IDENTIFIER:      "IDENTIFIER",
	QUOTED_ARG:      "QUOTED_ARG",
	UNQUOTED_ARG:    "UNQUOTED_ARG",
	BRACKET_ARG:     "BRACKET_ARG",
	NEWLINE:         "NEWLINE",
	SPACE:           "SPACE",
	LINE_COMMENT:    "LINE_COMMENT",
	BRACKET_COMMENT: "BRACKET_COMMENT",
}

// String returns the name of the token kind (e.g. "LPAREN").
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// ---------------------------------------------------------------------------
// Pos / Position
// ---------------------------------------------------------------------------

// Pos is a compact source position: a 1-based byte offset into a File.
// The zero value is NoPos, meaning no position.
type Pos int32

// NoPos is the zero Pos value, indicating no position.
const NoPos Pos = 0

// IsValid reports whether p is a valid position.
func (p Pos) IsValid() bool { return p != NoPos }

// Position holds the full human-readable source location.
type Position struct {
	Filename string
	Offset   int // 0-based byte offset
	Line     int // 1-based
	Column   int // 1-based byte column
}

// IsValid reports whether the position is valid (Line > 0).
func (p Position) IsValid() bool { return p.Line > 0 }

// String returns a human-readable representation such as "file.cmake:10:5"
// or "10:5" when no filename is set.
func (p Position) String() string {
	if p.Filename != "" {
		return fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Column)
	}
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// ---------------------------------------------------------------------------
// Token
// ---------------------------------------------------------------------------

// Token is a single lexical token.
type Token struct {
	Kind Kind
	Pos  Pos
	Lit  string // the literal text of the token
}

// String returns a human-readable representation such as "LPAREN(()".
func (t Token) String() string {
	return fmt.Sprintf("%s(%s)", t.Kind, t.Lit)
}

// ---------------------------------------------------------------------------
// File
// ---------------------------------------------------------------------------

// File records the source filename and line offsets for a file being
// processed. It allows converting a Pos to a Position.
type File struct {
	name  string
	base  int   // Pos value of the first byte (always 1)
	size  int   // byte size of file content
	lines []int // byte offsets of the start of each line (lines[0] == 0)
}

// NewFile creates a new File with the given name and byte size.
// The first line starts at offset 0, so lines[0] == 0 is pre-seeded.
func NewFile(name string, size int) *File {
	return &File{
		name:  name,
		base:  1,
		size:  size,
		lines: []int{0},
	}
}

// Name returns the filename associated with this file.
func (f *File) Name() string { return f.name }

// Base returns the Pos of the first byte in this file (always 1 for a
// standalone File created by NewFile).
func (f *File) Base() Pos { return Pos(f.base) }

// Size returns the byte size of the file content.
func (f *File) Size() int { return f.size }

// AddLine records the byte offset of the start of a new line.
// offset must be monotonically increasing and within [0, size].
// It is called by the scanner each time a newline character is consumed.
func (f *File) AddLine(offset int) {
	if n := len(f.lines); n == 0 || offset > f.lines[n-1] {
		f.lines = append(f.lines, offset)
	}
}

// LineCount returns the number of lines recorded in the file.
func (f *File) LineCount() int { return len(f.lines) }

// Pos converts a 0-based byte offset within the file to a Pos value.
func (f *File) Pos(offset int) Pos {
	return Pos(f.base + offset)
}

// Offset converts a Pos back to a 0-based byte offset within the file.
func (f *File) Offset(p Pos) int {
	return int(p) - f.base
}

// Position converts a Pos to a full Position value using a binary search
// on the line table.
func (f *File) Position(p Pos) Position {
	offset := f.Offset(p)
	// Binary search: find the last line whose start offset <= offset.
	line := sort.Search(len(f.lines), func(i int) bool {
		return f.lines[i] > offset
	}) - 1
	if line < 0 {
		line = 0
	}
	col := offset - f.lines[line] + 1 // 1-based
	return Position{
		Filename: f.name,
		Offset:   offset,
		Line:     line + 1, // 1-based
		Column:   col,
	}
}

// ---------------------------------------------------------------------------
// FileSet
// ---------------------------------------------------------------------------

// FileSet holds a collection of files, allowing Pos values to be decoded
// across multiple files (for include chains).
type FileSet struct {
	files []*File
	base  int // next available Pos base
}

// NewFileSet returns a new, empty FileSet.
func NewFileSet() *FileSet {
	return &FileSet{base: 1}
}

// AddFile registers a new file with the given name and byte size in the
// FileSet, assigns it a non-overlapping Pos range, and returns it.
func (fs *FileSet) AddFile(name string, size int) *File {
	f := &File{
		name:  name,
		base:  fs.base,
		size:  size,
		lines: []int{0},
	}
	fs.files = append(fs.files, f)
	// +1 so the EOF pos of one file never collides with the base of the next.
	fs.base += size + 1
	return f
}

// File returns the File that contains the given Pos, or nil if not found.
// It uses binary search over the registered files.
func (fs *FileSet) File(p Pos) *File {
	if len(fs.files) == 0 {
		return nil
	}
	pv := int(p)
	// Find the last file whose base <= pv.
	idx := sort.Search(len(fs.files), func(i int) bool {
		return fs.files[i].base > pv
	}) - 1
	if idx < 0 {
		return nil
	}
	f := fs.files[idx]
	// Verify p is within [base, base+size].
	if pv > f.base+f.size {
		return nil
	}
	return f
}

// Position converts a Pos to a full Position using the FileSet's file table.
func (fs *FileSet) Position(p Pos) Position {
	f := fs.File(p)
	if f == nil {
		return Position{}
	}
	return f.Position(p)
}
