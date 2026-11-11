package vbs

import (
	"fmt"
	"strings"
	"time"
)

// builtin describes a built-in function or sub.
type builtin struct {
	name string
	min  int
	max  int // -1: variadic
	fn   func(in *Interpreter, args []Value) (Value, error)
}

// builtins maps lower-cased names to built-in functions. It is filled by
// the init functions of the builtins_*.go files.
var builtins = map[string]*builtin{}

func reg(name string, min, max int, fn func(in *Interpreter, args []Value) (Value, error)) {
	builtins[strings.ToLower(name)] = &builtin{name: name, min: min, max: max, fn: fn}
}

// callBuiltin checks the argument count and calls a built-in function.
func (in *Interpreter) callBuiltin(b *builtin, args []Value) (Value, error) {
	if len(args) < b.min || (b.max >= 0 && len(args) > b.max) {
		return nil, newErr(450, b.name)
	}
	v, err := b.fn(in, args)
	if err != nil {
		return nil, withDetail(err, b.name)
	}
	return v, nil
}

// optArg returns the i-th argument when it was supplied (not omitted and not
// Empty).
func optArg(args []Value, i int) (Value, bool) {
	if i < len(args) && args[i] != nil {
		return args[i], true
	}
	return nil, false
}

// optInt returns the i-th argument as an integer, or def when omitted.
func optInt(args []Value, i int, def int64) (int64, error) {
	v, ok := optArg(args, i)
	if !ok {
		return def, nil
	}
	return toInt(v)
}

// optStr returns the i-th argument as a string, or def when omitted.
func optStr(args []Value, i int, def string) (string, error) {
	v, ok := optArg(args, i)
	if !ok {
		return def, nil
	}
	return toStr(v)
}

// builtinConsts holds the intrinsic constants (lower-cased names).
var builtinConsts = map[string]Value{
	// String constants.
	"vbcr":          "\r",
	"vblf":          "\n",
	"vbcrlf":        "\r\n",
	"vbnewline":     "\r\n",
	"vbtab":         "\t",
	"vbnullstring":  "",
	"vbnullchar":    "\x00",
	"vbback":        "\b",
	"vbformfeed":    "\f",
	"vbverticaltab": "\v",

	// Tristate / boolean.
	"vbtrue":       int64(-1),
	"vbfalse":      int64(0),
	"vbusedefault": int64(-2),

	// MsgBox buttons and results.
	"vbokonly":           int64(0),
	"vbokcancel":         int64(1),
	"vbabortretryignore": int64(2),
	"vbyesnocancel":      int64(3),
	"vbyesno":            int64(4),
	"vbretrycancel":      int64(5),
	"vbcritical":         int64(16),
	"vbquestion":         int64(32),
	"vbexclamation":      int64(48),
	"vbinformation":      int64(64),
	"vbdefaultbutton1":   int64(0),
	"vbdefaultbutton2":   int64(256),
	"vbdefaultbutton3":   int64(512),
	"vbdefaultbutton4":   int64(768),
	"vbapplicationmodal": int64(0),
	"vbsystemmodal":      int64(4096),
	"vbok":               int64(1),
	"vbcancel":           int64(2),
	"vbabort":            int64(3),
	"vbretry":            int64(4),
	"vbignore":           int64(5),
	"vbyes":              int64(6),
	"vbno":               int64(7),

	// Comparison modes.
	"vbbinarycompare":   int64(0),
	"vbtextcompare":     int64(1),
	"vbdatabasecompare": int64(2),

	// VarType results.
	"vbempty":      int64(0),
	"vbnull":       int64(1),
	"vbinteger":    int64(2),
	"vblong":       int64(3),
	"vbsingle":     int64(4),
	"vbdouble":     int64(5),
	"vbcurrency":   int64(6),
	"vbdate":       int64(7),
	"vbstring":     int64(8),
	"vbobject":     int64(9),
	"vberror":      int64(10),
	"vbboolean":    int64(11),
	"vbvariant":    int64(12),
	"vbdataobject": int64(13),
	"vbdecimal":    int64(14),
	"vbbyte":       int64(17),
	"vbarray":      int64(8192),

	// Date constants.
	"vbsunday":             int64(1),
	"vbmonday":             int64(2),
	"vbtuesday":            int64(3),
	"vbwednesday":          int64(4),
	"vbthursday":           int64(5),
	"vbfriday":             int64(6),
	"vbsaturday":           int64(7),
	"vbusesystem":          int64(0),
	"vbusesystemdayofweek": int64(0),
	"vbfirstjan1":          int64(1),
	"vbfirstfourdays":      int64(2),
	"vbfirstfullweek":      int64(3),
	"vbgeneraldate":        int64(0),
	"vblongdate":           int64(1),
	"vbshortdate":          int64(2),
	"vblongtime":           int64(3),
	"vbshorttime":          int64(4),

	// Colours.
	"vbblack":   int64(0x000000),
	"vbred":     int64(0x0000FF),
	"vbgreen":   int64(0x00FF00),
	"vbyellow":  int64(0x00FFFF),
	"vbblue":    int64(0xFF0000),
	"vbmagenta": int64(0xFF00FF),
	"vbcyan":    int64(0xFFFF00),
	"vbwhite":   int64(0xFFFFFF),

	// Miscellaneous.
	"vbobjecterror": int64(-2147221504),
}

