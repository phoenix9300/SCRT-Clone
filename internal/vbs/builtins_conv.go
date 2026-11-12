package vbs

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// roundDecimal rounds the non-negative number a to the given number of
// decimals working on its shortest decimal representation, which is how
// VBScript avoids binary artefacts (Round(2.675, 2) is 2.68). It returns
// the integer and fraction digit strings. bankers selects round-half-even;
// otherwise halves round up.
func roundDecimal(a float64, decimals int, bankers bool) (ip, fp string) {
	s := strconv.FormatFloat(a, 'f', -1, 64)
	ip, fp, _ = strings.Cut(s, ".")
	if len(fp) <= decimals {
		return ip, fp + strings.Repeat("0", decimals-len(fp))
	}
	rest := fp[decimals:]
	fp = fp[:decimals]
	up := false
	switch {
	case rest[0] > '5':
		up = true
	case rest[0] == '5':
		if strings.Trim(rest[1:], "0") != "" || !bankers {
			up = true
		} else {
			// Exactly half: round to even.
			last := byte('0')
			if decimals > 0 {
				last = fp[decimals-1]
			} else if len(ip) > 0 {
				last = ip[len(ip)-1]
			}
			up = (last-'0')%2 == 1
		}
	}
	if up {
		digits := []byte(ip + fp)
		i := len(digits) - 1
		for ; i >= 0; i-- {
			if digits[i] == '9' {
				digits[i] = '0'
				continue
			}
			digits[i]++
			break
		}
		if i < 0 {
			digits = append([]byte{'1'}, digits...)
		}
		ip = string(digits[:len(digits)-decimals])
		fp = string(digits[len(digits)-decimals:])
	}
	return ip, fp
}

// roundTo implements Round(x, decimals).
func roundTo(f float64, decimals int) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || decimals > 20 {
		return f
	}
	neg := f < 0
	ip, fp := roundDecimal(math.Abs(f), decimals, true)
	s := ip
	if fp != "" {
		s += "." + fp
	}
	r, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return f
	}
	if neg {
		r = -r
	}
	return r
}

// numericArg resolves a numeric function argument; isNull reports Null.
func numericArg(v Value) (out Value, null bool, err error) {
	if v, err = scalar(v); err != nil {
		return nil, false, err
	}
	if isNull(v) {
		return nil, true, nil
	}
	return v, false, nil
}

// mathFn registers a one-argument floating point function.
func mathFn(name string, fn func(float64) (float64, error)) {
	reg(name, 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		f, err := toFloat(args[0])
		if err != nil {
			return nil, err
		}
		r, err := fn(f)
		if err != nil {
			return nil, err
		}
		if math.IsNaN(r) {
			return nil, newErr(5, "")
		}
		if math.IsInf(r, 0) {
			return nil, newErr(6, "")
		}
		return r, nil
	})
}

func radixString(n int64, base int) string {
	if n >= 0 {
		return strings.ToUpper(strconv.FormatInt(n, base))
	}
	var u uint64
	switch {
	case n >= math.MinInt16:
		u = uint64(uint16(n))
	case n >= math.MinInt32:
		u = uint64(uint32(n))
	default:
		u = uint64(n)
	}
	return strings.ToUpper(strconv.FormatUint(u, base))
}

