package script

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
)

// errQuit is returned by crt.Quit; front ends treat it as a normal end.
var errQuit = errors.New("script quit")

type prop struct {
	get func() (any, error)
	set func(any) error
}

type method func(args []any) (any, error)

// object is one scriptable object of the crt model. Member names are matched
// case-insensitively; values crossing the boundary are nil, bool, int64,
// float64, string, []any and *object.
type object struct {
	typ         string
	env         *env
	props       map[string]prop
	methods     map[string]method
	propNames   []string // display spellings
	methodNames []string
	call        method // default member (Arguments(i))
	indexed     func() []any
}

func newObject(e *env, typ string) *object {
	return &object{typ: typ, env: e, props: map[string]prop{}, methods: map[string]method{}}
}

func (o *object) prop(name string, get func() (any, error), set func(any) error) {
	o.props[strings.ToLower(name)] = prop{get, set}
	o.propNames = append(o.propNames, name)
}

// val registers a read/write property backed by a plain variable.
func (o *object) val(name string, p *any) {
	o.prop(name, func() (any, error) { return *p, nil }, func(v any) error { *p = v; return nil })
}

func (o *object) method(name string, fn method) {
	o.methods[strings.ToLower(name)] = fn
	o.methodNames = append(o.methodNames, name)
}

func (o *object) hasMethod(name string) bool {
	_, ok := o.methods[strings.ToLower(name)]
	return ok
}

func (o *object) hasProp(name string) bool {
	_, ok := o.props[strings.ToLower(name)]
	return ok
}

func (o *object) record(err error) error {
	if err != nil && o.env != nil && !errors.Is(err, errQuit) && !errors.Is(err, context.Canceled) {
		o.env.setErr(err)
	}
	return err
}

func (o *object) getProp(name string) (any, error) {
	p, ok := o.props[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("%s has no member %q", o.typ, name)
	}
	v, err := p.get()
	return v, o.record(err)
}

func (o *object) setProp(name string, v any) error {
	p, ok := o.props[strings.ToLower(name)]
	if !ok || p.set == nil {
		return o.record(fmt.Errorf("%s.%s cannot be set", o.typ, name))
	}
	return o.record(p.set(v))
}

func (o *object) invoke(name string, args []any) (any, error) {
	if name == "" {
		if o.call == nil {
			return o, nil
		}
		v, err := o.call(args)
		return v, o.record(err)
	}
	m, ok := o.methods[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("%s has no method %q", o.typ, name)
	}
	v, err := m(args)
	return v, o.record(err)
}

// ----------------------------------------------------------- conversions

func isNumber(v any) bool {
	switch v.(type) {
	case int, int32, int64, float32, float64:
		return true
	}
	return false
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	case float64:
		return x
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

func toInt(v any) int { return int(toFloat(v)) }

func toBool(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		l := strings.ToLower(strings.TrimSpace(x))
		return l == "true" || l == "1" || l == "yes"
	}
	return toFloat(v) != 0
}

func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case int32:
		return strconv.Itoa(int(x))
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

func argStr(a []any, i int, def string) string {
	if i < len(a) && a[i] != nil {
		return toStr(a[i])
	}
	return def
}

func argInt(a []any, i, def int) int {
	if i < len(a) && a[i] != nil {
		return toInt(a[i])
	}
	return def
}

func argFloat(a []any, i int, def float64) float64 {
	if i < len(a) && a[i] != nil {
		return toFloat(a[i])
	}
	return def
}

func argBool(a []any, i int, def bool) bool {
	if i < len(a) && a[i] != nil {
		return toBool(a[i])
	}
	return def
}

// flattenStrings expands arrays and converts every element to a string.
func flattenStrings(args []any) []string {
	var out []string
	for _, a := range args {
		if l, ok := a.([]any); ok {
			out = append(out, flattenStrings(l)...)
		} else if a != nil {
			out = append(out, toStr(a))
		}
	}
	return out
}

// ------------------------------------------------------------------- env

// env is the state shared by the objects of one script run.
type env struct {
	h    *Host
	ctx  context.Context
	run  *Run
	cn   *conn.Conn
	path string
	args []string

	mu      sync.Mutex
	lastErr int
	lastMsg string
	cache   map[string]*object

	logName   string
	cmdText   any
	cmdAll    any
	clipFmt   any
	cmdVisble any
}

func (e *env) setErr(err error) {
	e.mu.Lock()
	e.lastErr, e.lastMsg = 1, err.Error()
	e.mu.Unlock()
}

func (e *env) cached(key string, mk func() *object) *object {
	e.mu.Lock()
	defer e.mu.Unlock()
	if o, ok := e.cache[key]; ok {
		return o
	}
	if e.cache == nil {
		e.cache = map[string]*object{}
	}
	o := mk()
	e.cache[key] = o
	return o
}

func (e *env) ui() UI { return e.h.UI }

// sleep waits for d or until the run is stopped.
func (e *env) sleep(ms float64) error {
	return sleepCtx(e.ctx, ms)
}