func init() {
	// ---- arrays ----
	reg("Array", 0, -1, func(in *Interpreter, args []Value) (Value, error) {
		data := make([]Value, len(args))
		for i, a := range args {
			data[i] = storeValue(a)
		}
		return newTempArray(data), nil
	})
	reg("UBound", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		arr, ok := args[0].(*Array)
		if !ok {
			return nil, newErr(13, "")
		}
		dim, err := optInt(args, 1, 1)
		if err != nil {
			return nil, err
		}
		if dim < 1 || int(dim) > len(arr.dims) {
			return nil, newErr(9, "")
		}
		return int64(arr.dims[dim-1] - 1), nil
	})
	reg("LBound", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		arr, ok := args[0].(*Array)
		if !ok {
			return nil, newErr(13, "")
		}
		dim, err := optInt(args, 1, 1)
		if err != nil {
			return nil, err
		}
		if dim < 1 || int(dim) > len(arr.dims) {
			return nil, newErr(9, "")
		}
		return int64(0), nil
	})
	reg("IsArray", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		_, ok := args[0].(*Array)
		return ok, nil
	})

	// ---- type tests ----
	reg("IsEmpty", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return args[0] == nil, nil
	})
	reg("IsNull", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return isNull(args[0]), nil
	})
	reg("IsObject", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return isObjectRef(args[0]), nil
	})
	reg("IsNumeric", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		v := args[0]
		if _, ok := asObject(v); ok {
			dv, err := defaultValue(v)
			if err != nil {
				return false, nil
			}
			v = dv
		}
		switch x := v.(type) {
		case nil, bool, int64, float64:
			return true, nil
		case string:
			_, ok := parseNumber(x)
			return ok, nil
		}
		return false, nil
	})
	reg("IsDate", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		switch x := args[0].(type) {
		case time.Time:
			return true, nil
		case string:
			_, ok := parseDate(x)
			return ok, nil
		}
		return false, nil
	})
	reg("VarType", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return varType(args[0]), nil
	})
	reg("TypeName", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return typeName(args[0]), nil
	})

	// ---- interaction ----
	reg("MsgBox", 1, 5, func(in *Interpreter, args []Value) (Value, error) {
		s, err := concatStr(args[0])
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(in.out, s)
		return int64(1), nil
	})
	reg("InputBox", 1, 7, func(in *Interpreter, args []Value) (Value, error) {
		return "", nil
	})

	// ---- objects ----
	reg("CreateObject", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		return in.createObject(args)
	})
	reg("GetObject", 0, 2, func(in *Interpreter, args []Value) (Value, error) {
		if h := in.hostFuncs["getobject"]; h != nil {
			return in.callHost(h, args)
		}
		return nil, newErr(432, "")
	})
	reg("GetRef", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		name, err := toStr(args[0])
		if err != nil {
			return nil, err
		}
		p := in.procs[strings.ToLower(name)]
		if p == nil {
			return nil, newErr(5, "")
		}
		return &procRef{in: in, proc: p}, nil
	})

	// ---- dynamic code ----
	reg("Eval", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		src, err := toStr(args[0])
		if err != nil {
			return nil, err
		}
		f := in.curFrame()
		e, perr := parseExpression(src, f.file)
		if perr != nil {
			return nil, evalSyntaxErr(perr)
		}
		return in.evalExpr(f, e)
	})
	reg("Execute", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return nil, in.execute(args[0], in.curFrame())
	})
	reg("ExecuteGlobal", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		return nil, in.execute(args[0], in.gframe)
	})

	// ---- miscellaneous ----
	reg("RGB", 3, 3, func(in *Interpreter, args []Value) (Value, error) {
		var c [3]int64
		for i := range c {
			n, err := toInt(args[i])
			if err != nil {
				return nil, err
			}
			if n < 0 {
				return nil, newErr(5, "")
			}
			if n > 255 {
				n = 255
			}
			c[i] = n
		}
		return c[0] + c[1]*256 + c[2]*65536, nil
	})
	reg("ScriptEngine", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		return "VBScript", nil
	})
	reg("ScriptEngineMajorVersion", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		return int64(5), nil
	})
	reg("ScriptEngineMinorVersion", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		return int64(8), nil
	})
	reg("ScriptEngineBuildVersion", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		return int64(16384), nil
	})
	reg("GetLocale", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		return int64(1033), nil
	})
	reg("SetLocale", 0, 1, func(in *Interpreter, args []Value) (Value, error) {
		return int64(1033), nil
	})
	reg("Stop", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		return nil, nil
	})
}

