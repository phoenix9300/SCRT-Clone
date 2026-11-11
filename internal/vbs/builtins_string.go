package vbs

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxStringLen bounds strings built by String() and Space().
const maxStringLen = 1 << 28

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// runeLen returns the length of s in characters.
func runeLen(s string) int {
	if isASCII(s) {
		return len(s)
	}
	return utf8.RuneCountInString(s)
}

// byteOffset returns the byte offset of the character with index n, or
// len(s) when s is shorter.
func byteOffset(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if isASCII(s) {
		if n > len(s) {
			return len(s)
		}
		return n
	}
	off := 0
	for i := 0; i < n && off < len(s); i++ {
		_, w := utf8.DecodeRuneInString(s[off:])
		off += w
	}
	return off
}

// runeSlice returns n characters of s starting at character index start
// (n < 0: the rest).
func runeSlice(s string, start, n int) string {
	a := byteOffset(s, start)
	if n < 0 {
		return s[a:]
	}
	return s[a : a+byteOffset(s[a:], n)]
}

// strArg resolves a string argument; null reports a Null argument.
func strArg(v Value) (s string, null bool, err error) {
	if v, err = scalar(v); err != nil {
		return "", false, err
	}
	if isNull(v) {
		return "", true, nil
	}
	s, err = toStr(v)
	return s, false, err
}

// compareArg reads an optional compare-mode argument (1 = text compare).
func compareArg(args []Value, i int) (bool, error) {
	n, err := optInt(args, i, 0)
	if err != nil {
		return false, err
	}
	if n < 0 || n > 2 {
		return false, newErr(5, "")
	}
	return n == 1, nil
}

// tristate reads an optional Tristate argument (-1 True, 0 False, -2
// default).
func tristate(args []Value, i int, def bool) (bool, error) {
	n, err := optInt(args, i, -2)
	if err != nil {
		return false, err
	}
	switch n {
	case -2:
		return def, nil
	case 0:
		return false, nil
	}
	return true, nil
}

// instr implements InStr; start is 1-based.
func instr(s1, s2 string, start int, text bool) int64 {
	n1 := runeLen(s1)
	if n1 == 0 || start > n1 {
		return 0
	}
	if s2 == "" {
		return int64(start)
	}
	if text {
		s1, s2 = strings.ToLower(s1), strings.ToLower(s2)
	}
	off := byteOffset(s1, start-1)
	i := strings.Index(s1[off:], s2)
	if i < 0 {
		return 0
	}
	return int64(start + runeLen(s1[off:off+i]))
}

// instrRev implements InStrRev; start is 1-based, -1 means the end.
func instrRev(s1, s2 string, start int, text bool) int64 {
	n1 := runeLen(s1)
	if start == -1 {
		start = n1
	}
	if n1 == 0 || start > n1 {
		return 0
	}
	if s2 == "" {
		return int64(start)
	}
	if text {
		s1, s2 = strings.ToLower(s1), strings.ToLower(s2)
	}
	head := s1[:byteOffset(s1, start)]
	i := strings.LastIndex(head, s2)
	if i < 0 {
		return 0
	}
	return int64(runeLen(head[:i]) + 1)
}

// foldIndex finds needle in s ignoring case, returning the rune index.
func foldIndex(s, lneedle []rune, from int) int {
	if len(lneedle) == 0 {
		return from
	}
	for i := from; i+len(lneedle) <= len(s); i++ {
		j := 0
		for j < len(lneedle) && s[i+j] == lneedle[j] {
			j++
		}
		if j == len(lneedle) {
			return i
		}
	}
	return -1
}

