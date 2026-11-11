package vbs

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Value is the dynamic VBScript value. The allowed dynamic types are:
//
//	nil            Empty
//	vbs.Null       Null
//	vbs.Nothing    Nothing (an object reference to nothing)
//	bool           Boolean
//	int64          Integer / Long
//	float64        Double / Single / Currency
//	string         String
//	time.Time      Date (see the package documentation for time zone notes)
//	*vbs.Array     array
//	vbs.Object     any object (host objects, class instances, built-ins)
//
// Values handed to the interpreter by the host (SetGlobal, results of
// Object.Get, results of functions registered with SetFunc) are normalised:
// the other Go integer and float types become int64 / float64, []byte
// becomes string and []string / []any become arrays.
type Value = any

// Object is implemented by host objects and by the built-in objects.
//
// Member names are passed lower-cased because VBScript is case-insensitive.
type Object interface {
	// Get is used for `obj.name` and `obj.name(args)` in expressions and for
	// the call statements `obj.name a, b` and `Call obj.name(a, b)`. An empty
	// name designates the default member: `obj(args)`, or the object used
	// where a plain value is required.
	Get(name string, args []Value) (Value, error)
	// Set is used for `obj.name = v`, `obj.name(args) = v` and
	// `Set obj.name = v`.
	Set(name string, args []Value, v Value) error
}

// Enumerable may be implemented by an Object to support `For Each`.
type Enumerable interface {
	// Enumerate returns the items visited by For Each, in order.
	Enumerate() []Value
}

// TypeNamer may be implemented by an Object to control what TypeName()
// reports for it. Objects without it report "Object".
type TypeNamer interface {
	TypeName() string
}

type nullType struct{}

// Null is the VBScript Null value.
var Null Value = nullType{}

type nothingType struct{}

func (nothingType) Get(string, []Value) (Value, error) { return nil, newErr(91, "") }
func (nothingType) Set(string, []Value, Value) error   { return newErr(91, "") }
func (nothingType) TypeName() string                   { return "Nothing" }

// Nothing is the VBScript Nothing object reference. It implements Object;
// every member access on it fails with error 91 (Object variable not set).
var Nothing Object = nothingType{}

func isNull(v Value) bool    { _, ok := v.(nullType); return ok }
func isNothing(v Value) bool { _, ok := v.(nothingType); return ok }

// asObject reports whether v is a usable object reference (not Nothing).
func asObject(v Value) (Object, bool) {
	o, ok := v.(Object)
	if !ok || isNothing(v) {
		return nil, false
	}
	return o, true
}

// isObjectRef reports whether v is an object reference, including Nothing.
func isObjectRef(v Value) bool {
	_, ok := v.(Object)
	return ok
}

// maxArrayElems bounds the number of elements of a single array so that a
// script cannot take the host down with one ReDim.
const maxArrayElems = 1 << 25

// Array is a VBScript array. Arrays are zero-based and may have several
// dimensions; elements are stored in column-major order (the first index
// varies fastest), which is what makes ReDim Preserve of the last dimension
// cheap.
//
// VBScript arrays have value semantics: the interpreter copies an array when
// it is assigned to another variable. Arrays created by the host with
// NewArray are copied when a script stores them, so later changes made by the
// script are not visible to the host and vice versa.
type Array struct {
	dims  []int   // element count per dimension (upper bound + 1)
	data  []Value // column-major storage
	fixed bool    // declared with Dim a(n)
	temp  bool    // freshly produced, may be adopted without copying
}

// NewArray creates a one-dimensional array holding the given items.
func NewArray(items ...Value) *Array {
	data := make([]Value, len(items))
	for i, it := range items {
		data[i] = normalize(it)
	}
	return &Array{dims: []int{len(items)}, data: data}
}

// newTempArray wraps data (without copying) in an array that the first
// variable it is assigned to may adopt.
func newTempArray(data []Value) *Array {
	if data == nil {
		data = []Value{}
	}
	return &Array{dims: []int{len(data)}, data: data, temp: true}
}

func newArrayDims(dims []int) (*Array, error) {
	n := 1
	for _, d := range dims {
		if d < 0 {
			return nil, newErr(9, "")
		}
		if d != 0 && n > maxArrayElems/d {
			return nil, newErr(7, "")
		}
		n *= d
	}
	return &Array{dims: append([]int(nil), dims...), data: make([]Value, n)}, nil
}

