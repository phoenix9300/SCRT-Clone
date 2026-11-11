package vbs

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokKind uint8

const (
	tEOF    tokKind = iota
	tNL             // end of line
	tColon          // statement separator ':'
	tIdent          // identifier or keyword
	tNumber         // numeric literal (val is int64 or float64)
	tString         // string literal (val is string)
	tDate           // date literal (val is time.Time)
	tOp             // operator or punctuation (text holds the symbol)
)

type token struct {
	kind    tokKind
	text    string // identifier as written, or operator symbol
	lower   string // lower-cased identifier
	val     Value  // literal value
	line    int
	col     int
	space   bool // white space (or a line continuation) precedes the token
	bracket bool // [escaped identifier]: never a keyword
}

type lexer struct {
	src   string
	file  string
	pos   int
	line  int
	bol   int // offset of the first byte of the current line
	toks  []token
	space bool // pending white-space flag for the next token
}

// lexSource splits src into tokens. Comments are dropped, line
// continuations are folded and leading host directive lines ("# $language =
// ...") are skipped while keeping line numbers intact.
func lexSource(src, file string) ([]token, *Error) {
	lx := &lexer{src: src, file: file, line: 1}
	if strings.HasPrefix(src, "\xef\xbb\xbf") {
		lx.pos, lx.bol = 3, 3
	}
	if err := lx.run(); err != nil {
		return nil, err
	}
	return lx.toks, nil
}

func (lx *lexer) errAt(num, line, col int) *Error {
	return &Error{Number: num, Message: errText(num), File: lx.file, Line: line, Col: col, Syntax: true}
}

func (lx *lexer) emit(t token) {
	t.space = lx.space
	lx.space = false
	lx.toks = append(lx.toks, t)
}

func (lx *lexer) skipLine() {
	for lx.pos < len(lx.src) && lx.src[lx.pos] != '\n' && lx.src[lx.pos] != '\r' {
		lx.pos++
	}
}

func (lx *lexer) newline() {
	if lx.pos < len(lx.src) && lx.src[lx.pos] == '\r' {
		lx.pos++
		if lx.pos < len(lx.src) && lx.src[lx.pos] == '\n' {
			lx.pos++
		}
	} else if lx.pos < len(lx.src) {
		lx.pos++
	}
	lx.line++
	lx.bol = lx.pos
}

// skipBlanks skips horizontal white space.
func (lx *lexer) skipBlanks() {
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		if c == ' ' || c == '\t' || c == '\v' || c == '\f' {
			lx.pos++
			lx.space = true
			continue
		}
		if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRuneInString(lx.src[lx.pos:])
			if r != utf8.RuneError && unicode.IsSpace(r) && r != ' ' && r != ' ' && r != '\u0085' {
				lx.pos += n
				lx.space = true
				continue
			}
		}
		return
	}
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// atStmtStart reports whether the next token begins a statement, which is
// where a Rem comment may appear.
func (lx *lexer) atStmtStart() bool {
	if len(lx.toks) == 0 {
		return true
	}
	t := lx.toks[len(lx.toks)-1]
	switch t.kind {
	case tNL, tColon:
		return true
	case tIdent:
		return !t.bracket && (t.lower == "then" || t.lower == "else")
	}
	return false
}