// replaceStr implements Replace.
func replaceStr(s, find, repl string, start, count int, text bool) string {
	if start > 1 {
		s = runeSlice(s, start-1, -1)
	}
	if find == "" || count == 0 || s == "" {
		return s
	}
	if !text {
		return strings.Replace(s, find, repl, count)
	}
	rs := []rune(s)
	ls := []rune(strings.ToLower(s))
	lf := []rune(strings.ToLower(find))
	var sb strings.Builder
	pos := 0
	for count != 0 {
		i := foldIndex(ls, lf, pos)
		if i < 0 {
			break
		}
		sb.WriteString(string(rs[pos:i]))
		sb.WriteString(repl)
		pos = i + len(lf)
		if count > 0 {
			count--
		}
	}
	sb.WriteString(string(rs[pos:]))
	return sb.String()
}

// splitStr implements Split.
func splitStr(s, delim string, count int, text bool) []Value {
	if s == "" || count == 0 {
		return []Value{}
	}
	if delim == "" {
		return []Value{s}
	}
	var parts []Value
	if !text {
		for _, p := range strings.SplitN(s, delim, count) {
			parts = append(parts, p)
		}
		return parts
	}
	rs := []rune(s)
	ls := []rune(strings.ToLower(s))
	ld := []rune(strings.ToLower(delim))
	pos := 0
	for count < 0 || len(parts) < count-1 {
		i := foldIndex(ls, ld, pos)
		if i < 0 {
			break
		}
		parts = append(parts, string(rs[pos:i]))
		pos = i + len(ld)
	}
	return append(parts, string(rs[pos:]))
}

func groupThousands(ip string) string {
	if len(ip) <= 3 {
		return ip
	}
	var sb strings.Builder
	lead := len(ip) % 3
	if lead > 0 {
		sb.WriteString(ip[:lead])
	}
	for i := lead; i < len(ip); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(ip[i : i+3])
	}
	return sb.String()
}

// formatFixed renders |f| with the given number of decimals (rounding
// halves up, as the Format* functions do) and reports whether the rounded
// value is negative.
func formatFixed(f float64, digits int, lead, group bool) (string, bool) {
	neg := f < 0
	ip, fp := roundDecimal(math.Abs(f), digits, false)
	if strings.Trim(ip+fp, "0") == "" {
		neg = false
	}
	if group {
		ip = groupThousands(ip)
	}
	if !lead && ip == "0" && fp != "" {
		ip = ""
	}
	if fp != "" {
		ip += "." + fp
	}
	return ip, neg
}

// formatArgs reads the common arguments of FormatNumber and friends.
func formatArgs(args []Value, parensDef bool) (f float64, digits int, lead, parens, group bool, err error) {
	if f, err = toFloat(args[0]); err != nil {
		return
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		err = newErr(6, "")
		return
	}
	d, err := optInt(args, 1, -1)
	if err != nil {
		return
	}
	switch {
	case d == -1:
		d = 2
	case d < 0 || d > 255:
		err = newErr(5, "")
		return
	}
	digits = int(d)
	if lead, err = tristate(args, 2, true); err != nil {
		return
	}
	if parens, err = tristate(args, 3, parensDef); err != nil {
		return
	}
	group, err = tristate(args, 4, true)
	return
}

func escapeString(s string) string {
	const hexd = "0123456789ABCDEF"
	var sb strings.Builder
	for _, u := range utf16.Encode([]rune(s)) {
		switch {
		case u < 128 && (isDigit(byte(u)) || isIdentStart(byte(u)) || strings.IndexByte("@*+-./", byte(u)) >= 0):
			sb.WriteByte(byte(u))
		case u < 256:
			sb.WriteByte('%')
			sb.WriteByte(hexd[u>>4])
			sb.WriteByte(hexd[u&15])
		default:
			sb.WriteString("%u")
			for shift := 12; shift >= 0; shift -= 4 {
				sb.WriteByte(hexd[(u>>uint(shift))&15])
			}
		}
	}
	return sb.String()
}

func unescapeString(s string) string {
	var units []uint16
	for i := 0; i < len(s); {
		if s[i] == '%' {
			if i+5 < len(s) && (s[i+1] == 'u' || s[i+1] == 'U') {
				if n, err := strconv.ParseUint(s[i+2:i+6], 16, 16); err == nil {
					units = append(units, uint16(n))
					i += 6
					continue
				}
			}
			if i+2 < len(s) {
				if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					units = append(units, uint16(n))
					i += 3
					continue
				}
			}
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		units = append(units, utf16.Encode([]rune{r})...)
		i += w
	}
	return string(utf16.Decode(units))
}