func init() {
	reg("CStr", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return toStr(args[0])
	})
	reg("CInt", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < math.MinInt16 || n > math.MaxInt16 {
			return nil, newErr(6, "")
		}
		return n, nil
	})
	reg("CLng", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < math.MinInt32 || n > math.MaxInt32 {
			return nil, newErr(6, "")
		}
		return n, nil
	})
	reg("CByte", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < 0 || n > 255 {
			return nil, newErr(6, "")
		}
		return n, nil
	})
	reg("CDbl", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return toFloat(args[0])
	})
	reg("CSng", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		f, err := toFloat(args[0])
		if err != nil {
			return nil, err
		}
		if math.Abs(f) > math.MaxFloat32 {
			return nil, newErr(6, "")
		}
		// Keep the shortest decimal form of the Single so that CSng(0.1)
		// still prints as 0.1.
		r, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'g', -1, 32), 64)
		return r, nil
	})
	reg("CCur", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		f, err := toFloat(args[0])
		if err != nil {
			return nil, err
		}
		return roundTo(f, 4), nil
	})
	reg("CBool", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return toBool(args[0])
	})
	reg("CDate", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return toDate(args[0])
	})
	reg("Hex", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		v, null, err := numericArg(args[0])
		if err != nil || null {
			return Null, err
		}
		n, err := toInt(v)
		if err != nil {
			return nil, err
		}
		return radixString(n, 16), nil
	})
	reg("Oct", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		v, null, err := numericArg(args[0])
		if err != nil || null {
			return Null, err
		}
		n, err := toInt(v)
		if err != nil {
			return nil, err
		}
		return radixString(n, 8), nil
	})
	reg("Int", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		v, null, err := numericArg(args[0])
		if err != nil || null {
			return Null, err
		}
		if n, ok := intOperand(v); ok {
			return n, nil
		}
		f, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		return math.Floor(f), nil
	})
	reg("Fix", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		v, null, err := numericArg(args[0])
		if err != nil || null {
			return Null, err
		}
		if n, ok := intOperand(v); ok {
			return n, nil
		}
		f, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		return math.Trunc(f), nil
	})
	reg("Abs", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		v, null, err := numericArg(args[0])
		if err != nil || null {
			return Null, err
		}
		if n, ok := intOperand(v); ok {
			return intValue(absInt(n)), nil
		}
		f, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		return math.Abs(f), nil
	})
	reg("Sgn", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		f, err := toFloat(args[0])
		if err != nil {
			return nil, err
		}
		switch {
		case f > 0:
			return int64(1), nil
		case f < 0:
			return int64(-1), nil
		}
		return int64(0), nil
	})
	mathFn("Sqr", func(f float64) (float64, error) {
		if f < 0 {
			return 0, newErr(5, "")
		}
		return math.Sqrt(f), nil
	})
	mathFn("Exp", func(f float64) (float64, error) { return math.Exp(f), nil })
	mathFn("Log", func(f float64) (float64, error) {
		if f <= 0 {
			return 0, newErr(5, "")
		}
		return math.Log(f), nil
	})
	mathFn("Sin", func(f float64) (float64, error) { return math.Sin(f), nil })
	mathFn("Cos", func(f float64) (float64, error) { return math.Cos(f), nil })
	mathFn("Tan", func(f float64) (float64, error) { return math.Tan(f), nil })
	mathFn("Atn", func(f float64) (float64, error) { return math.Atan(f), nil })

	reg("Round", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		v, null, err := numericArg(args[0])
		if err != nil || null {
			return Null, err
		}
		digits, err := optInt(args, 1, 0)
		if err != nil {
			return nil, err
		}
		if digits < 0 {
			return nil, newErr(5, "")
		}
		if n, ok := intOperand(v); ok {
			return n, nil
		}
		f, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		return roundTo(f, int(digits)), nil
	})

	reg("Rnd", 0, 1, func(in *Interpreter, args []Value) (Value, error) {
		if v, ok := optArg(args, 0); ok {
			f, err := toFloat(v)
			if err != nil {
				return nil, err
			}
			switch {
			case f < 0:
				bits := math.Float32bits(float32(f))
				in.rndSeed = (bits + (bits >> 24)) & 0xffffff
			case f == 0:
				return in.rndLast, nil
			}
		}
		in.rndSeed = (in.rndSeed*0x43fd43fd + 0xc39ec3) & 0xffffff
		// Rnd yields a Single: keep its shortest decimal form.
		in.rndLast, _ = strconv.ParseFloat(strconv.FormatFloat(float64(in.rndSeed)/16777216.0, 'g', -1, 32), 64)
		return in.rndLast, nil
	})
	reg("Randomize", 0, 1, func(in *Interpreter, args []Value) (Value, error) {
		var seed float64
		if v, ok := optArg(args, 0); ok {
			f, err := toFloat(v)
			if err != nil {
				return nil, err
			}
			seed = f
		} else {
			n := time.Now()
			seed = float64(n.Hour()*3600+n.Minute()*60+n.Second()) + float64(n.Nanosecond())/1e9
		}
		hi := uint32(math.Float64bits(seed) >> 32)
		hi = ((hi & 0xffff) ^ (hi >> 16)) << 8
		in.rndSeed = (in.rndSeed & 0xff0000ff) | hi
		in.rndSeed &= 0xffffff
		return nil, nil
	})
}
