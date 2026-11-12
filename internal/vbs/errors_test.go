package vbs

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOnError(t *testing.T) {
	runCases(t, []tcase{
		{"resume next continues", `
On Error Resume Next
x = 1 / 0
WScript.Echo "after", Err.Number, Err.Description`, "after 11 Division by zero"},
		{"goto 0 restores", `
On Error Resume Next
x = 1 / 0
WScript.Echo Err.Number
On Error GoTo 0
WScript.Echo "ok"`, "11\nok"},
		{"err clear", `
On Error Resume Next
x = 1 / 0
Err.Clear
WScript.Echo Err.Number, "[" & Err.Description & "]"`, "0 []"},
		{"err persists until next error", `
On Error Resume Next
x = 1 / 0
y = 5
WScript.Echo Err.Number`, "11"},
		{"on error resets err", `
On Error Resume Next
x = 1 / 0
On Error Resume Next
WScript.Echo Err.Number`, "0"},
		{"raise", `
On Error Resume Next
Err.Raise 1234, "MySrc", "custom message"
WScript.Echo Err.Number, Err.Source, Err.Description`, "1234 MySrc custom message"},
		{"raise number only", `
On Error Resume Next
Err.Raise 5
WScript.Echo Err.Number, Err.Description`, "5 Invalid procedure call or argument"},
		{"raise vbObjectError", `
On Error Resume Next
Err.Raise vbObjectError + 7, "src", "boom"
WScript.Echo Err.Number = vbObjectError + 7`, "True"},
		{"default member of err is number", `
On Error Resume Next
x = 1 / 0
WScript.Echo Err = 11, Err`, "True 11"},
		{"runtime error source", `
On Error Resume Next
x = 1 / 0
WScript.Echo Err.Source`, "Microsoft VBScript runtime error"},
		{"error in called procedure", `
Sub Boom : x = 1 / 0 : WScript.Echo "not reached" : End Sub
On Error Resume Next
Boom
WScript.Echo "main", Err.Number`, "main 11"},
		{"on error is per procedure", `
Sub Inner : On Error Resume Next : x = 1 / 0 : WScript.Echo "inner " & Err.Number : End Sub
Sub Outer : Inner : WScript.Echo "outer" : End Sub
Outer`, "inner 11\nouter"},
		{"error propagates out of proc without handler", `
Sub Boom : x = 1 / 0 : WScript.Echo "no" : End Sub
Sub Caller
  On Error Resume Next
  Boom
  WScript.Echo "caller", Err.Number
End Sub
Caller`, "caller 11"},
		{"resume next in if condition", `
On Error Resume Next
If 1 / 0 = 1 Then WScript.Echo "then" Else WScript.Echo "else"`, "then"},
		{"checking err pattern", `
On Error Resume Next
Set o = CreateObject("No.Such.Prog")
If Err.Number <> 0 Then WScript.Echo "failed", Err.Number
Err.Clear
WScript.Echo "done"`, "failed 429\ndone"},
		{"type mismatch number", `
On Error Resume Next
x = "a" + 1
WScript.Echo Err.Number, Err.Description`, "13 Type mismatch"},
		{"subscript out of range", `
On Error Resume Next
Dim a(1)
a(5) = 1
WScript.Echo Err.Number, Left(Err.Description, 22)`, "9 Subscript out of range"},
		{"object required", `
On Error Resume Next
x = 5
x.Foo = 1
WScript.Echo Err.Number`, "424"},
		{"error in loop continues", `
On Error Resume Next
For i = 1 To 3
  x = 1 / 0
  n = n + 1
Next
WScript.Echo n`, "3"},
		{"err.raise inside proc caught by caller", `
Sub Thrower : Err.Raise 4000, "T", "bad" : End Sub
On Error Resume Next
Thrower
WScript.Echo Err.Number, Err.Source`, "4000 T"},
		{"err.description assignable", `
On Error Resume Next
Err.Description = "mine"
WScript.Echo Err.Description`, "mine"},
		{"err.number assignable", `
Err.Number = 77
WScript.Echo Err.Number
Err.Clear
WScript.Echo Err.Number`, "77\n0"},
		{"on error goto 0 inside proc then error is fatal", `
On Error Resume Next
Sub S
  On Error GoTo 0
End Sub
S
x = 1 / 0
WScript.Echo "still", Err.Number`, "still 11"},
	})
}

func TestUncaughtErrors(t *testing.T) {
	runErrCases(t, []errCase{
		{"division line", "x = 1\ny = 2\nz = x / 0", 11, 3, false},
		{"line in proc", "Sub S\n  x = 1\n  y = 1 / 0\nEnd Sub\nS", 11, 3, false},
		{"line after continuation", "x = 1 + _\n 2\ny = 1 / 0", 11, 3, false},
		{"raise", "Err.Raise 5000, \"s\", \"msg\"", 5000, 1, false},
		{"after resume next goto 0", "On Error Resume Next\nOn Error GoTo 0\nx = 1 / 0", 11, 3, false},
		{"in class method", "Class C\n Sub M\n  x = 1 / 0\n End Sub\nEnd Class\nSet o = New C\no.M", 11, 3, false},
		{"in multiline if", "If True Then\n  x = 1\n  x = x / 0\nEnd If", 11, 3, false},
		{"undefined member of dictionary", "Set d = CreateObject(\"Scripting.Dictionary\")\nd.Nope", 438, 2, false},
		{"createobject unknown", "Set o = CreateObject(\"Nope.Nope\")", 429, 1, false},
	})
	t.Run("error text", func(t *testing.T) {
		_, err := runOut(t, "x = 1\ny = 1 / 0")
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("got %v", err)
		}
		if e.Col == 0 || e.Source == "" || e.Message != "Division by zero" {
			t.Errorf("unexpected error fields: %+v", e)
		}
		want := fmt.Sprintf("test.vbs(2, %d): VBScript runtime error 11: Division by zero", e.Col)
		if e.Error() != want {
			t.Errorf("Error() = %q, want %q", e.Error(), want)
		}
	})
}

