package eval

import (
	"fmt"
	"strconv"
	"strings"
)

// evalMathExpr evaluates a math(EXPR) expression.
//
// CMake documents this as "a subset of C expressions" and implements it with a
// recursive-descent parser over 64-bit signed integers. Division and modulo by
// zero are errors rather than crashes, and there is no floating point: 7/2 is
// 3, which is a regular source of surprise for anyone computing a midpoint.
func evalMathExpr(src string) (int64, error) {
	p := &mathParser{src: src}
	p.skipSpace()
	v, err := p.parseOr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos < len(p.src) {
		return 0, fmt.Errorf("unexpected character %q", p.src[p.pos])
	}
	return v, nil
}

type mathParser struct {
	src string
	pos int
}

func (p *mathParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
		p.pos++
	}
}

// accept consumes tok if it is next, honouring the longest-match rule so that
// "<<" is not read as two "<" tokens.
func (p *mathParser) accept(tok string) bool {
	p.skipSpace()
	if !strings.HasPrefix(p.src[p.pos:], tok) {
		return false
	}
	// Do not let "<" match the front of "<<".
	if tok == "<" || tok == ">" {
		if strings.HasPrefix(p.src[p.pos:], tok+tok) {
			return false
		}
	}
	p.pos += len(tok)
	return true
}

func (p *mathParser) parseOr() (int64, error) {
	l, err := p.parseXor()
	if err != nil {
		return 0, err
	}
	for p.accept("|") {
		r, err := p.parseXor()
		if err != nil {
			return 0, err
		}
		l |= r
	}
	return l, nil
}

func (p *mathParser) parseXor() (int64, error) {
	l, err := p.parseAnd()
	if err != nil {
		return 0, err
	}
	for p.accept("^") {
		r, err := p.parseAnd()
		if err != nil {
			return 0, err
		}
		l ^= r
	}
	return l, nil
}

func (p *mathParser) parseAnd() (int64, error) {
	l, err := p.parseShift()
	if err != nil {
		return 0, err
	}
	for p.accept("&") {
		r, err := p.parseShift()
		if err != nil {
			return 0, err
		}
		l &= r
	}
	return l, nil
}

func (p *mathParser) parseShift() (int64, error) {
	l, err := p.parseAdd()
	if err != nil {
		return 0, err
	}
	for {
		switch {
		case p.accept("<<"):
			r, err := p.parseAdd()
			if err != nil {
				return 0, err
			}
			l <<= uint(r)
		case p.accept(">>"):
			r, err := p.parseAdd()
			if err != nil {
				return 0, err
			}
			l >>= uint(r)
		default:
			return l, nil
		}
	}
}

func (p *mathParser) parseAdd() (int64, error) {
	l, err := p.parseMul()
	if err != nil {
		return 0, err
	}
	for {
		switch {
		case p.accept("+"):
			r, err := p.parseMul()
			if err != nil {
				return 0, err
			}
			l += r
		case p.accept("-"):
			r, err := p.parseMul()
			if err != nil {
				return 0, err
			}
			l -= r
		default:
			return l, nil
		}
	}
}

func (p *mathParser) parseMul() (int64, error) {
	l, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		switch {
		case p.accept("*"):
			r, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			l *= r
		case p.accept("/"):
			r, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			if r == 0 {
				return 0, fmt.Errorf("divide by zero")
			}
			l /= r
		case p.accept("%"):
			r, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			if r == 0 {
				return 0, fmt.Errorf("divide by zero")
			}
			l %= r
		default:
			return l, nil
		}
	}
}

func (p *mathParser) parseUnary() (int64, error) {
	switch {
	case p.accept("-"):
		v, err := p.parseUnary()
		return -v, err
	case p.accept("+"):
		return p.parseUnary()
	case p.accept("~"):
		v, err := p.parseUnary()
		return ^v, err
	}
	return p.parsePrimary()
}

func (p *mathParser) parsePrimary() (int64, error) {
	p.skipSpace()
	if p.accept("(") {
		v, err := p.parseOr()
		if err != nil {
			return 0, err
		}
		if !p.accept(")") {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		return v, nil
	}
	start := p.pos
	if p.pos < len(p.src) && (p.src[p.pos] == '0') && p.pos+1 < len(p.src) &&
		(p.src[p.pos+1] == 'x' || p.src[p.pos+1] == 'X') {
		p.pos += 2
		for p.pos < len(p.src) && isHexDigit(p.src[p.pos]) {
			p.pos++
		}
		n, err := strconv.ParseUint(p.src[start+2:p.pos], 16, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid hexadecimal literal %q", p.src[start:p.pos])
		}
		return int64(n), nil
	}
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	if p.pos == start {
		if p.pos < len(p.src) {
			return 0, fmt.Errorf("unexpected character %q", p.src[p.pos])
		}
		return 0, fmt.Errorf("unexpected end of expression")
	}
	return strconv.ParseInt(p.src[start:p.pos], 10, 64)
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
