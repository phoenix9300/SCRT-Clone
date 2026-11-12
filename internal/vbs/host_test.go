package vbs

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestHostRouting(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		calls []string
		out   string
	}{
		{"send", `crt.Screen.Send "a" & vbCr`,
			[]string{`GET crt.screen()`, `GET crt.screen.send("a\r")`}, ""},
		{"set property", `crt.Screen.Synchronous = True`,
			[]string{`GET crt.screen()`, `SET crt.screen.synchronous() = Boolean:True`}, ""},
		{"wait in if", `If crt.Screen.WaitForString("x", 5) Then WScript.Echo "found" Else WScript.Echo "timeout"`,
			[]string{`GET crt.screen()`, `GET crt.screen.waitforstring("x", Integer:5)`}, "timeout"},
		{"get tab", `Set tab = crt.GetTab(1)`,
			[]string{`GET crt.gettab(Integer:1)`}, ""},
		{"wait for strings", `n = crt.Screen.WaitForStrings("a", "b", 10)
WScript.Echo n`,
			[]string{`GET crt.screen()`, `GET crt.screen.waitforstrings("a", "b", Integer:10)`}, "0"},
		{"dialog", `crt.Dialog.MessageBox "hi"`,
			[]string{`GET crt.dialog()`, `GET crt.dialog.messagebox("hi")`}, ""},
		{"dialog result", `r = crt.Dialog.MessageBox("hi", "title", 4)
WScript.Echo r`,
			[]string{`GET crt.dialog()`, `GET crt.dialog.messagebox("hi", "title", Integer:4)`}, "1"},
		{"chained", `crt.GetTab(1).Screen.Send "x"`,
			[]string{`GET crt.gettab(Integer:1)`, `GET tab1.screen()`, `GET tab1.screen.send("x")`}, ""},
		{"chained property read", `WScript.Echo crt.GetTab(2).Caption`,
			[]string{`GET crt.gettab(Integer:2)`, `GET tab2.caption()`}, "Tab 2"},
		{"with block", "With crt.Screen\n .Send \"q\"\n .Synchronous = True\n x = .WaitForString(\"z\")\nEnd With",
			[]string{`GET crt.screen()`, `GET crt.screen.send("q")`, `SET crt.screen.synchronous() = Boolean:True`, `GET crt.screen.waitforstring("z")`}, ""},
		{"with block evaluates target once", "With crt.Session\n .Connect \"x\"\n .Disconnect\nEnd With\nWScript.Echo crt.Session.Connected",
			[]string{`GET crt.session()`, `GET crt.session.connect("x")`, `GET crt.session.disconnect()`, `GET crt.session()`, `GET crt.session.connected()`}, "False"},
		{"object variable reuse", `Set scr = crt.Screen
scr.Send "1"
scr.Send "2"`,
			[]string{`GET crt.screen()`, `GET crt.screen.send("1")`, `GET crt.screen.send("2")`}, ""},
		{"call statement", `Call crt.Screen.Send("hello")`,
			[]string{`GET crt.screen()`, `GET crt.screen.send("hello")`}, ""},
		{"parenthesised statement args", `crt.Screen.Send ("p")`,
			[]string{`GET crt.screen()`, `GET crt.screen.send("p")`}, ""},
		{"named values", `crt.Sleep 100`,
			[]string{`GET crt.sleep(Integer:100)`}, ""},
		{"read only property", `WScript.Echo crt.Version`,
			[]string{`GET crt.version()`}, "9.9"},
		{"case insensitive routing", `CRT.SCREEN.SEND "u"`,
			[]string{`GET crt.screen()`, `GET crt.screen.send("u")`}, ""},
		{"set object property", `Set crt.Screen = Nothing`,
			[]string{`SET crt.screen() = Nothing`}, ""},
		{"index assignment", `crt.Screen.Cells(1, 2) = "z"`,
			[]string{`GET crt.screen()`, `SET crt.screen.cells(Integer:1, Integer:2) = "z"`}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, crt, buf := newHost()
			err := in.Run(context.Background(), c.src, "t.vbs")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !reflect.DeepEqual(crt.rec.calls, c.calls) {
				t.Errorf("calls:\n got %q\nwant %q", crt.rec.calls, c.calls)
			}
			if got := lines(buf); got != c.out {
				t.Errorf("output %q, want %q", got, c.out)
			}
		})
	}
}