// evalSyntaxErr turns a compilation error of dynamically compiled code into
// a trappable runtime error positioned at the calling statement.
func evalSyntaxErr(e *Error) *Error {
	return &Error{Number: e.Number, Message: e.Message, Syntax: true, Source: "Microsoft VBScript compilation error"}
}

// execute implements Execute and ExecuteGlobal: the code runs in frame f.
func (in *Interpreter) execute(code Value, f *frame) error {
	src, err := toStr(code)
	if err != nil {
		return err
	}
	stmts, perr := parseProgram(src, f.file)
	if perr != nil {
		return evalSyntaxErr(perr)
	}
	if err := in.hoist(f, stmts); err != nil {
		return err
	}
	_, err = in.execBlock(f, stmts)
	return err
}

// createObject implements CreateObject.
func (in *Interpreter) createObject(args []Value) (Value, error) {
	progID, err := toStr(args[0])
	if err != nil {
		return nil, err
	}
	if o, found, err := in.createByName(strings.ToLower(strings.TrimSpace(progID))); found {
		return o, err
	}
	if h := in.hostFuncs["createobject"]; h != nil {
		return in.callHost(h, args)
	}
	return nil, newErr(429, progID)
}

// classAliases maps the names usable with `New` to ProgIDs.
var classAliases = map[string]string{
	"regexp": "vbscript.regexp",
}

// createByName instantiates a registered class. found is false when no
// class is registered under the (lower-cased) name.
func (in *Interpreter) createByName(ln string) (o Value, found bool, err error) {
	fac := in.factories[ln]
	if fac == nil {
		if alias, ok := classAliases[ln]; ok {
			fac = in.factories[alias]
		}
	}
	if fac == nil {
		return nil, false, nil
	}
	obj, ferr := fac()
	if ferr != nil {
		return nil, true, in.hostErr(ferr)
	}
	if obj == nil {
		return nil, true, newErr(429, ln)
	}
	return obj, true, nil
}
