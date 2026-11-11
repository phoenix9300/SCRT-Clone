package vbs

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// regExp is the VBScript.RegExp object. Patterns are translated to Go's RE2
// syntax (see translateRegex for what cannot be translated).
type regExp struct {
	pattern    string
	global     bool
	ignoreCase bool
	multiline  bool

	compiled *regexp.Regexp
	key      string
}

func (re *regExp) TypeName() string { return "IRegExp2" }

func regexErr(msg string) *Error {
	return newErrMsg(5017, "Syntax error in regular expression: "+msg)
}

func isHexRune(r rune) bool {
	return r < utf8.RuneSelf && isHexDigit(byte(r))
}

// translateRegex converts a VBScript (ECMAScript 3 flavoured) pattern to
// RE2. Constructs RE2 cannot express are rejected with error 5017:
// backreferences (\1 ...) and lookahead ((?=...), (?!...)).
func translateRegex(pat string) (string, error) {
	rs := []rune(pat)
	var sb strings.Builder
	inClass := false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\':
			if i+1 >= len(rs) {
				return "", regexErr("trailing backslash")
			}
			i++
			e := rs[i]
			switch {
			case e >= '1' && e <= '9':
				return "", regexErr("backreferences (\\" + string(e) + ") are not supported")
			case e == '0':
				sb.WriteString(`\x00`)
			case strings.ContainsRune("dDwWsSnrtfv", e):
				sb.WriteRune('\\')
				sb.WriteRune(e)
			case e == 'b':
				if inClass {
					sb.WriteString(`\x08`)
				} else {
					sb.WriteString(`\b`)
				}
			case e == 'B':
				if inClass {
					sb.WriteString("B")
				} else {
					sb.WriteString(`\B`)
				}
			case e == 'x' && i+2 < len(rs) && isHexRune(rs[i+1]) && isHexRune(rs[i+2]):
				sb.WriteString(`\x{` + string(rs[i+1:i+3]) + `}`)
				i += 2
			case e == 'u' && i+4 < len(rs) && isHexRune(rs[i+1]) && isHexRune(rs[i+2]) && isHexRune(rs[i+3]) && isHexRune(rs[i+4]):
				sb.WriteString(`\x{` + string(rs[i+1:i+5]) + `}`)
				i += 4
			case e == 'c' && i+1 < len(rs) && rs[i+1] < utf8.RuneSelf && isIdentStart(byte(rs[i+1])) && rs[i+1] != '_':
				fmt.Fprintf(&sb, `\x{%x}`, rs[i+1]&31)
				i++
			case e < utf8.RuneSelf && (isIdentStart(byte(e)) || isDigit(byte(e))):
				// An escaped letter without special meaning is the letter.
				sb.WriteString(regexp.QuoteMeta(string(e)))
			case e < utf8.RuneSelf:
				sb.WriteRune('\\')
				sb.WriteRune(e)
			default:
				sb.WriteRune(e)
			}
		case inClass:
			switch c {
			case ']':
				inClass = false
				sb.WriteRune(c)
			case '[':
				sb.WriteString(`\[`)
			default:
				sb.WriteRune(c)
			}
		case c == '[':
			// ECMAScript: [] matches nothing and [^] matches anything.
			if i+1 < len(rs) && rs[i+1] == ']' {
				sb.WriteString(`[^\x00-\x{10FFFF}]`)
				i++
				continue
			}
			if i+2 < len(rs) && rs[i+1] == '^' && rs[i+2] == ']' {
				sb.WriteString(`[\x00-\x{10FFFF}]`)
				i += 2
				continue
			}
			inClass = true
			sb.WriteRune(c)
		case c == '(' && i+1 < len(rs) && rs[i+1] == '?':
			if i+2 < len(rs) {
				switch rs[i+2] {
				case ':':
					sb.WriteString("(?:")
					i += 2
					continue
				case '=', '!':
					return "", regexErr("lookahead ((?" + string(rs[i+2]) + "...) is not supported")
				}
			}
			return "", regexErr("unsupported group syntax")
		default:
			sb.WriteRune(c)
		}
	}
	if inClass {
		return "", newErr(5019, "")
	}
	return sb.String(), nil
}

func (re *regExp) compile() (*regexp.Regexp, error) {
	key := fmt.Sprintf("%t|%t|%s", re.ignoreCase, re.multiline, re.pattern)
	if re.compiled != nil && re.key == key {
		return re.compiled, nil
	}
	body, err := translateRegex(re.pattern)
	if err != nil {
		return nil, err
	}
	flags := ""
	if re.ignoreCase {
		flags += "i"
	}
	if re.multiline {
		flags += "m"
	}
	if flags != "" {
		body = "(?" + flags + ")" + body
	}
	c, cerr := regexp.Compile(body)
	if cerr != nil {
		msg := cerr.Error()
		if i := strings.Index(msg, ": "); i >= 0 {
			msg = msg[i+2:]
		}
		return nil, regexErr(msg)
	}
	re.compiled, re.key = c, key
	return c, nil
}

