package token_test

import (
	"testing"

	"github.com/vertex-language/go-cmake/token"
)

// ---------------------------------------------------------------------------
// Kind.String() round-trip
// ---------------------------------------------------------------------------

func TestKindString(t *testing.T) {
	cases := []struct {
		kind token.Kind
		want string
	}{
		{token.ILLEGAL, "ILLEGAL"},
		{token.EOF, "EOF"},
		{token.LPAREN, "LPAREN"},
		{token.RPAREN, "RPAREN"},
		{token.IDENTIFIER, "IDENTIFIER"},
		{token.QUOTED_ARG, "QUOTED_ARG"},
		{token.UNQUOTED_ARG, "UNQUOTED_ARG"},
		{token.BRACKET_ARG, "BRACKET_ARG"},
		{token.NEWLINE, "NEWLINE"},
		{token.SPACE, "SPACE"},
		{token.LINE_COMMENT, "LINE_COMMENT"},
		{token.BRACKET_COMMENT, "BRACKET_COMMENT"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestKindStringUnknown(t *testing.T) {
	k := token.Kind(999)
	got := k.String()
	want := "Kind(999)"
	if got != want {
		t.Errorf("unknown Kind.String() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// File.AddLine / File.Position
// ---------------------------------------------------------------------------
//
// Synthetic file (12 bytes): "abc\ndef\nghi\n"
// lines: [0, 4, 8, 12]  (pre-seed 0, then AddLine for each newline successor)

func TestFileAddLineAndPosition(t *testing.T) {
	const src = "abc\ndef\nghi\n"
	f := token.NewFile("test.cmake", len(src))

	// Seed line starts after each newline.
	f.AddLine(4)
	f.AddLine(8)
	f.AddLine(12)

	if got := f.LineCount(); got != 4 {
		t.Errorf("LineCount() = %d, want 4", got)
	}

	cases := []struct {
		offset int
		line   int
		col    int
	}{
		{0, 1, 1},
		{1, 1, 2},
		{2, 1, 3},
		{3, 1, 4},
		{4, 2, 1},
		{7, 2, 4},
		{8, 3, 1},
		{11, 3, 4},
		{12, 4, 1},
	}
	for _, tc := range cases {
		p := f.Pos(tc.offset)
		pos := f.Position(p)
		if pos.Line != tc.line || pos.Column != tc.col {
			t.Errorf("offset %d: got Line=%d Col=%d, want Line=%d Col=%d",
				tc.offset, pos.Line, pos.Column, tc.line, tc.col)
		}
		if pos.Filename != "test.cmake" {
			t.Errorf("offset %d: Filename = %q, want %q", tc.offset, pos.Filename, "test.cmake")
		}
		if pos.Offset != tc.offset {
			t.Errorf("offset %d: pos.Offset = %d, want %d", tc.offset, pos.Offset, tc.offset)
		}
	}
}

func TestFileBase(t *testing.T) {
	f := token.NewFile("x.cmake", 100)
	if f.Base() != token.Pos(1) {
		t.Errorf("Base() = %d, want 1", f.Base())
	}
	if f.Size() != 100 {
		t.Errorf("Size() = %d, want 100", f.Size())
	}
	if f.Name() != "x.cmake" {
		t.Errorf("Name() = %q, want %q", f.Name(), "x.cmake")
	}
}

// ---------------------------------------------------------------------------
// FileSet.File - finds the right file by Pos
// ---------------------------------------------------------------------------

func TestFileSetFile(t *testing.T) {
	fs := token.NewFileSet()

	f1 := fs.AddFile("a.cmake", 9)  // base=1,  range [1..10]
	f2 := fs.AddFile("b.cmake", 19) // base=11, range [11..30]
	f3 := fs.AddFile("c.cmake", 49) // base=31, range [31..80]

	cases := []struct {
		p    token.Pos
		want *token.File
	}{
		{token.Pos(1), f1},
		{token.Pos(10), f1},
		{token.Pos(11), f2},
		{token.Pos(30), f2},
		{token.Pos(31), f3},
		{token.Pos(80), f3},
		{token.NoPos, nil},
		{token.Pos(81), nil}, // past f3 end
	}
	for _, tc := range cases {
		got := fs.File(tc.p)
		if got != tc.want {
			wantName := "<nil>"
			if tc.want != nil {
				wantName = tc.want.Name()
			}
			gotName := "<nil>"
			if got != nil {
				gotName = got.Name()
			}
			t.Errorf("FileSet.File(%d) = %s, want %s", tc.p, gotName, wantName)
		}
	}
}

func TestFileSetPosition(t *testing.T) {
	fs := token.NewFileSet()
	f := fs.AddFile("main.cmake", 20)
	f.AddLine(5)
	f.AddLine(10)

	// offset 6 => line 2, col 2
	p := f.Pos(6)
	pos := fs.Position(p)
	if pos.Line != 2 || pos.Column != 2 || pos.Filename != "main.cmake" {
		t.Errorf("FileSet.Position: got %+v, want Line=2 Col=2 Filename=main.cmake", pos)
	}
}

// ---------------------------------------------------------------------------
// Position.String() formatting
// ---------------------------------------------------------------------------

func TestPositionString(t *testing.T) {
	cases := []struct {
		pos  token.Position
		want string
	}{
		{token.Position{Filename: "foo.cmake", Line: 10, Column: 5}, "foo.cmake:10:5"},
		{token.Position{Filename: "", Line: 3, Column: 1}, "3:1"},
		{token.Position{Filename: "bar.cmake", Line: 1, Column: 1}, "bar.cmake:1:1"},
	}
	for _, tc := range cases {
		if got := tc.pos.String(); got != tc.want {
			t.Errorf("Position.String() = %q, want %q", got, tc.want)
		}
	}
}

func TestPositionIsValid(t *testing.T) {
	valid := token.Position{Line: 1, Column: 1}
	invalid := token.Position{}
	if !valid.IsValid() {
		t.Error("expected valid.IsValid() == true")
	}
	if invalid.IsValid() {
		t.Error("expected invalid.IsValid() == false")
	}
}

// ---------------------------------------------------------------------------
// Token.String()
// ---------------------------------------------------------------------------

func TestTokenString(t *testing.T) {
	tok := token.Token{Kind: token.LPAREN, Pos: token.Pos(1), Lit: "("}
	got := tok.String()
	want := "LPAREN(()"
	if got != want {
		t.Errorf("Token.String() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Pos helpers
// ---------------------------------------------------------------------------

func TestPosIsValid(t *testing.T) {
	if token.NoPos.IsValid() {
		t.Error("NoPos.IsValid() should be false")
	}
	if !token.Pos(1).IsValid() {
		t.Error("Pos(1).IsValid() should be true")
	}
}