// Items returns the elements of the array. For a one-dimensional array this
// is the natural order; for multi-dimensional arrays it is the column-major
// storage order. The slice aliases the array's storage.
func (a *Array) Items() []Value {
	if a == nil {
		return nil
	}
	return a.data
}

// Len returns the total number of elements.
func (a *Array) Len() int {
	if a == nil {
		return 0
	}
	return len(a.data)
}

// Dims returns the number of dimensions (0 for an unallocated dynamic array).
func (a *Array) Dims() int {
	if a == nil {
		return 0
	}
	return len(a.dims)
}

// UBound returns the upper bound of the 1-based dimension dim, or -1 when
// the dimension does not exist or is empty.
func (a *Array) UBound(dim int) int {
	if a == nil || dim < 1 || dim > len(a.dims) {
		return -1
	}
	return a.dims[dim-1] - 1
}

// Index returns the element at the given indexes (one per dimension).
func (a *Array) Index(idx ...int) (Value, bool) {
	off, ok := a.offset(idx)
	if !ok {
		return nil, false
	}
	return a.data[off], true
}

// SetIndex stores v at the given indexes and reports whether they were valid.
func (a *Array) SetIndex(v Value, idx ...int) bool {
	off, ok := a.offset(idx)
	if !ok {
		return false
	}
	a.data[off] = storeValue(normalize(v))
	return true
}

func (a *Array) offset(idx []int) (int, bool) {
	if a == nil || len(idx) != len(a.dims) || len(idx) == 0 {
		return 0, false
	}
	off, mul := 0, 1
	for i, ix := range idx {
		if ix < 0 || ix >= a.dims[i] {
			return 0, false
		}
		off += ix * mul
		mul *= a.dims[i]
	}
	return off, true
}

// clone makes a deep copy (nested arrays are copied too, objects are shared).
func (a *Array) clone() *Array {
	n := &Array{dims: append([]int(nil), a.dims...), data: make([]Value, len(a.data))}
	for i, v := range a.data {
		if sub, ok := v.(*Array); ok {
			v = sub.clone()
		}
		n.data[i] = v
	}
	return n
}

// resizeLast returns a copy whose last dimension has n elements, preserving
// the existing contents (ReDim Preserve).
func (a *Array) resizeLast(n int) (*Array, error) {
	dims := append([]int(nil), a.dims...)
	dims[len(dims)-1] = n
	na, err := newArrayDims(dims)
	if err != nil {
		return nil, err
	}
	copy(na.data, a.data)
	return na, nil
}

// storeValue prepares a value for being stored in a variable, array element,
// field or collection: arrays are copied unless they are fresh temporaries.
func storeValue(v Value) Value {
	if a, ok := v.(*Array); ok {
		if a.temp {
			a.temp = false
			return a
		}
		return a.clone()
	}
	return v
}

// normalize converts convenient Go types supplied by the host into the
// canonical dynamic types of Value.
func normalize(v Value) Value {
	switch x := v.(type) {
	case nil, bool, int64, float64, string, time.Time, *Array, nullType:
		return v
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint:
		if uint64(x) > math.MaxInt64 {
			return float64(x)
		}
		return int64(x)
	case uint64:
		if x > math.MaxInt64 {
			return float64(x)
		}
		return int64(x)
	case float32:
		return float64(x)
	case []byte:
		return string(x)
	case []string:
		data := make([]Value, len(x))
		for i, s := range x {
			data[i] = s
		}
		return &Array{dims: []int{len(data)}, data: data}
	case []Value:
		return NewArray(x...)
	case []int:
		data := make([]Value, len(x))
		for i, n := range x {
			data[i] = int64(n)
		}
		return &Array{dims: []int{len(data)}, data: data}
	case time.Duration:
		return float64(x) / float64(time.Second)
	}
	return v
}

// defaultValue resolves object references to the value of their default
// member, as VBScript does when an object is used where a plain value is
// required. Non-objects are returned unchanged.
func defaultValue(v Value) (Value, error) {
	for i := 0; i < 16; i++ {
		o, ok := v.(Object)
		if !ok {
			return v, nil
		}
		if isNothing(v) {
			return nil, newErr(91, "")
		}
		nv, err := o.Get("", nil)
		if err != nil {
			if e, ok := err.(*Error); ok && e.Number != 438 && e.Number != 450 {
				return nil, e
			}
			return nil, newErr(438, "")
		}
		v = normalize(nv)
	}
	return nil, newErr(438, "")
}

func boolNum(b bool) int64 {
	if b {
		return -1
	}
	return 0
}