func TestHostDefaultMember(t *testing.T) {
	in, crt, buf := newHost()
	args := newFake(crt.rec, "crt.args")
	args.meths[""] = func(a []Value) (Value, error) {
		if len(a) == 0 {
			return "all", nil
		}
		return "arg" + ToString(a[0]), nil
	}
	// Like the real host, the Arguments property applies indexing arguments
	// itself: crt.Arguments(0) arrives as Get("arguments", [0]).
	crt.meths["arguments"] = func(a []Value) (Value, error) {
		if len(a) == 0 {
			return args, nil
		}
		return "direct" + ToString(a[0]), nil
	}
	err := in.Run(context.Background(), `
Set a = crt.Arguments
WScript.Echo a(0), a(1)
WScript.Echo crt.Arguments(0)
WScript.Echo a`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`GET crt.arguments()`,
		`GET crt.args.(Integer:0)`,
		`GET crt.args.(Integer:1)`,
		`GET crt.arguments(Integer:0)`,
		`GET crt.args.()`,
	}
	if !reflect.DeepEqual(crt.rec.calls, want) {
		t.Errorf("calls:\n got %q\nwant %q", crt.rec.calls, want)
	}
	if got := lines(buf); got != "arg0 arg1\ndirect0\nall" {
		t.Errorf("output %q", got)
	}
}

func TestHostValueReturns(t *testing.T) {
	in, crt, buf := newHost()
	crt.screen.pending = "hello login: "
	err := in.Run(context.Background(), `
WScript.Echo crt.Screen.WaitForString("login:", 1)
WScript.Echo crt.Screen.WaitForString("zzz", 1)
crt.Screen.Send "root"
crt.Screen.Send "again"
`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	if got := lines(buf); got != "True\nFalse" {
		t.Errorf("output %q", got)
	}
	if !reflect.DeepEqual(crt.screen.sent, []string{"root", "again"}) {
		t.Errorf("sent %q", crt.screen.sent)
	}
}

func TestHostErrorsFromCrt(t *testing.T) {
	in, _, buf := newHost()
	err := in.Run(context.Background(), `
On Error Resume Next
Set t = crt.GetTab(99)
WScript.Echo Err.Number, Err.Description
Err.Clear
x = crt.NoSuchMember
WScript.Echo Err.Number`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1000 tab 99 does not exist\n438"; lines(buf) != want {
		t.Errorf("got %q want %q", lines(buf), want)
	}
	in, _, _ = newHost()
	err = in.Run(context.Background(), "x = 1\nSet t = crt.GetTab(99)", "t.vbs")
	var e *Error
	if !errors.As(err, &e) || e.Number != HostErrorNumber || e.Line != 2 {
		t.Errorf("err = %#v", err)
	}
}

func TestSubMainAndCall(t *testing.T) {
	in, crt, buf := newHost()
	err := in.Run(context.Background(), `
Sub Main
  crt.Screen.Send "from main" & vbCr
  WScript.Echo "main ran"
End Sub
Function Add(a, b) : Add = a + b : End Function
WScript.Echo "loaded"`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	if !in.HasProc("Main") || !in.HasProc("main") || !in.HasProc("ADD") || in.HasProc("Nope") {
		t.Errorf("HasProc wrong: Main=%v Add=%v Nope=%v", in.HasProc("Main"), in.HasProc("ADD"), in.HasProc("Nope"))
	}
	if len(crt.rec.calls) != 0 {
		t.Errorf("Run must not call Main: %q", crt.rec.calls)
	}
	if _, err := in.Call(context.Background(), "Main"); err != nil {
		t.Fatal(err)
	}
	want := []string{`GET crt.screen()`, `GET crt.screen.send("from main\r")`}
	if !reflect.DeepEqual(crt.rec.calls, want) {
		t.Errorf("calls %q", crt.rec.calls)
	}
	v, err := in.Call(context.Background(), "Add", int64(2), int64(40))
	if err != nil || ToInt(v) != 42 {
		t.Errorf("Add = %v, %v", v, err)
	}
	if got := lines(buf); got != "loaded\nmain ran" {
		t.Errorf("output %q", got)
	}
	if _, err := in.Call(context.Background(), "Nope"); err == nil {
		t.Error("calling an undefined procedure must fail")
	}
	var e *Error
	_, err = in.Call(context.Background(), "Add", int64(1))
	if !errors.As(err, &e) || e.Number != 450 {
		t.Errorf("wrong argument count: %v", err)
	}
	if _, ok := in.Global("x"); ok {
		t.Errorf("Global of an unknown name should be absent")
	}
}

func TestCallHostScriptCallback(t *testing.T) {
	// A host object calling back into script code is not supported across
	// goroutines, but Call from the same goroutine between Runs must keep
	// global state.
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	if err := in.Run(context.Background(), "counter = 0\nSub Bump : counter = counter + 1 : End Sub", "t.vbs"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := in.Call(context.Background(), "Bump"); err != nil {
			t.Fatal(err)
		}
	}
	if v, ok := in.Global("counter"); !ok || ToInt(v) != 3 {
		t.Errorf("counter = %v, %v", v, ok)
	}
	if err := in.Run(context.Background(), "Bump\nWScript.Echo counter", "second.vbs"); err != nil || buf.String() != "4\n" {
		t.Errorf("second run: %v %q", err, buf.String())
	}
}

func TestDirectivesAndRegisterClass(t *testing.T) {
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	in.RegisterClass("My.Thing", func() (Object, error) {
		o := newFake(&recorder{}, "thing")
		o.props["hello"] = "world"
		return o, nil
	})
	in.RegisterClass("WScript.Shell", func() (Object, error) { return nil, errors.New("disabled") })
	err := in.Run(context.Background(), `# $language = "VBScript"
# $interface = "1.0"
Set o = CreateObject("My.Thing")
WScript.Echo o.Hello
On Error Resume Next
Set s = CreateObject("WScript.Shell")
WScript.Echo Err.Number <> 0`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != "world\nTrue\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestCancellation(t *testing.T) {
	scripts := map[string]string{
		"do loop":    "Do : Loop",
		"for loop":   "For i = 1 To 2000000000 : Next",
		"while":      "While True : Wend",
		"sleep":      "WScript.Sleep 60000",
		"nested":     "Do\nFor i = 1 To 1000000000\nNext\nLoop",
		"in proc":    "Sub S : Do : Loop : End Sub\nS",
		"trapped":    "On Error Resume Next\nDo\nx = 1 / 0\nLoop",
		"sleep loop": "Do : WScript.Sleep 1000 : Loop",
	}
	for name, src := range scripts {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := New()
			in.SetOutput(&bufW{})
			done := make(chan error, 1)
			go func() { done <- in.Run(ctx, src, "t.vbs") }()
			time.Sleep(30 * time.Millisecond)
			start := time.Now()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("err = %v, want context.Canceled", err)
				}
				if d := time.Since(start); d > 200*time.Millisecond {
					t.Errorf("stopped after %v", d)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("script did not stop")
			}
		})
	}
}

