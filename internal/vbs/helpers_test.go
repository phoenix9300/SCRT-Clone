package vbs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// tcase is a script whose WScript.Echo output is compared with want (lines
// joined by "\n", without the final newline).
type tcase struct {
	name string
	src  string
	want string
}

// runOut runs src in a fresh interpreter and returns its output.
func runOut(t *testing.T, src string) (string, error) {
	t.Helper()
	in := New()
	var buf bytes.Buffer
	in.SetOutput(&buf)
	err := in.Run(context.Background(), src, "test.vbs")
	return strings.TrimRight(buf.String(), "\n"), err
}

func runCases(t *testing.T, cases []tcase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := runOut(t, c.src)
			if err != nil {
				t.Fatalf("unexpected error: %v\noutput so far:\n%s", err, got)
			}
			if got != c.want {
				t.Errorf("output mismatch\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// ecase is an expression and the CStr of its expected value.
type ecase struct {
	expr string
	want string
}

func evalCases(t *testing.T, cases []ecase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			in := New()
			v, err := in.Eval(context.Background(), c.expr)
			if err != nil {
				t.Fatalf("Eval(%s): unexpected error: %v", c.expr, err)
			}
			if got := ToString(v); got != c.want {
				t.Errorf("Eval(%s) = %q (%T), want %q", c.expr, got, v, c.want)
			}
		})
	}
}

// errCase is a script expected to fail with the given error number (and
// line, when non-zero).
type errCase struct {
	name   string
	src    string
	number int
	line   int
	syntax bool
}

func runErrCases(t *testing.T, cases []errCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runOut(t, c.src)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("expected *Error, got %v (output %q)", err, out)
			}
			if e.Number != c.number {
				t.Errorf("error number = %d (%s), want %d", e.Number, e.Message, c.number)
			}
			if c.line != 0 && e.Line != c.line {
				t.Errorf("error line = %d (%v), want %d", e.Line, e, c.line)
			}
			if e.Syntax != c.syntax {
				t.Errorf("Syntax = %v, want %v (%v)", e.Syntax, c.syntax, e)
			}
			if e.File != "test.vbs" {
				t.Errorf("File = %q, want test.vbs", e.File)
			}
		})
	}
}

// ---- fake host object tree ----

// recorder logs every Get/Set that reaches a fake host object.
type recorder struct {
	calls []string
}

func (r *recorder) logf(format string, a ...any) {
	r.calls = append(r.calls, fmt.Sprintf(format, a...))
}

func (r *recorder) reset() { r.calls = nil }

func fmtVal(v Value) string {
	switch x := v.(type) {
	case nil:
		return "Empty"
	case string:
		return fmt.Sprintf("%q", x)
	case *fakeObj:
		return "<" + x.path + ">"
	case *fakeScreen:
		return "<" + x.path + ">"
	case nothingType:
		return "Nothing"
	case Object:
		return "<object>"
	case *Array:
		parts := make([]string, len(x.Items()))
		for i, e := range x.Items() {
			parts[i] = fmtVal(e)
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	return fmt.Sprintf("%s:%s", typeName(v), ToString(v))
}

func fmtVals(args []Value) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = fmtVal(a)
	}
	return strings.Join(parts, ", ")
}

// fakeObj is a generic recording host object with properties and methods.
type fakeObj struct {
	rec   *recorder
	path  string
	props map[string]Value
	meths map[string]func(args []Value) (Value, error)
}

func newFake(rec *recorder, path string) *fakeObj {
	return &fakeObj{rec: rec, path: path, props: map[string]Value{}, meths: map[string]func([]Value) (Value, error){}}
}

func (o *fakeObj) Get(name string, args []Value) (Value, error) {
	o.rec.logf("GET %s.%s(%s)", o.path, name, fmtVals(args))
	if m, ok := o.meths[name]; ok {
		return m(args)
	}
	if v, ok := o.props[name]; ok {
		return v, nil
	}
	return nil, NewError(438, "")
}

func (o *fakeObj) Set(name string, args []Value, v Value) error {
	o.rec.logf("SET %s.%s(%s) = %s", o.path, name, fmtVals(args), fmtVal(v))
	o.props[name] = v
	return nil
}

// fakeScreen simulates just enough of a terminal for the end-to-end
// scripts: Send looks the text up in responses and appends the reply to the
// pending output, which the Wait*/Read* methods consume.
type fakeScreen struct {
	*fakeObj
	pending   string
	sent      []string
	responses map[string]string
}

