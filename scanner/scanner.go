// Package scanner implements the CMake language lexer.
//
// It converts raw CMake source bytes into a sequence of [token.Token] values.
// The scanner is designed to be called in a loop via [Scanner.Scan]:
//
//	var s scanner.Scanner
//	file := fset.AddFile("CMakeLists.txt", len(src))
//	s.Init(file, src)
//	for {
//	    tok := s.Scan()
//	    if tok.Kind == token.EOF { break }
//	    // use tok
//	}
//
// The scanner is context-sensitive in one important way: inside a command
// argument list (between the opening '(' and its matching ')'), newlines and
// spaces are treated as separators rather than file-level whitespace, and the
// ')' token ends the argument list rather than being an argument character.
package scanner

import (
	"fmt"

	"github.com/vertex-language/go-cmake/token"
)

// ErrorHandler is called for each scanning error.
// If nil, errors are silently discarded.
type ErrorHandler func(pos token.Position, msg string)

// Scanner holds the state of the CMake lexer.
// Call [Scanner.Init] before the first call to [Scanner.Scan].
type Scanner struct {
	file *token.File
	src  []byte
	err  ErrorHandler

	ch     rune // current character (-1 = EOF)
	offset int  // byte offset of ch in src
	rdOff  int  // byte offset of next read position

	// inArgs tracks nesting depth inside argument lists.
	// 0 = file level, >0 = inside one or more argument lists.
	inArgs int
}

const bom = 0xFEFF // UTF-8 BOM

// Init prepares the scanner to tokenize src. The file must have been created
// by the same FileSet that will be used to interpret the resulting Pos values.
func (s *Scanner) Init(file *token.File, src []byte, err ErrorHandler) {
	s.file = file
	s.src = src
	s.err = err
	s.offset = 0
	s.rdOff = 0
	s.inArgs = 0
	s.next() // prime ch

	// Skip optional UTF-8 BOM.
	if s.ch == bom {
		s.next()
	}
}

// next advances to the next character.
func (s *Scanner) next() {
	if s.rdOff < len(s.src) {
		s.offset = s.rdOff
		ch := rune(s.src[s.rdOff])
		s.rdOff++
		// Handle \r\n → \n normalisation.
		if ch == '\r' {
			if s.rdOff < len(s.src) && s.src[s.rdOff] == '\n' {
				s.rdOff++
			}
			ch = '\n'
		}
		if ch == '\n' {
			s.file.AddLine(s.rdOff)
		}
		s.ch = ch
	} else {
		s.offset = len(s.src)
		s.ch = -1
	}
}

func (s *Scanner) peek() rune {
	if s.rdOff < len(s.src) {
		ch := rune(s.src[s.rdOff])
		if ch == '\r' {
			ch = '\n'
		}
		return ch
	}
	return -1
}

func (s *Scanner) error(offset int, msg string) {
	if s.err != nil {
		s.err(s.file.Position(s.file.Pos(offset)), msg)
	}
}

func (s *Scanner) pos(offset int) token.Pos {
	return s.file.Pos(offset)
}

// Scan returns the next token.
// At the end of the source, it returns a token with Kind == token.EOF.
// Scan is safe to call after EOF; it will keep returning EOF tokens.
func (s *Scanner) Scan() token.Token {
	if s.ch == -1 {
		return token.Token{Kind: token.EOF, Pos: s.pos(s.offset), Lit: ""}
	}

	start := s.offset
	startPos := s.pos(start)

	if s.inArgs == 0 {
		return s.scanFileLevel(start, startPos)
	}
	return s.scanArgLevel(start, startPos)
}

