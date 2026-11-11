package vbs

import (
	"math"
	"strings"
	"time"
)

// scalar resolves an operand to a plain value: objects yield their default
// value, arrays are a type mismatch.
func scalar(v Value) (Value, error) {
	switch v.(type) {
	case nil, bool, int64, float64, string, time.Time, nullType:
		return v, nil
	case *Array:
		return nil, newErr(13, "")
	case Object:
		return defaultValue(v)
	}
	if n := normalize(v); n != v {
		return scalar(n)
	}
	return nil, newErr(13, "")
}

// intOperand reports whether v is an integer-like operand (Integer, Boolean
// or Empty) and returns its value.
func intOperand(v Value) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case bool:
		return boolNum(x), true
	case nil:
		return 0, true
	}
	return 0, false
}

func binaryOp(op opKind, a, b Value, hardA, hardB bool) (Value, error) {
	switch op {
	case opConcat:
		return concatOp(a, b)
	case opIs:
		return objectIs(a, b)
	case opEq, opNe, opLt, opGt, opLe, opGe:
		c, null, err := compareValues(a, b, hardA, hardB)
		if err != nil {
			return nil, err
		}
		if null {
			return Null, nil
		}
		switch op {
		case opEq:
			return c == 0, nil
		case opNe:
			return c != 0, nil
		case opLt:
			return c < 0, nil
		case opGt:
			return c > 0, nil
		case opLe:
			return c <= 0, nil
		}
		return c >= 0, nil
	case opAnd, opOr, opXor, opEqv, opImp:
		return logicOp(op, a, b)
	}
	return arithOp(op, a, b)
}

// arithOp implements + - * / \ Mod ^.
func arithOp(op opKind, a, b Value) (Value, error) {
	var err error
	if a, err = scalar(a); err != nil {
		return nil, err
	}
	if b, err = scalar(b); err != nil {
		return nil, err
	}
	if isNull(a) || isNull(b) {
		return Null, nil
	}
	if op == opAdd {
		as, aStr := a.(string)
		bs, bStr := b.(string)
		switch {
		case aStr && bStr:
			return as + bs, nil
		case aStr && b == nil:
			return as, nil
		case bStr && a == nil:
			return bs, nil
		}
	}
	ai, aInt := intOperand(a)
	bi, bInt := intOperand(b)
	if aInt && bInt {
		switch op {
		case opAdd:
			if s := ai + bi; (s > ai) == (bi > 0) {
				return intValue(s), nil
			}
			return float64(ai) + float64(bi), nil
		case opSub:
			if d := ai - bi; (d < ai) == (bi > 0) {
				return intValue(d), nil
			}
			return float64(ai) - float64(bi), nil
		case opMul:
			if absInt(ai) < 1<<31 && absInt(bi) < 1<<31 {
				return intValue(ai * bi), nil
			}
			return float64(ai) * float64(bi), nil
		}
	}
	switch op {
	case opIntDiv, opMod:
		x, err := toInt(a)
		if err != nil {
			return nil, err
		}
		y, err := toInt(b)
		if err != nil {
			return nil, err
		}
		if y == 0 {
			return nil, newErr(11, "")
		}
		if y == -1 {
			if op == opMod {
				return int64(0), nil
			}
			if x == math.MinInt64 {
				return nil, newErr(6, "")
			}
			return intValue(-x), nil
		}
		if op == opIntDiv {
			return intValue(x / y), nil
		}
		return intValue(x % y), nil
	}
	af, err := toFloat(a)
	if err != nil {
		return nil, err
	}
	bf, err := toFloat(b)
	if err != nil {
		return nil, err
	}
	_, aDate := a.(time.Time)
	_, bDate := b.(time.Time)
	var r float64
	switch op {
	case opAdd:
		r = af + bf
		if aDate || bDate {
			return floatToDate(r)
		}
	case opSub:
		r = af - bf
		if aDate && !bDate {
			return floatToDate(r)
		}
	case opMul:
		r = af * bf
	case opDiv:
		if bf == 0 {
			return nil, newErr(11, "")
		}
		r = af / bf
	case opPow:
		if af == 0 && bf < 0 {
			return nil, newErr(11, "")
		}
		r = math.Pow(af, bf)
		if math.IsNaN(r) {
			return nil, newErr(5, "")
		}
	default:
		return nil, newErrMsg(51, "Internal error: bad operator")
	}
	if math.IsInf(r, 0) || math.IsNaN(r) {
		return nil, newErr(6, "")
	}
	return r, nil
}

