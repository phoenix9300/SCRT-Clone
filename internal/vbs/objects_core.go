package vbs

import "strings"

// errObject is the global Err object.
type errObject struct {
	number   int64
	desc     string
	source   string
	helpFile string
	helpCtx  int64
}

func (e *errObject) clear() { *e = errObject{} }

// capture records a trapped runtime error.
func (e *errObject) capture(err *Error) {
	e.number = int64(err.Number)
	e.desc = err.Message
	e.source = err.Source
	e.helpFile, e.helpCtx = "", 0
	if e.source == "" {
		if err.Syntax {
			e.source = "Microsoft VBScript compilation error"
		} else {
			e.source = "Microsoft VBScript runtime error"
		}
	}
}

func (e *errObject) TypeName() string { return "ErrObject" }

func (e *errObject) Get(name string, args []Value) (Value, error) {
	switch name {
	case "", "number":
		return e.number, nil
	case "description":
		return e.desc, nil
	case "source":
		return e.source, nil
	case "helpfile":
		return e.helpFile, nil
	case "helpcontext":
		return e.helpCtx, nil
	case "clear":
		e.clear()
		return nil, nil
	case "raise":
		if len(args) < 1 || len(args) > 5 {
			return nil, newErr(450, "")
		}
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, newErr(5, "")
		}
		src, err := optStr(args, 1, "")
		if err != nil {
			return nil, err
		}
		desc, err := optStr(args, 2, "")
		if err != nil {
			return nil, err
		}
		if desc == "" {
			desc = errText(int(n))
		}
		e.number, e.source, e.desc = n, src, desc
		if e.helpFile, err = optStr(args, 3, ""); err != nil {
			return nil, err
		}
		if e.helpCtx, err = optInt(args, 4, 0); err != nil {
			return nil, err
		}
		return nil, &Error{Number: int(n), Message: desc, Source: src}
	}
	return nil, newErr(438, "")
}

func (e *errObject) Set(name string, args []Value, v Value) error {
	var err error
	switch name {
	case "", "number":
		e.number, err = toInt(v)
	case "description":
		e.desc, err = toStr(v)
	case "source":
		e.source, err = toStr(v)
	case "helpfile":
		e.helpFile, err = toStr(v)
	case "helpcontext":
		e.helpCtx, err = toInt(v)
	default:
		return newErr(438, "")
	}
	return err
}

// collection is a read-only, enumerable list used for Matches, SubMatches,
// Files, SubFolders, WScript.Arguments and similar objects.
type collection struct {
	typeName string
	items    []Value
	// byKey optionally resolves Item(key) for string keys.
	byKey func(key string) (Value, bool)
}

func (c *collection) TypeName() string   { return c.typeName }
func (c *collection) Enumerate() []Value { return c.items }
func (c *collection) gcRefs() []Value    { return c.items }

func (c *collection) Get(name string, args []Value) (Value, error) {
	switch name {
	case "count", "length":
		return int64(len(c.items)), nil
	case "", "item":
		if len(args) != 1 {
			return nil, newErr(450, "")
		}
		if key, ok := args[0].(string); ok && c.byKey != nil {
			if v, found := c.byKey(key); found {
				return v, nil
			}
			return nil, newErr(5, "")
		}
		i, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if i < 0 || i >= int64(len(c.items)) {
			return nil, newErr(9, "")
		}
		return c.items[i], nil
	}
	return nil, newErr(438, "")
}

func (c *collection) Set(string, []Value, Value) error { return newErr(438, "") }

// needArgs checks the argument count of a built-in object method.
func needArgs(args []Value, min, max int) error {
	if len(args) < min || len(args) > max {
		return newErr(450, "")
	}
	return nil
}

// argStrAt returns argument i converted to a string.
func argStrAt(args []Value, i int) (string, error) {
	if i >= len(args) {
		return "", newErr(450, "")
	}
	return toStr(args[i])
}

// optBool returns the i-th argument as a boolean, or def when omitted.
func optBool(args []Value, i int, def bool) (bool, error) {
	v, ok := optArg(args, i)
	if !ok {
		return def, nil
	}
	return toBool(v)
}

func lowerEq(a, b string) bool { return strings.EqualFold(a, b) }