func newFakeScreen(rec *recorder, path string) *fakeScreen {
	s := &fakeScreen{fakeObj: newFake(rec, path), responses: map[string]string{}}
	s.props["synchronous"] = false
	s.meths["send"] = func(args []Value) (Value, error) {
		text := ToString(args[0])
		s.sent = append(s.sent, text)
		if r, ok := s.responses[text]; ok {
			s.pending += r
		}
		return nil, nil
	}
	s.meths["waitforstring"] = func(args []Value) (Value, error) {
		want := ToString(args[0])
		i := strings.Index(s.pending, want)
		if i < 0 {
			return false, nil
		}
		s.pending = s.pending[i+len(want):]
		return true, nil
	}
	s.meths["waitforstrings"] = func(args []Value) (Value, error) {
		// Trailing numeric argument is the timeout.
		var wants []string
		for _, a := range args {
			switch x := a.(type) {
			case string:
				wants = append(wants, x)
			case *Array:
				for _, e := range x.Items() {
					wants = append(wants, ToString(e))
				}
			}
		}
		best, bestAt := 0, -1
		for n, w := range wants {
			if i := strings.Index(s.pending, w); i >= 0 && (bestAt < 0 || i < bestAt) {
				best, bestAt = n+1, i
			}
		}
		if best > 0 {
			s.pending = s.pending[bestAt+len(wants[best-1]):]
		}
		return best, nil
	}
	s.meths["readstring"] = func(args []Value) (Value, error) {
		delim := ToString(args[0])
		i := strings.Index(s.pending, delim)
		if i < 0 {
			out := s.pending
			s.pending = ""
			return out, nil
		}
		out := s.pending[:i]
		s.pending = s.pending[i+len(delim):]
		return out, nil
	}
	return s
}

// fakeCrt is a small "crt" object tree: crt.Screen, crt.Session, crt.Dialog,
// crt.GetTab(n) and crt.GetScriptTab.
type fakeCrt struct {
	*fakeObj
	rec    *recorder
	screen *fakeScreen
	tabs   []*fakeObj
}

func newFakeCrt() *fakeCrt {
	rec := &recorder{}
	c := &fakeCrt{fakeObj: newFake(rec, "crt"), rec: rec}
	c.screen = newFakeScreen(rec, "crt.screen")
	c.props["screen"] = c.screen
	c.props["version"] = "9.9"

	session := newFake(rec, "crt.session")
	session.props["connected"] = true
	session.props["remoteaddress"] = "10.0.0.1"
	session.meths["connect"] = func(args []Value) (Value, error) { return nil, nil }
	session.meths["disconnect"] = func(args []Value) (Value, error) {
		session.props["connected"] = false
		return nil, nil
	}
	c.props["session"] = session

	dialog := newFake(rec, "crt.dialog")
	dialog.meths["messagebox"] = func(args []Value) (Value, error) { return 1, nil }
	dialog.meths["prompt"] = func(args []Value) (Value, error) { return "typed", nil }
	c.props["dialog"] = dialog

	for i := 1; i <= 2; i++ {
		tab := newFake(rec, fmt.Sprintf("tab%d", i))
		tab.props["caption"] = fmt.Sprintf("Tab %d", i)
		tab.props["index"] = i
		tab.props["screen"] = newFakeScreen(rec, fmt.Sprintf("tab%d.screen", i))
		c.tabs = append(c.tabs, tab)
	}
	c.meths["gettab"] = func(args []Value) (Value, error) {
		n := int(ToInt(args[0]))
		if n < 1 || n > len(c.tabs) {
			return nil, fmt.Errorf("tab %d does not exist", n)
		}
		return c.tabs[n-1], nil
	}
	c.meths["getscripttab"] = func(args []Value) (Value, error) { return c.tabs[0], nil }
	c.meths["gettabcount"] = func(args []Value) (Value, error) { return len(c.tabs), nil }
	c.meths["sleep"] = func(args []Value) (Value, error) { return nil, nil }
	return c
}

// newHost returns an interpreter wired to a fake crt object and an output
// buffer.
func newHost() (*Interpreter, *fakeCrt, *bytes.Buffer) {
	in := New()
	var buf bytes.Buffer
	in.SetOutput(&buf)
	crt := newFakeCrt()
	in.SetGlobal("crt", crt)
	return in, crt, &buf
}

func lines(buf *bytes.Buffer) string {
	return strings.TrimRight(buf.String(), "\n")
}