func absInt(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func unaryOp(op opKind, v Value) (Value, error) {
	v, err := scalar(v)
	if err != nil {
		return nil, err
	}
	if isNull(v) {
		return Null, nil
	}
	switch op {
	case opNot:
		if b, ok := v.(bool); ok {
			return !b, nil
		}
		n, err := logicInt(v)
		if err != nil {
			return nil, err
		}
		return ^n, nil
	case opNeg:
		if n, ok := intOperand(v); ok {
			if n == math.MinInt64 {
				return -float64(n), nil
			}
			return intValue(-n), nil
		}
		f, err := toFloat(v)
		if err != nil {
			return nil, err
		}
		return -f, nil
	case opPos:
		switch v.(type) {
		case int64, float64, time.Time:
			return v, nil
		}
		if n, ok := intOperand(v); ok {
			return n, nil
		}
		return toFloat(v)
	}
	return nil, newErrMsg(51, "Internal error: bad operator")
}

// concatStr converts an operand of & to a string; Null counts as "".
func concatStr(v Value) (string, error) {
	if isNull(v) {
		return "", nil
	}
	return toStr(v)
}

func concatOp(a, b Value) (Value, error) {
	if isNull(a) && isNull(b) {
		return Null, nil
	}
	as, err := concatStr(a)
	if err != nil {
		return nil, err
	}
	bs, err := concatStr(b)
	if err != nil {
		return nil, err
	}
	return as + bs, nil
}

// sameObject compares two object references for identity.
func sameObject(a, b Value) (same bool) {
	defer func() {
		if recover() != nil {
			same = false // uncomparable dynamic types are never identical
		}
	}()
	return a == b
}

func objectIs(a, b Value) (Value, error) {
	if !isObjectRef(a) || !isObjectRef(b) {
		return nil, newErr(424, "")
	}
	return sameObject(a, b), nil
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compareValues orders two values the way the VBScript comparison operators
// do. null reports that the result is Null.
//
// hardA / hardB tell whether the operand was written as a literal. VBScript
// converts the other operand to the literal's type ("5" = 5 is True for a
// string variable holding "5"), whereas a string variable compared with a
// numeric variable is simply greater than any number.
func compareValues(a, b Value, hardA, hardB bool) (c int, null bool, err error) {
	if a, err = scalar(a); err != nil {
		return 0, false, err
	}
	if b, err = scalar(b); err != nil {
		return 0, false, err
	}
	if isNull(a) || isNull(b) {
		return 0, true, nil
	}
	as, aStr := a.(string)
	bs, bStr := b.(string)
	switch {
	case aStr && bStr:
		return strings.Compare(as, bs), false, nil
	case aStr:
		c, err := compareStrOther(as, b, hardA, hardB)
		return c, false, err
	case bStr:
		c, err := compareStrOther(bs, a, hardB, hardA)
		return -c, false, err
	}
	if ai, ok := intOperand(a); ok {
		if bi, ok := intOperand(b); ok {
			return cmpInt(ai, bi), false, nil
		}
	}
	af, err := toFloat(a)
	if err != nil {
		return 0, false, err
	}
	bf, err := toFloat(b)
	if err != nil {
		return 0, false, err
	}
	return cmpFloat(af, bf), false, nil
}

// compareStrOther compares string s with the non-string value o.
func compareStrOther(s string, o Value, hardS, hardO bool) (int, error) {
	switch x := o.(type) {
	case nil:
		return strings.Compare(s, ""), nil
	case bool:
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return cmpInt(-1, boolNum(x)), nil
		case "false":
			return cmpInt(0, boolNum(x)), nil
		}
	case time.Time:
		if t, ok := parseDate(s); ok {
			return cmpFloat(dateToFloat(t), dateToFloat(x)), nil
		}
	}
	switch {
	case hardO || !hardS:
		// Numeric literal (or two variables): numeric strings are compared
		// by value against a literal; otherwise a string sorts after any
		// number.
		if hardO {
			if f, ok := parseNumber(s); ok {
				of, err := toFloat(o)
				if err != nil {
					return 0, err
				}
				return cmpFloat(f, of), nil
			}
		}
		return 1, nil
	default:
		// String literal against a numeric variable: compare as strings.
		os, err := toStr(o)
		if err != nil {
			return 0, err
		}
		return strings.Compare(s, os), nil
	}
}

// logicInt converts an operand of a logical operator to an integer.
func logicInt(v Value) (int64, error) {
	if s, ok := v.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return -1, nil
		case "false":
			return 0, nil
		}
	}
	return toInt(v)
}

// logicOp implements And, Or, Xor, Eqv and Imp: logical on Booleans, bitwise
// on numbers, with VBScript's Null propagation.
func logicOp(op opKind, a, b Value) (Value, error) {
	var err error
	if a, err = scalar(a); err != nil {
		return nil, err
	}
	if b, err = scalar(b); err != nil {
		return nil, err
	}
	aNull, bNull := isNull(a), isNull(b)
	if aNull || bNull {
		if aNull && bNull {
			return Null, nil
		}
		other := a
		if aNull {
			other = b
		}
		n, err := logicInt(other)
		if err != nil {
			return nil, err
		}
		switch op {
		case opAnd:
			if n == 0 {
				return other, nil
			}
		case opOr:
			if n != 0 {
				return other, nil
			}
		case opImp:
			// Null Imp True is True; False Imp Null is True.
			if aNull && n == -1 {
				return other, nil
			}
			if bNull && n == 0 {
				return true, nil
			}
		}
		return Null, nil
	}
	ab, aBool := a.(bool)
	bb, bBool := b.(bool)
	if aBool && bBool {
		switch op {
		case opAnd:
			return ab && bb, nil
		case opOr:
			return ab || bb, nil
		case opXor:
			return ab != bb, nil
		case opEqv:
			return ab == bb, nil
		}
		return !ab || bb, nil
	}
	x, err := logicInt(a)
	if err != nil {
		return nil, err
	}
	y, err := logicInt(b)
	if err != nil {
		return nil, err
	}
	switch op {
	case opAnd:
		return x & y, nil
	case opOr:
		return x | y, nil
	case opXor:
		return x ^ y, nil
	case opEqv:
		return ^(x ^ y), nil
	}
	return ^x | y, nil
}