func (lx *lexer) run() *Error {
	top := true // still inside the leading host-directive region
	for {
		lx.skipBlanks()
		if lx.pos >= len(lx.src) {
			break
		}
		c := lx.src[lx.pos]
		line, col := lx.line, lx.pos-lx.bol+1
		switch {
		case c == '\r' || c == '\n':
			lx.emit(token{kind: tNL, line: line, col: col})
			lx.newline()
			continue
		case c == '\'':
			lx.skipLine()
			continue
		case c == '#' && top:
			lx.skipLine()
			continue
		case c == '_':
			// Line continuation: an underscore that ends the line.
			j := lx.pos + 1
			for j < len(lx.src) && (lx.src[j] == ' ' || lx.src[j] == '\t') {
				j++
			}
			if j >= len(lx.src) || lx.src[j] == '\r' || lx.src[j] == '\n' || lx.src[j] == '\'' {
				lx.pos = j
				lx.skipLine()
				if lx.pos < len(lx.src) {
					lx.newline()
				}
				lx.space = true
				continue
			}
		}
		top = false
		var err *Error
		switch {
		case c == ':':
			lx.pos++
			lx.emit(token{kind: tColon, line: line, col: col})
		case c == '"':
			err = lx.lexString(line, col)
		case c == '#':
			err = lx.lexDate(line, col)
		case isDigit(c):
			err = lx.lexNumber(line, col)
		case c == '.' && lx.pos+1 < len(lx.src) && isDigit(lx.src[lx.pos+1]) && !lx.afterOperand():
			err = lx.lexNumber(line, col)
		case c == '&' && lx.radixAhead():
			err = lx.lexRadix(line, col)
		case isIdentStart(c) || c >= utf8.RuneSelf:
			err = lx.lexIdent(line, col)
		case c == '[':
			end := strings.IndexAny(lx.src[lx.pos:], "]\r\n")
			if end < 0 || lx.src[lx.pos+end] != ']' {
				return lx.errAt(1007, line, col)
			}
			name := lx.src[lx.pos+1 : lx.pos+end]
			lx.pos += end + 1
			lx.emit(token{kind: tIdent, text: name, lower: strings.ToLower(name), line: line, col: col, bracket: true})
		default:
			err = lx.lexOp(line, col)
		}
		if err != nil {
			return err
		}
	}
	line, col := lx.line, lx.pos-lx.bol+1
	lx.emit(token{kind: tNL, line: line, col: col})
	lx.emit(token{kind: tEOF, line: line, col: col})
	return nil
}

// afterOperand reports whether the previous token directly precedes the
// current position and can be the object of a member access, in which case a
// '.' is a member operator rather than the start of a number such as ".5".
func (lx *lexer) afterOperand() bool {
	if lx.space || len(lx.toks) == 0 {
		return false
	}
	t := lx.toks[len(lx.toks)-1]
	return t.kind == tIdent || (t.kind == tOp && t.text == ")")
}

func (lx *lexer) radixAhead() bool {
	if lx.pos+2 >= len(lx.src) {
		return false
	}
	isRadixDigit := isHexDigit
	switch lx.src[lx.pos+1] {
	case 'h', 'H':
	case 'o', 'O':
		isRadixDigit = func(c byte) bool { return c >= '0' && c <= '7' }
	default:
		return false
	}
	i := lx.pos + 2
	for i < len(lx.src) && isRadixDigit(lx.src[i]) {
		i++
	}
	if i == lx.pos+2 {
		return false
	}
	// `"x" &header` concatenates with an identifier; it is not &HEAD.
	if i < len(lx.src) && (isIdentStart(lx.src[i]) || isDigit(lx.src[i]) || lx.src[i] >= utf8.RuneSelf) {
		return false
	}
	return true
}

func (lx *lexer) lexRadix(line, col int) *Error {
	base := 16
	if c := lx.src[lx.pos+1]; c == 'o' || c == 'O' {
		base = 8
	}
	i := lx.pos + 2
	start := i
	for i < len(lx.src) && isHexDigit(lx.src[i]) {
		if base == 8 && (lx.src[i] < '0' || lx.src[i] > '7') {
			break
		}
		i++
	}
	u, err := strconv.ParseUint(lx.src[start:i], base, 64)
	if err != nil {
		return lx.errAt(1031, line, col)
	}
	long := false
	if i < len(lx.src) && lx.src[i] == '&' {
		long = true
		i++
	}
	lx.pos = i
	lx.emit(token{kind: tNumber, val: radixLiteral(u, long), line: line, col: col})
	return nil
}