func TestCancellationDeadlineAndCall(t *testing.T) {
	in := New()
	if err := in.Run(context.Background(), "Sub Spin : Do : Loop : End Sub", "t.vbs"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := in.Call(ctx, "Spin")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 250*time.Millisecond {
		t.Errorf("err=%v after %v", err, time.Since(start))
	}
	// The interpreter stays usable after a cancelled run.
	buf := &bufW{}
	in.SetOutput(buf)
	if err := in.Run(context.Background(), "WScript.Echo \"alive\"", "t.vbs"); err != nil || buf.String() != "alive\n" {
		t.Errorf("after cancel: %v %q", err, buf.String())
	}
}

func TestCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	err := in.Run(ctx, "WScript.Echo \"x\"", "t.vbs")
	if !errors.Is(err, context.Canceled) || buf.String() != "" {
		t.Errorf("err=%v out=%q", err, buf.String())
	}
}

func TestCloseIdempotent(t *testing.T) {
	in := New()
	buf := &bufW{}
	in.SetOutput(buf)
	err := in.Run(context.Background(), `
Class T : Sub Class_Terminate : WScript.Echo "t" : End Sub : End Class
Set a = New T
Set b = New T`, "t.vbs")
	if err != nil {
		t.Fatal(err)
	}
	in.Close()
	in.Close()
	if buf.String() != "t\nt\n" {
		t.Errorf("got %q", buf.String())
	}
}