// expandReplacement appends the replacement text for one match, expanding
// $1..$99, $&, $`, $', $_ and $$.
func expandReplacement(sb *strings.Builder, repl, src string, m []int) {
	groups := len(m)/2 - 1
	for i := 0; i < len(repl); i++ {
		c := repl[i]
		if c != '$' || i+1 >= len(repl) {
			sb.WriteByte(c)
			continue
		}
		n := repl[i+1]
		switch {
		case n == '$':
			sb.WriteByte('$')
			i++
		case n == '&':
			sb.WriteString(src[m[0]:m[1]])
			i++
		case n == '`':
			sb.WriteString(src[:m[0]])
			i++
		case n == '\'':
			sb.WriteString(src[m[1]:])
			i++
		case n == '_':
			sb.WriteString(src)
			i++
		case isDigit(n):
			g, width := int(n-'0'), 1
			if i+2 < len(repl) && isDigit(repl[i+2]) {
				if g2 := g*10 + int(repl[i+2]-'0'); g2 >= 1 && g2 <= groups {
					g, width = g2, 2
				}
			}
			if g < 1 || g > groups {
				sb.WriteByte(c)
				continue
			}
			if m[2*g] >= 0 {
				sb.WriteString(src[m[2*g]:m[2*g+1]])
			}
			i += width
		default:
			sb.WriteByte(c)
		}
	}
}

// regMatch is a Match object.
type regMatch struct {
	value  string
	first  int
	length int
	subs   []Value
}

func (m *regMatch) TypeName() string { return "IMatch2" }

func (m *regMatch) Get(name string, args []Value) (Value, error) {
	switch name {
	case "", "value":
		return m.value, nil
	case "firstindex":
		return int64(m.first), nil
	case "length":
		return int64(m.length), nil
	case "submatches":
		c := &collection{typeName: "ISubMatches", items: m.subs}
		if len(args) > 0 {
			return c.Get("item", args)
		}
		return c, nil
	}
	return nil, newErr(438, "")
}

func (m *regMatch) Set(string, []Value, Value) error { return newErr(438, "") }

func (re *regExp) Get(name string, args []Value) (Value, error) {
	switch name {
	case "pattern":
		return re.pattern, nil
	case "global":
		return re.global, nil
	case "ignorecase":
		return re.ignoreCase, nil
	case "multiline":
		return re.multiline, nil
	case "test":
		src, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		c, err := re.compile()
		if err != nil {
			return nil, err
		}
		return c.MatchString(src), nil
	case "replace":
		if err := needArgs(args, 2, 2); err != nil {
			return nil, err
		}
		src, err := toStr(args[0])
		if err != nil {
			return nil, err
		}
		repl, err := toStr(args[1])
		if err != nil {
			return nil, err
		}
		c, err := re.compile()
		if err != nil {
			return nil, err
		}
		limit := 1
		if re.global {
			limit = -1
		}
		var sb strings.Builder
		last := 0
		for _, m := range c.FindAllStringSubmatchIndex(src, limit) {
			sb.WriteString(src[last:m[0]])
			expandReplacement(&sb, repl, src, m)
			last = m[1]
		}
		sb.WriteString(src[last:])
		return sb.String(), nil
	case "execute":
		src, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		c, err := re.compile()
		if err != nil {
			return nil, err
		}
		limit := 1
		if re.global {
			limit = -1
		}
		var items []Value
		// Character offsets are derived incrementally from byte offsets.
		bytePos, charPos := 0, 0
		for _, m := range c.FindAllStringSubmatchIndex(src, limit) {
			charPos += utf8.RuneCountInString(src[bytePos:m[0]])
			bytePos = m[0]
			val := src[m[0]:m[1]]
			match := &regMatch{value: val, first: charPos, length: utf8.RuneCountInString(val)}
			for g := 1; g < len(m)/2; g++ {
				if m[2*g] >= 0 {
					match.subs = append(match.subs, src[m[2*g]:m[2*g+1]])
				} else {
					match.subs = append(match.subs, nil)
				}
			}
			items = append(items, match)
		}
		return &collection{typeName: "IMatchCollection2", items: items}, nil
	}
	return nil, newErr(438, "")
}

func (re *regExp) Set(name string, args []Value, v Value) error {
	var err error
	switch name {
	case "pattern":
		re.pattern, err = toStr(v)
	case "global":
		re.global, err = toBool(v)
	case "ignorecase":
		re.ignoreCase, err = toBool(v)
	case "multiline":
		re.multiline, err = toBool(v)
	default:
		return newErr(438, "")
	}
	return err
}