// strFn registers a one-argument string function that maps Null to Null.
func strFn(name string, fn func(string) (Value, error)) {
	reg(name, 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		s, null, err := strArg(args[0])
		if err != nil || null {
			return Null, err
		}
		return fn(s)
	})
}

func init() {
	strFn("Len", func(s string) (Value, error) { return int64(runeLen(s)), nil })
	strFn("LenB", func(s string) (Value, error) { return int64(2 * runeLen(s)), nil })
	strFn("LCase", func(s string) (Value, error) { return strings.ToLower(s), nil })
	strFn("UCase", func(s string) (Value, error) { return strings.ToUpper(s), nil })
	strFn("Trim", func(s string) (Value, error) { return strings.Trim(s, " "), nil })
	strFn("LTrim", func(s string) (Value, error) { return strings.TrimLeft(s, " "), nil })
	strFn("RTrim", func(s string) (Value, error) { return strings.TrimRight(s, " "), nil })
	strFn("StrReverse", func(s string) (Value, error) {
		rs := []rune(s)
		for i, j := 0, len(rs)-1; i < j; i, j = i+1, j-1 {
			rs[i], rs[j] = rs[j], rs[i]
		}
		return string(rs), nil
	})
	strFn("Escape", func(s string) (Value, error) { return escapeString(s), nil })
	strFn("Unescape", func(s string) (Value, error) { return unescapeString(s), nil })

	reg("Left", 2, 2, func(in *Interpreter, args []Value) (Value, error) {
		s, null, err := strArg(args[0])
		if err != nil {
			return nil, err
		}
		n, err := toInt(args[1])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, newErr(5, "")
		}
		if null {
			return Null, nil
		}
		if n >= int64(len(s)) {
			return s, nil
		}
		return runeSlice(s, 0, int(n)), nil
	})
	reg("Right", 2, 2, func(in *Interpreter, args []Value) (Value, error) {
		s, null, err := strArg(args[0])
		if err != nil {
			return nil, err
		}
		n, err := toInt(args[1])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, newErr(5, "")
		}
		if null {
			return Null, nil
		}
		total := int64(runeLen(s))
		if n >= total {
			return s, nil
		}
		return runeSlice(s, int(total-n), -1), nil
	})
	reg("Mid", 2, 3, func(in *Interpreter, args []Value) (Value, error) {
		s, null, err := strArg(args[0])
		if err != nil {
			return nil, err
		}
		start, err := toInt(args[1])
		if err != nil {
			return nil, err
		}
		length := int64(-1)
		if v, ok := optArg(args, 2); ok {
			if length, err = toInt(v); err != nil {
				return nil, err
			}
			if length < 0 {
				return nil, newErr(5, "")
			}
		}
		if start < 1 {
			return nil, newErr(5, "")
		}
		if null {
			return Null, nil
		}
		if start > int64(len(s)) {
			return "", nil
		}
		if length > int64(len(s)) {
			length = -1
		}
		return runeSlice(s, int(start-1), int(length)), nil
	})
	reg("InStr", 2, 4, func(in *Interpreter, args []Value) (Value, error) {
		start := int64(1)
		rest := args
		if len(args) >= 3 {
			if isNull(args[0]) {
				return nil, newErr(94, "")
			}
			n, err := toInt(args[0])
			if err != nil {
				return nil, err
			}
			if n < 1 {
				return nil, newErr(5, "")
			}
			start = n
			rest = args[1:]
		}
		text, err := compareArg(rest, 2)
		if err != nil {
			return nil, err
		}
		s1, null1, err := strArg(rest[0])
		if err != nil {
			return nil, err
		}
		s2, null2, err := strArg(rest[1])
		if err != nil {
			return nil, err
		}
		if null1 || null2 {
			return Null, nil
		}
		if start > int64(len(s1))+1 {
			return int64(0), nil
		}
		return instr(s1, s2, int(start), text), nil
	})
	reg("InStrRev", 2, 4, func(in *Interpreter, args []Value) (Value, error) {
		s1, null1, err := strArg(args[0])
		if err != nil {
			return nil, err
		}
		s2, null2, err := strArg(args[1])
		if err != nil {
			return nil, err
		}
		start, err := optInt(args, 2, -1)
		if err != nil {
			return nil, err
		}
		if start == 0 || start < -1 {
			return nil, newErr(5, "")
		}
		text, err := compareArg(args, 3)
		if err != nil {
			return nil, err
		}
		if null1 || null2 {
			return Null, nil
		}
		if start > int64(len(s1)) {
			return int64(0), nil
		}
		return instrRev(s1, s2, int(start), text), nil
	})
	reg("Replace", 3, 6, func(in *Interpreter, args []Value) (Value, error) {
		var s [3]string
		for i := range s {
			v, null, err := strArg(args[i])
			if err != nil {
				return nil, err
			}
			if null {
				return nil, newErr(94, "")
			}
			s[i] = v
		}
		start, err := optInt(args, 3, 1)
		if err != nil {
			return nil, err
		}
		count, err := optInt(args, 4, -1)
		if err != nil {
			return nil, err
		}
		if start < 1 || count < -1 {
			return nil, newErr(5, "")
		}
		text, err := compareArg(args, 5)
		if err != nil {
			return nil, err
		}
		if start > int64(len(s[0])) {
			return "", nil
		}
		if count > math.MaxInt32 {
			count = -1
		}
		return replaceStr(s[0], s[1], s[2], int(start), int(count), text), nil
	})
	reg("Split", 1, 4, func(in *Interpreter, args []Value) (Value, error) {
		s, null, err := strArg(args[0])
		if err != nil {
			return nil, err
		}
		if null {
			return nil, newErr(94, "")
		}
		delim := " "
		if len(args) > 1 && args[1] != nil {
			if delim, err = toStr(args[1]); err != nil {
				return nil, err
			}
		}
		count, err := optInt(args, 2, -1)
		if err != nil {
			return nil, err
		}
		if count < -1 {
			return nil, newErr(5, "")
		}
		if count > math.MaxInt32 {
			count = -1
		}
		text, err := compareArg(args, 3)
		if err != nil {
			return nil, err
		}
		return newTempArray(splitStr(s, delim, int(count), text)), nil
	})
	reg("Join", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		arr, ok := args[0].(*Array)
		if !ok || len(arr.dims) != 1 {
			return nil, newErr(13, "")
		}
		delim := " "
		if len(args) > 1 && args[1] != nil {
			var err error
			if delim, err = toStr(args[1]); err != nil {
				return nil, err
			}
		}
		parts := make([]string, len(arr.data))
		for i, e := range arr.data {
			s, err := concatStr(e)
			if err != nil {
				return nil, err
			}
			parts[i] = s
		}
		return strings.Join(parts, delim), nil
	})
	reg("Filter", 2, 4, func(in *Interpreter, args []Value) (Value, error) {
		arr, ok := args[0].(*Array)
		if !ok || len(arr.dims) != 1 {
			return nil, newErr(13, "")
		}
		match, err := toStr(args[1])
		if err != nil {
			return nil, err
		}
		include := true
		if v, ok := optArg(args, 2); ok {
			if include, err = toBool(v); err != nil {
				return nil, err
			}
		}
		text, err := compareArg(args, 3)
		if err != nil {
			return nil, err
		}
		if text {
			match = strings.ToLower(match)
		}
		out := []Value{}
		for _, e := range arr.data {
			s, err := concatStr(e)
			if err != nil {
				return nil, err
			}
			hay := s
			if text {
				hay = strings.ToLower(hay)
			}
			if strings.Contains(hay, match) == include {
				out = append(out, s)
			}
		}
		return newTempArray(out), nil
	})
	reg("StrComp", 2, 3, func(in *Interpreter, args []Value) (Value, error) {
		s1, null1, err := strArg(args[0])
		if err != nil {
			return nil, err
		}
		s2, null2, err := strArg(args[1])
		if err != nil {
			return nil, err
		}
		text, err := compareArg(args, 2)
		if err != nil {
			return nil, err
		}
		if null1 || null2 {
			return Null, nil
		}
		if text {
			s1, s2 = strings.ToLower(s1), strings.ToLower(s2)
		}
		return int64(strings.Compare(s1, s2)), nil
	})
	reg("String", 2, 2, func(in *Interpreter, args []Value) (Value, error) {
		if isNull(args[0]) || isNull(args[1]) {
			return Null, nil
		}
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, newErr(5, "")
		}
		ch, err := scalar(args[1])
		if err != nil {
			return nil, err
		}
		var unit string
		if s, ok := ch.(string); ok {
			if s == "" {
				return nil, newErr(5, "")
			}
			r, _ := utf8.DecodeRuneInString(s)
			unit = string(r)
		} else {
			code, err := toInt(ch)
			if err != nil {
				return nil, err
			}
			unit = string(rune(code & 0xffff))
		}
		if n > maxStringLen/int64(len(unit)) {
			return nil, newErr(14, "")
		}
		return strings.Repeat(unit, int(n)), nil
	})
	reg("Space", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		if isNull(args[0]) {
			return Null, nil
		}
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, newErr(5, "")
		}
		if n > maxStringLen {
			return nil, newErr(14, "")
		}
		return strings.Repeat(" ", int(n)), nil
	})
	asc := func(in *Interpreter, args []Value) (Value, error) {
		s, null, err := strArg(args[0])
		if err != nil {
			return nil, err
		}
		if null {
			return nil, newErr(94, "")
		}
		if s == "" {
			return nil, newErr(5, "")
		}
		r, _ := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError {
			return int64(s[0]), nil
		}
		return int64(r), nil
	}
	reg("Asc", 1, 1, asc)
	reg("AscW", 1, 1, asc)
	reg("AscB", 1, 1, asc)
	reg("Chr", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < 0 || n > 255 {
			return nil, newErr(5, "")
		}
		return string(rune(n)), nil
	})
	chrW := func(in *Interpreter, args []Value) (Value, error) {
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < -32768 || n > 0x10FFFF {
			return nil, newErr(5, "")
		}
		if n < 0 {
			n += 65536
		}
		return string(rune(n)), nil
	}
	reg("ChrW", 1, 1, chrW)
	reg("ChrB", 1, 1, chrW)

	reg("FormatNumber", 1, 5, func(in *Interpreter, args []Value) (Value, error) {
		f, digits, lead, parens, group, err := formatArgs(args, false)
		if err != nil {
			return nil, err
		}
		s, neg := formatFixed(f, digits, lead, group)
		switch {
		case neg && parens:
			s = "(" + s + ")"
		case neg:
			s = "-" + s
		}
		return s, nil
	})
	reg("FormatCurrency", 1, 5, func(in *Interpreter, args []Value) (Value, error) {
		f, digits, lead, parens, group, err := formatArgs(args, true)
		if err != nil {
			return nil, err
		}
		s, neg := formatFixed(f, digits, lead, group)
		s = "$" + s
		switch {
		case neg && parens:
			s = "(" + s + ")"
		case neg:
			s = "-" + s
		}
		return s, nil
	})
	reg("FormatPercent", 1, 5, func(in *Interpreter, args []Value) (Value, error) {
		f, digits, lead, parens, group, err := formatArgs(args, false)
		if err != nil {
			return nil, err
		}
		s, neg := formatFixed(f*100, digits, lead, group)
		s += "%"
		switch {
		case neg && parens:
			s = "(" + s + ")"
		case neg:
			s = "-" + s
		}
		return s, nil
	})
}