func (lx *lexer) lexNumber(line, col int) *Error {
	s := lx.src
	start := lx.pos
	i := lx.pos
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	isFloat := false
	if i < len(s) && s[i] == '.' {
		switch {
		case i+1 < len(s) && isDigit(s[i+1]):
			isFloat = true
			i++
			for i < len(s) && isDigit(s[i]) {
				i++
			}
		case i+1 >= len(s) || !(isIdentStart(s[i+1]) || s[i+1] >= utf8.RuneSelf):
			isFloat = true // "1." is a Double
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && isDigit(s[j]) {
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			isFloat = true
			i = j
		}
	}
	text := s[start:i]
	lx.pos = i
	var val Value
	if !isFloat {
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			val = intValue(n)
		} else {
			isFloat = true
		}
	}
	if isFloat {
		f, err := strconv.ParseFloat(strings.TrimSuffix(text, "."), 64)
		if err != nil {
			return lx.errAt(1031, line, col)
		}
		val = f
	}
	lx.emit(token{kind: tNumber, text: text, val: val, line: line, col: col})
	return nil
}

func (lx *lexer) lexString(line, col int) *Error {
	s := lx.src
	i := lx.pos + 1
	var sb strings.Builder
	seg := i
	for {
		if i >= len(s) || s[i] == '\n' || s[i] == '\r' {
			return lx.errAt(1033, line, col)
		}
		if s[i] == '"' {
			sb.WriteString(s[seg:i])
			if i+1 < len(s) && s[i+1] == '"' {
				sb.WriteByte('"')
				i += 2
				seg = i
				continue
			}
			i++
			break
		}
		i++
	}
	lx.pos = i
	lx.emit(token{kind: tString, val: sb.String(), line: line, col: col})
	return nil
}

func (lx *lexer) lexDate(line, col int) *Error {
	rest := lx.src[lx.pos+1:]
	end := strings.IndexAny(rest, "#\r\n")
	if end < 0 || rest[end] != '#' {
		return lx.errAt(1002, line, col)
	}
	t, ok := parseDate(rest[:end])
	if !ok {
		return lx.errAt(1002, line, col)
	}
	lx.pos += end + 2
	lx.emit(token{kind: tDate, val: t, line: line, col: col})
	return nil
}

func (lx *lexer) lexIdent(line, col int) *Error {
	s := lx.src
	start := lx.pos
	i := lx.pos
	for i < len(s) {
		c := s[i]
		if isIdentStart(c) || isDigit(c) {
			i++
			continue
		}
		if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRuneInString(s[i:])
			if r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				i += n
				continue
			}
		}
		break
	}
	if i == start {
		return lx.errAt(1032, line, col)
	}
	text := s[start:i]
	lower := strings.ToLower(text)
	lx.pos = i
	if lower == "rem" && lx.atStmtStart() {
		lx.skipLine()
		return nil
	}
	lx.emit(token{kind: tIdent, text: text, lower: lower, line: line, col: col})
	return nil
}

func (lx *lexer) lexOp(line, col int) *Error {
	s := lx.src
	c := s[lx.pos]
	next := byte(0)
	if lx.pos+1 < len(s) {
		next = s[lx.pos+1]
	}
	op := ""
	switch c {
	case '<':
		switch next {
		case '>':
			op = "<>"
		case '=':
			op = "<="
		default:
			op = "<"
		}
	case '>':
		switch next {
		case '=':
			op = ">="
		case '<':
			op = "<>"
		default:
			op = ">"
		}
	case '=':
		switch next {
		case '<':
			op = "<="
		case '>':
			op = ">="
		default:
			op = "="
		}
	case '+', '-', '*', '/', '\\', '^', '&', '(', ')', ',', '.':
		op = string(c)
	default:
		return lx.errAt(1032, line, col)
	}
	width := 1
	if len(op) == 2 {
		width = 2
	}
	lx.pos += width
	lx.emit(token{kind: tOp, text: op, line: line, col: col})
	return nil
}