// scanFileLevel handles tokens at the top level of a CMake file.
// At this level we expect: whitespace, newlines, bracket comments,
// line comments, and command identifiers.
func (s *Scanner) scanFileLevel(start int, startPos token.Pos) token.Token {
	switch s.ch {
	case '\n':
		s.next()
		return token.Token{Kind: token.NEWLINE, Pos: startPos, Lit: "\n"}

	case ' ', '\t':
		for s.ch == ' ' || s.ch == '\t' {
			s.next()
		}
		return token.Token{Kind: token.SPACE, Pos: startPos, Lit: string(s.src[start:s.offset])}

	case '#':
		return s.scanComment(startPos)

	default:
		// Must be an identifier (command name) or EOF.
		if isIdentStart(s.ch) {
			for isIdentCont(s.ch) {
				s.next()
			}
			lit := string(s.src[start:s.offset])
			return token.Token{Kind: token.IDENTIFIER, Pos: startPos, Lit: lit}
		}
		if s.ch == '(' {
			s.inArgs++
			s.next()
			return token.Token{Kind: token.LPAREN, Pos: startPos, Lit: "("}
		}
		// Unexpected character at file level.
		s.error(start, fmt.Sprintf("unexpected character %q", s.ch))
		s.next()
		return s.Scan()
	}
}

// scanArgLevel handles tokens inside an argument list.
func (s *Scanner) scanArgLevel(start int, startPos token.Pos) token.Token {
	switch {
	case s.ch == ')':
		s.inArgs--
		s.next()
		return token.Token{Kind: token.RPAREN, Pos: startPos, Lit: ")"}

	case s.ch == '(':
		// Nested parens inside argument lists are given as UNQUOTED_ARG
		// per the spec ("Each ( or ) is given to the command invocation
		// as a literal Unquoted Argument").
		s.inArgs++
		s.next()
		return token.Token{Kind: token.LPAREN, Pos: startPos, Lit: "("}

	case s.ch == '\n':
		s.next()
		return token.Token{Kind: token.NEWLINE, Pos: startPos, Lit: "\n"}

	case s.ch == ' ' || s.ch == '\t':
		for s.ch == ' ' || s.ch == '\t' {
			s.next()
		}
		return token.Token{Kind: token.SPACE, Pos: startPos, Lit: string(s.src[start:s.offset])}

	case s.ch == '#':
		return s.scanComment(startPos)

	case s.ch == '[':
		// Could be a bracket argument: [[...]] or [==[...]==]
		// Peek ahead to check.
		level := s.bracketLevel()
		if level >= 0 {
			return s.scanBracketArg(startPos, level)
		}
		// Not a bracket: fall through to unquoted.
		return s.scanUnquotedArg(startPos)

	case s.ch == '"':
		return s.scanQuotedArg(startPos)

	default:
		return s.scanUnquotedArg(startPos)
	}
}

// bracketLevel returns the number of '=' characters in the bracket open
// if the current position starts a bracket open "[=*[", or -1 otherwise.
// It does NOT advance s.ch.
func (s *Scanner) bracketLevel() int {
	if s.ch != '[' {
		return -1
	}
	i := s.rdOff
	level := 0
	for i < len(s.src) && s.src[i] == '=' {
		level++
		i++
	}
	if i < len(s.src) && s.src[i] == '[' {
		return level
	}
	return -1
}

// scanBracketArg reads a bracket argument starting at the current position.
// level is the number of '=' characters in the bracket.
func (s *Scanner) scanBracketArg(startPos token.Pos, level int) token.Token {
	// Consume '[' + '='*level + '['
	s.next() // '['
	for i := 0; i < level; i++ {
		s.next() // '='
	}
	s.next() // '['

	// Read until we find ']' + '='*level + ']'.
	for {
		if s.ch == -1 {
			s.error(s.offset, "unterminated bracket argument")
			break
		}
		if s.ch == ']' {
			// Check if this is the close bracket.
			j := s.rdOff
			eqCount := 0
			for j < len(s.src) && s.src[j] == '=' {
				eqCount++
				j++
			}
			if eqCount == level && j < len(s.src) && s.src[j] == ']' {
				// Found the close bracket — consume it.
				s.next() // ']'
				for i := 0; i < level; i++ {
					s.next() // '='
				}
				s.next() // ']'
				break
			}
		}
		s.next()
	}
	lit := string(s.src[int(startPos)-int(s.file.Base()) : s.offset])
	return token.Token{Kind: token.BRACKET_ARG, Pos: startPos, Lit: lit}
}

