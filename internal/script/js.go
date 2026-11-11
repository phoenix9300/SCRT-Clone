package script

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/dop251/goja"
)

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

type jsWrapper struct {
	vm    *goja.Runtime
	cache map[*object]*goja.Object
}

func (w *jsWrapper) toJS(v any) goja.Value {
	switch x := v.(type) {
	case nil:
		return goja.Null()
	case *object:
		return w.wrap(x)
	case []any:
		items := make([]any, len(x))
		for i, e := range x {
			items[i] = w.toJS(e)
		}
		return w.vm.NewArray(items...)
	}
	return w.vm.ToValue(v)
}

func (w *jsWrapper) fail(o *object, err error) {
	if errors.Is(err, errQuit) {
		w.vm.Interrupt(errQuit)
	}
	panic(w.vm.NewGoError(err))
}

func (w *jsWrapper) wrap(o *object) *goja.Object {
	if jo, ok := w.cache[o]; ok {
		return jo
	}
	vm := w.vm
	jo := vm.NewObject()
	w.cache[o] = jo
	names := func(n string) []string {
		if l := lowerFirst(n); l != n {
			return []string{n, l}
		}
		return []string{n}
	}
	for _, n := range o.propNames {
		n := n
		p := o.props[strings.ToLower(n)]
		getter := vm.ToValue(func(call goja.FunctionCall) goja.Value {
			v, err := o.getProp(n)
			if err != nil {
				w.fail(o, err)
			}
			return w.toJS(v)
		})
		var setter goja.Value = goja.Undefined()
		if p.set != nil {
			setter = vm.ToValue(func(call goja.FunctionCall) goja.Value {
				var v any
				if len(call.Arguments) > 0 {
					v = call.Arguments[0].Export()
				}
				if err := o.setProp(n, v); err != nil {
					w.fail(o, err)
				}
				return goja.Undefined()
			})
		}
		for _, name := range names(n) {
			_ = jo.DefineAccessorProperty(name, getter, setter, goja.FLAG_FALSE, goja.FLAG_TRUE)
		}
	}
	for _, n := range o.methodNames {
		n := n
		fn := vm.ToValue(func(call goja.FunctionCall) goja.Value {
			args := make([]any, len(call.Arguments))
			for i, a := range call.Arguments {
				args[i] = a.Export()
			}
			v, err := o.invoke(n, args)
			if err != nil {
				w.fail(o, err)
			}
			return w.toJS(v)
		})
		for _, name := range names(n) {
			_ = jo.Set(name, fn)
		}
	}
	if o.indexed != nil {
		items := o.indexed()
		for i, s := range items {
			_ = jo.Set(strconv.Itoa(i), s)
		}
		_ = jo.Set("length", len(items))
	}
	return jo
}

func runJS(e *env, src string) error {
	vm := goja.New()
	w := &jsWrapper{vm: vm, cache: map[*object]*goja.Object{}}
	_ = vm.Set("crt", w.wrap(newCRT(e)))
	print := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = a.String()
		}
		fmt.Fprintln(e.run, strings.Join(parts, " "))
		return goja.Undefined()
	}
	_ = vm.Set("print", print)
	con := vm.NewObject()
	for _, n := range []string{"log", "info", "warn", "error", "debug"} {
		_ = con.Set(n, print)
	}
	_ = vm.Set("console", con)

	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-e.ctx.Done():
			vm.Interrupt("stopped")
		case <-finished:
		}
	}()
	name := e.path
	if name == "" {
		name = "script.js"
	}
	_, err := vm.RunScript(name, src)
	if err == nil {
		return nil
	}
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		if v, ok := ie.Value().(error); ok && errors.Is(v, errQuit) {
			return errQuit
		}
		return e.ctx.Err()
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		if errors.Is(ex, errQuit) {
			return errQuit
		}
	}
	return err
}