// toFloat converts to Double with VBScript rules (CDbl).
func toFloat(v Value) (float64, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case bool:
		return float64(boolNum(x)), nil
	case string:
		f, ok := parseNumber(x)
		if !ok {
			return 0, newErr(13, "")
		}
		return f, nil
	case time.Time:
		return dateToFloat(x), nil
	case nullType:
		return 0, newErr(94, "")
	case *Array:
		return 0, newErr(13, "")
	case Object:
		dv, err := defaultValue(x)
		if err != nil {
			return 0, err
		}
		return toFloat(dv)
	}
	if n := normalize(v); n != v {
		return toFloat(n)
	}
	return 0, newErr(13, "")
}

// roundToInt rounds half to even (the rounding used by CInt, CLng, "\", Mod
// and array subscripts).
func roundToInt(f float64) (int64, error) {
	r := math.RoundToEven(f)
	if math.IsNaN(r) || r >= 9.2233720368547758e18 || r <= -9.2233720368547758e18 {
		return 0, newErr(6, "")
	}
	return int64(r), nil
}

// toInt converts to an integer with VBScript rules (CLng).
func toInt(v Value) (int64, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case int64:
		return x, nil
	case bool:
		return boolNum(x), nil
	case float64:
		return roundToInt(x)
	}
	f, err := toFloat(v)
	if err != nil {
		return 0, err
	}
	return roundToInt(f)
}

// toStr converts to String with VBScript rules (CStr).
func toStr(v Value) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return formatFloat(x), nil
	case time.Time:
		return formatDateGeneral(x), nil
	case nullType:
		return "", newErr(94, "")
	case *Array:
		return "", newErr(13, "")
	case Object:
		dv, err := defaultValue(x)
		if err != nil {
			return "", err
		}
		return toStr(dv)
	}
	if n := normalize(v); n != v {
		return toStr(n)
	}
	return "", newErr(13, "")
}

// toBool converts to Boolean with VBScript rules (CBool).
func toBool(v Value) (bool, error) {
	switch x := v.(type) {
	case nil:
		return false, nil
	case bool:
		return x, nil
	case int64:
		return x != 0, nil
	case float64:
		return x != 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		f, ok := parseNumber(x)
		if !ok {
			return false, newErr(13, "")
		}
		return f != 0, nil
	case time.Time:
		return dateToFloat(x) != 0, nil
	case nullType:
		return false, newErr(94, "")
	case *Array:
		return false, newErr(13, "")
	case Object:
		dv, err := defaultValue(x)
		if err != nil {
			return false, err
		}
		return toBool(dv)
	}
	if n := normalize(v); n != v {
		return toBool(n)
	}
	return false, newErr(13, "")
}

// condBool evaluates a value used as a condition; Null counts as False.
func condBool(v Value) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case nil, nullType:
		return false, nil
	}
	return toBool(v)
}

// toDate converts to Date with VBScript rules (CDate).
func toDate(v Value) (time.Time, error) {
	switch x := v.(type) {
	case time.Time:
		return x, nil
	case string:
		if t, ok := parseDate(x); ok {
			return t, nil
		}
		if f, ok := parseNumber(x); ok {
			return floatToDate(f)
		}
		return time.Time{}, newErr(13, "")
	case nullType:
		return time.Time{}, newErr(94, "")
	}
	f, err := toFloat(v)
	if err != nil {
		return time.Time{}, err
	}
	return floatToDate(f)
}

// ToString converts v to a string using CStr semantics. It never panics and
// never fails: Null yields "Null", arrays and objects without a default value
// yield "".
func ToString(v Value) (s string) {
	defer func() {
		if recover() != nil {
			s = ""
		}
	}()
	if isNull(v) {
		return "Null"
	}
	s, _ = toStr(v)
	return s
}

// ToBool converts v to a boolean using CBool semantics; values that cannot be
// converted yield false.
func ToBool(v Value) (b bool) {
	defer func() {
		if recover() != nil {
			b = false
		}
	}()
	b, _ = condBool(v)
	return b
}

// ToInt converts v to an integer using CLng semantics (round half to even);
// values that cannot be converted yield 0.
func ToInt(v Value) (n int64) {
	defer func() {
		if recover() != nil {
			n = 0
		}
	}()
	if isNull(v) {
		return 0
	}
	n, _ = toInt(v)
	return n
}