// scanQuotedArg reads a double-quoted argument.
func (s *Scanner) scanQuotedArg(startPos token.Pos) token.Token {
	s.next() // consume opening '"'
	for {
		switch s.ch {
		case -1:
			s.error(s.offset, "unterminated quoted argument")
			goto done
		case '"':
			s.next() // consume closing '"'
			goto done
		case '\\':
			s.next() // consume '\\'
			if s.ch == '\n' {
				// Line continuation: backslash + newline is ignored.
				s.next()
			} else if s.ch != -1 {
				s.next() // consume escaped character
			}
		default:
			s.next()
		}
	}
done:
	startOff := int(startPos) - int(s.file.Base())
	lit := string(s.src[startOff:s.offset])
	return token.Token{Kind: token.QUOTED_ARG, Pos: startPos, Lit: lit}
}

// scanUnquotedArg reads an unquoted argument.
// Unquoted arguments terminate at whitespace, ')', '(', '#', or EOF.
// They may contain escape sequences. They may also contain legacy
// embedded quoted strings and $(MAKEVAR) references.
func (s *Scanner) scanUnquotedArg(startPos token.Pos) token.Token {
	for {
		switch s.ch {
		case -1, '\n', ' ', '\t', ')', '#':
			goto done
		case '(':
			// '(' is allowed inside an unquoted arg (nested paren counting)
			// but only if we're in an argument list context — which we are.
			// Per the spec it is passed as a literal Unquoted Argument, so
			// we stop here and let the arg-level dispatcher return LPAREN.
			goto done
		case '\\':
			s.next() // consume '\\'
			if s.ch == '\n' {
				// Line continuation inside unquoted is not standard but
				// handle gracefully.
				s.next()
			} else if s.ch != -1 {
				s.next()
			}
		case '[':
			// Could start a bracket arg only if this is the very start;
			// inside an ongoing unquoted arg, '[' is just a character.
			startOff := int(startPos) - int(s.file.Base())
			if s.offset == startOff {
				level := s.bracketLevel()
				if level >= 0 {
					// This is actually a bracket arg, not unquoted.
					return s.scanBracketArg(startPos, level)
				}
			}
			s.next()
		default:
			s.next()
		}
	}
done:
	startOff := int(startPos) - int(s.file.Base())
	lit := string(s.src[startOff:s.offset])
	if lit == "" {
		// This can happen if we landed on ')' immediately — shouldn't reach here.
		return s.Scan()
	}
	return token.Token{Kind: token.UNQUOTED_ARG, Pos: startPos, Lit: lit}
}

// scanComment reads either a bracket comment (#[[...]]) or a line comment (# ...).
func (s *Scanner) scanComment(startPos token.Pos) token.Token {
	s.next() // consume '#'

	// Check for bracket comment: '#' followed immediately by bracket open.
	if s.ch == '[' {
		level := s.bracketLevel()
		if level >= 0 {
			// Bracket comment.
			innerTok := s.scanBracketArg(startPos+1, level) // reuse bracket reader
			startOff := int(startPos) - int(s.file.Base())
			lit := string(s.src[startOff : int(innerTok.Pos)-int(s.file.Base())+len(innerTok.Lit)])
			return token.Token{Kind: token.BRACKET_COMMENT, Pos: startPos, Lit: lit}
		}
	}

	// Line comment: consume until end of line.
	for s.ch != '\n' && s.ch != -1 {
		s.next()
	}
	startOff := int(startPos) - int(s.file.Base())
	lit := string(s.src[startOff:s.offset])
	return token.Token{Kind: token.LINE_COMMENT, Pos: startPos, Lit: lit}
}

func isIdentStart(ch rune) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_'
}

func isIdentCont(ch rune) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9')
}
