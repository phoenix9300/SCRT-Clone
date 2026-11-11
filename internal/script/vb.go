package script

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/phoenix9300/SCRT-Clone/internal/vbs"
)

type vbsObj struct{ o *object }

func fromVBS(args []vbs.Value) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = fromVBSValue(a)
	}
	return out
}

func fromVBSValue(v vbs.Value) any {
	switch x := v.(type) {
	case nil:
		return nil
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
	case float32:
		return float64(x)
	case *vbs.Array:
		items := x.Items()
		out := make([]any, len(items))
		for i, it := range items {
			out[i] = fromVBSValue(it)
		}
		return out
	case *vbsObj:
		return x.o
	}
	if v == vbs.Null {
		return nil
	}
	return v
}

func toVBS(v any) vbs.Value {
	switch x := v.(type) {
	case *object:
		return &vbsObj{x}
	case []any:
		items := make([]vbs.Value, len(x))
		for i, e := range x {
			items[i] = toVBS(e)
		}
		return vbs.NewArray(items...)
	}
	return v
}

func vbsErr(err error) error {
	if errors.Is(err, errQuit) {
		return vbs.ErrQuit
	}
	return err
}

func (v *vbsObj) TypeName() string { return v.o.typ }

func (v *vbsObj) Get(name string, args []vbs.Value) (vbs.Value, error) {
	a := fromVBS(args)
	o := v.o
	switch {
	case name == "":
		if o.call != nil && len(a) > 0 {
			r, err := o.invoke("", a)
			return toVBS(r), vbsErr(err)
		}
		return v, nil
	case o.hasMethod(name):
		r, err := o.invoke(name, a)
		return toVBS(r), vbsErr(err)
	case o.hasProp(name):
		r, err := o.getProp(name)
		if po, ok := r.(*object); ok && err == nil && len(a) > 0 {
			r, err = po.invoke("", a)
		}
		return toVBS(r), vbsErr(err)
	}
	return nil, vbs.NewError(438, fmt.Sprintf("Object doesn't support this property or method: '%s.%s'", o.typ, name))
}

func (v *vbsObj) Set(name string, args []vbs.Value, val vbs.Value) error {
	if !v.o.hasProp(name) {
		return vbs.NewError(438, fmt.Sprintf("Object doesn't support this property or method: '%s.%s'", v.o.typ, name))
	}
	return vbsErr(v.o.setProp(name, fromVBSValue(val)))
}

func runVBS(e *env, src string) error {
	in := vbs.New()
	defer in.Close()
	in.SetOutput(e.run)
	in.SetArguments(e.args...)
	in.SetGlobal("crt", &vbsObj{newCRT(e)})
	for name, v := range map[string]int64{"IDOK": 1, "IDCANCEL": 2, "IDABORT": 3, "IDRETRY": 4, "IDIGNORE": 5, "IDYES": 6, "IDNO": 7} {
		in.SetGlobal(name, v)
	}
	dlg := e.cached("dialog", func() *object { return newDialog(e) })
	in.SetFunc("MsgBox", func(a []vbs.Value) (vbs.Value, error) {
		args := fromVBS(a)
		r, err := dlg.invoke("MessageBox", []any{argStr(args, 0, ""), argStr(args, 2, "Hawser"), int64(argInt(args, 1, 0))})
		return r, vbsErr(err)
	})
	in.SetFunc("InputBox", func(a []vbs.Value) (vbs.Value, error) {
		args := fromVBS(a)
		r, err := dlg.invoke("Prompt", []any{argStr(args, 0, ""), argStr(args, 1, "Hawser"), argStr(args, 2, "")})
		return r, vbsErr(err)
	})
	name := e.path
	if name == "" {
		name = "script.vbs"
	} else {
		name = filepath.Base(name)
	}
	err := in.Run(e.ctx, src, name)
	if err == nil && in.HasProc("main") {
		_, err = in.Call(e.ctx, "Main")
	}
	if errors.Is(err, vbs.ErrQuit) {
		return errQuit
	}
	return err
}