// ToFloat converts v to a float using CDbl semantics; values that cannot be
// converted yield 0.
func ToFloat(v Value) (f float64) {
	defer func() {
		if recover() != nil {
			f = 0
		}
	}()
	if isNull(v) {
		return 0
	}
	f, _ = toFloat(v)
	return f
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// parseNumber parses a numeric string the way VBScript's implicit conversion
// does: surrounding blanks, sign, thousands separators, decimals, exponent
// and the &H / &O prefixes are accepted.
func parseNumber(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, false
	}
	if t[0] == '&' {
		if len(t) < 3 {
			return 0, false
		}
		base := 0
		switch t[1] {
		case 'h', 'H':
			base = 16
		case 'o', 'O':
			base = 8
		default:
			return 0, false
		}
		digits := t[2:]
		long := strings.HasSuffix(digits, "&")
		digits = strings.TrimSuffix(digits, "&")
		u, err := strconv.ParseUint(digits, base, 64)
		if err != nil {
			return 0, false
		}
		return float64(radixLiteral(u, long)), true
	}
	i := 0
	neg := false
	if t[i] == '+' || t[i] == '-' {
		neg = t[i] == '-'
		i++
	}
	if i < len(t) && t[i] == '$' {
		i++
	}
	buf := make([]byte, 0, len(t))
	digits := 0
	for i < len(t) && (isDigit(t[i]) || t[i] == ',') {
		if t[i] != ',' {
			buf = append(buf, t[i])
			digits++
		}
		i++
	}
	if i < len(t) && t[i] == '.' {
		buf = append(buf, '.')
		i++
		for i < len(t) && isDigit(t[i]) {
			buf = append(buf, t[i])
			digits++
			i++
		}
	}
	if digits == 0 {
		return 0, false
	}
	if i < len(t) && (t[i] == 'e' || t[i] == 'E' || t[i] == 'd' || t[i] == 'D') {
		j := i + 1
		exp := []byte{'e'}
		if j < len(t) && (t[j] == '+' || t[j] == '-') {
			exp = append(exp, t[j])
			j++
		}
		k := j
		for j < len(t) && isDigit(t[j]) {
			exp = append(exp, t[j])
			j++
		}
		if j == k {
			return 0, false
		}
		buf = append(buf, exp...)
		i = j
	}
	if i != len(t) {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(buf), 64)
	if err != nil || math.IsInf(f, 0) {
		return 0, false
	}
	if neg {
		f = -f
	}
	return f, true
}

// radixLiteral applies the VBScript width rules to a hex/octal literal:
// values that fit 16 bits are Integers, values that fit 32 bits are Longs,
// and both wrap around to negative numbers (&HFFFF is -1, &HFFFF& is 65535).
func radixLiteral(u uint64, long bool) int64 {
	switch {
	case !long && u <= 0xFFFF:
		return int64(int16(u))
	case u <= 0xFFFFFFFF:
		return int64(int32(u))
	}
	return int64(u)
}

// formatFloat renders a Double like VBScript: up to 15 significant digits,
// scientific notation for very large and very small magnitudes.
func formatFloat(f float64) string {
	switch {
	case f == 0:
		return "0"
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "1.#INF"
	case math.IsInf(f, -1):
		return "-1.#INF"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	return strconv.FormatFloat(f, 'G', 15, 64)
}

// intValue narrows an integer result: values outside the 32-bit Long range
// become Doubles, as they do in VBScript.
func intValue(n int64) Value {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return float64(n)
	}
	return n
}

// typeName implements TypeName().
func typeName(v Value) string {
	switch x := v.(type) {
	case nil:
		return "Empty"
	case nullType:
		return "Null"
	case bool:
		return "Boolean"
	case int64:
		if x >= math.MinInt16 && x <= math.MaxInt16 {
			return "Integer"
		}
		return "Long"
	case float64:
		return "Double"
	case string:
		return "String"
	case time.Time:
		return "Date"
	case *Array:
		return "Variant()"
	case TypeNamer:
		return x.TypeName()
	case Object:
		return "Object"
	}
	if n := normalize(v); n != v {
		return typeName(n)
	}
	return "Unknown"
}

// varType implements VarType().
func varType(v Value) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case nullType:
		return 1
	case bool:
		return 11
	case int64:
		if x >= math.MinInt16 && x <= math.MaxInt16 {
			return 2
		}
		return 3
	case float64:
		return 5
	case string:
		return 8
	case time.Time:
		return 7
	case *Array:
		return 8204
	case Object:
		return 9
	}
	if n := normalize(v); n != v {
		return varType(n)
	}
	return 12
}