func TestSyntaxErrors(t *testing.T) {
	runErrCases(t, []errCase{
		{"missing then", "If True\nx = 1\nEnd If", 1017, 1, true},
		{"unterminated if", "If True Then\nx = 1", 1014, 0, true},
		{"unterminated string", "x = \"abc", 1033, 1, true},
		{"bad expression", "x = = 1", 1023, 1, true},
		{"unexpected end", "End If", 1024, 1, true},
		{"bad do", "Do\nx = 1\nNext", 1019, 0, true},
		{"line number", "x = 1\ny = 2\nz = (3", 1006, 3, true},
		{"unterminated sub", "Sub S\nx = 1", 1014, 0, true},
		{"nested sub", "Sub A\nSub B\nEnd Sub\nEnd Sub", 1002, 2, true},
		{"unterminated class", "Class C\nPublic x", 1014, 0, true},
		{"bad character", "x = 1 ` 2", 1032, 1, true},
	})
	t.Run("never executes any code", func(t *testing.T) {
		out, err := runOut(t, "WScript.Echo \"first\"\nx = = 1")
		if err == nil || out != "" {
			t.Fatalf("got out=%q err=%v", out, err)
		}
	})
	t.Run("column", func(t *testing.T) {
		_, err := runOut(t, "x = 1\n  y = (")
		var e *Error
		if !errors.As(err, &e) || !e.Syntax || e.Line != 2 || e.Col == 0 {
			t.Fatalf("got %+v", err)
		}
	})
}

func TestOptionExplicit(t *testing.T) {
	runErrCases(t, []errCase{
		{"undeclared", "Option Explicit\nx = 1", 500, 2, false},
		{"undeclared read", "Option Explicit\nWScript.Echo y", 500, 2, false},
		{"undeclared in proc", "Option Explicit\nSub S\n z = 1\nEnd Sub\nS", 500, 3, false},
		{"for variable", "Option Explicit\nFor i = 1 To 2\nNext", 500, 2, false},
	})
	runCases(t, []tcase{
		{"declared ok", `
Option Explicit
Dim x, y
Sub S(p) : Dim loc : loc = p : WScript.Echo loc : End Sub
x = 1 : y = 2
S x + y
Dim v
For Each v In Array(1)
Next`, "3"},
		{"trapped by resume next", `
Option Explicit
On Error Resume Next
q = 1
WScript.Echo Err.Number`, "500"},
	})
}

// failingHost is a host object that always returns plain Go errors.
type failingHost struct{}

func (failingHost) Get(name string, args []Value) (Value, error) {
	switch name {
	case "typed":
		return nil, NewError(5555, "typed host failure")
	case "ok":
		return "fine", nil
	}
	return nil, errors.New("host exploded")
}
func (failingHost) Set(name string, args []Value, v Value) error { return errors.New("cannot set") }

func TestHostErrors(t *testing.T) {
	in := New()
	var buf = &bufW{}
	in.SetOutput(buf)
	in.SetGlobal("h", failingHost{})
	err := in.Run(context.Background(), `
On Error Resume Next
h.Boom
WScript.Echo Err.Number, Err.Description
Err.Clear
h.Typed
WScript.Echo Err.Number, Err.Description
Err.Clear
h.X = 1
WScript.Echo Err.Number, Err.Description
Err.Clear
WScript.Echo h.Ok, Err.Number`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%d host exploded\n5555 typed host failure\n%d cannot set\nfine 0\n", HostErrorNumber, HostErrorNumber)
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}

	in2 := New()
	in2.SetGlobal("h", failingHost{})
	err = in2.Run(context.Background(), "x = 1\nh.Boom", "t.vbs")
	var e *Error
	if !errors.As(err, &e) || e.Number != HostErrorNumber || e.Line != 2 || e.Message != "host exploded" {
		t.Errorf("uncaught host error: %#v (%v)", e, err)
	}
}

func TestHostFunc(t *testing.T) {
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	in.SetFunc("Twice", func(args []Value) (Value, error) { return ToInt(args[0]) * 2, nil })
	in.SetFunc("Fail", func(args []Value) (Value, error) { return nil, errors.New("nope") })
	err := in.Run(context.Background(), `
WScript.Echo Twice(21)
On Error Resume Next
Fail
WScript.Echo Err.Number, Err.Description`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("42\n%d nope\n", HostErrorNumber); buf.String() != want {
		t.Errorf("got %q", buf.String())
	}
}

type bufW struct{ b []byte }

func (w *bufW) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *bufW) String() string              { return string(w.b) }

func TestKnownGaps(t *testing.T) {
	t.Run("exit outside loop", func(t *testing.T) {
		t.Skip("known gap: Exit Do/For outside a matching loop is not rejected at compile time")
	})
	t.Run("call target followed by member", func(t *testing.T) {
		t.Skip("known gap: `WScript.Echo (obj).Member` parses the parentheses as an argument list")
	})
}
