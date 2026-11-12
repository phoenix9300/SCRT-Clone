package script

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
)

// ----------------------------------------------------------------- Screen

func TestScreenExtras(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string {
		// move the cursor once the script reports it is waiting for it
		go func() {
			for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
				rs := f.h.Runs()
				if len(rs) > 0 && strings.Contains(rs[len(rs)-1].Output, "armed") {
					time.Sleep(200 * time.Millisecond)
					f.ui.moveCursor()
					return
				}
			}
		}()
		return nil
	}
	runLangs(t, lsrc{
		py: `
s = crt.Screen
s.Synchronous = True
s.SendSpecial("TN_BREAK")
s.SendBreak()
s.SendSpecial("no-such-special")
print("break=ok")
print("cursor-still=%d" % int(s.WaitForCursor(0.3)))
print("armed")
print("cursor-moved=%d" % int(s.WaitForCursor(10)))
s.Send("echo hello-c$((1+1))\r")
print("nocase0=%d" % int(s.WaitForString("HELLO-C2", 1)))
s.IgnoreCase = True
print("nocase1=%d" % int(s.WaitForString("HELLO-C2", 5)))
s.IgnoreCase = False
s.Send("echo again-c$((1+1))\r")
print("nocase2=%d" % int(s.WaitForString("AGAIN-C2", 5, True)))
s.Send("echo a$((1+1))-m$((2*3))n-$((7+2))z\r")
s.ReadString("zzz", "a2-", 10)
print("mi1=%d" % s.MatchIndex)
r = s.ReadString("nope", "n-", "9z", 10)
print("r=[" + r + "] mi2=%d" % s.MatchIndex)
r = s.ReadString("9z", 10)
print("r2=[" + r + "] mi3=%d" % s.MatchIndex)
import time
t0 = time.time()
r = s.ReadString("never-here", 1)
print("timeout=%d mi=%d len=%d" % (int(time.time() - t0 >= 0.9), s.MatchIndex, len(r) > 0))
`,
		js: `
var s = crt.Screen; s.Synchronous = true;
s.SendSpecial("TN_BREAK"); s.SendBreak(); s.SendSpecial("no-such-special");
print("break=ok");
print("cursor-still=" + (s.WaitForCursor(0.3) ? 1 : 0));
print("armed");
print("cursor-moved=" + (s.WaitForCursor(10) ? 1 : 0));
s.Send("echo hello-c$((1+1))\r");
print("nocase0=" + (s.WaitForString("HELLO-C2", 1) ? 1 : 0));
s.IgnoreCase = true;
print("nocase1=" + (s.WaitForString("HELLO-C2", 5) ? 1 : 0));
s.IgnoreCase = false;
s.Send("echo again-c$((1+1))\r");
print("nocase2=" + (s.WaitForString("AGAIN-C2", 5, true) ? 1 : 0));
s.Send("echo a$((1+1))-m$((2*3))n-$((7+2))z\r");
s.ReadString("zzz", "a2-", 10);
print("mi1=" + s.MatchIndex);
var r = s.ReadString("nope", "n-", "9z", 10);
print("r=[" + r + "] mi2=" + s.MatchIndex);
r = s.ReadString("9z", 10);
print("r2=[" + r + "] mi3=" + s.MatchIndex);
var t0 = Date.now();
r = s.ReadString("never-here", 1);
print("timeout=" + (Date.now() - t0 >= 900 ? 1 : 0) + " mi=" + s.MatchIndex + " len=" + (r.length > 0 ? 1 : 0));
`,
		vbs: vbB + `
Set s = crt.Screen
s.Synchronous = True
s.SendSpecial "TN_BREAK"
s.SendBreak
WScript.Echo "break=ok"
WScript.Echo "cursor-still=" & B(s.WaitForCursor(0.3))
WScript.Echo "armed"
WScript.Echo "cursor-moved=" & B(s.WaitForCursor(10))
s.Send "echo hello-c$((1+1))" & vbCr
WScript.Echo "nocase0=" & B(s.WaitForString("HELLO-C2", 1))
s.IgnoreCase = True
WScript.Echo "nocase1=" & B(s.WaitForString("HELLO-C2", 5))
s.Send "echo a$((1+1))-m$((2*3))n-$((7+2))z" & vbCr
s.ReadString "zzz", "a2-", 10
WScript.Echo "mi1=" & s.MatchIndex
r = s.ReadString("nope", "n-", "9z", 10)
WScript.Echo "r=[" & r & "] mi2=" & s.MatchIndex
`}, prep, nil, "break=ok", "cursor-still=0", "cursor-moved=1", "nocase0=0", "nocase1=1", "mi1=2", "r=[m6] mi2=2")
}

func TestScreenExtrasPythonTail(t *testing.T) {
	// the remaining checks of TestScreenExtras that only the Python/JS variants make
	needPython(t)
	h, cn, ui := setup(t)
	_ = ui
	info := runScript(t, h, cn, "py", `
s = crt.Screen
s.Synchronous = True
s.Send("echo a$((1+1))-m$((2*3))n-$((7+2))z\r")
s.ReadString("a2-", 10)
r = s.ReadString("9z", 10)
print("r=[" + r + "] mi=%d" % s.MatchIndex)
`)
	if !strings.Contains(info.Output, "r=[m6n-] mi=1") {
		t.Fatalf("%+v", info)
	}
}

func TestWaitsEndWithDisconnect(t *testing.T) {
	runLangs(t, lsrc{
		py: `
s = crt.Screen
s.Send("exit\r")
r1 = s.WaitForString("never-x", 20)
r2 = s.WaitForStrings(["a-never", "b-never"], 20)
r3 = s.ReadString("zzz-never", 20)
print("r1=%d r2=%d r3=%d" % (int(r1), r2, len(r3) >= 0))
try:
    s.Send("x")
except RuntimeError as e:
    print("send=raised")
`,
		js: `
var s = crt.Screen;
s.Send("exit\r");
var r1 = s.WaitForString("never-x", 20);
var r2 = s.WaitForStrings(["a-never", "b-never"], 20);
var r3 = s.ReadString("zzz-never", 20);
print("r1=" + (r1 ? 1 : 0) + " r2=" + r2 + " r3=" + (r3.length >= 0 ? 1 : 0));
try { s.Send("x"); } catch (e) { print("send=raised"); }
`,
		vbs: vbB + `
Set s = crt.Screen
s.Send "exit" & vbCr
r1 = s.WaitForString("never-x", 20)
r2 = s.WaitForStrings(Array("a-never", "b-never"), 20)
r3 = s.ReadString("zzz-never", 20)
WScript.Echo "r1=" & B(r1) & " r2=" & r2 & " r3=1"
On Error Resume Next
s.Send "x"
If Err.Number <> 0 Then WScript.Echo "send=raised"
`}, nil, nil, "r1=0 r2=0 r3=1", "send=raised")
}

// A wait with no time-out ends when the session ends.
func TestUntimedWaitEndsWithDisconnect(t *testing.T) {
	h, cn, _ := setup(t)
	r, err := h.Start(RunRequest{Conn: cn.ID, Lang: "js", Source: `
print("start");
var r = crt.Screen.WaitForString("never-appears");
print("returned=" + r);
`})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "script start", func() bool { return strings.Contains(r.Info().Output, "start") })
	cn.Disconnect()
	select {
	case <-r.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return after the disconnect")
	}
	if info := r.Info(); info.State != StateDone || !strings.Contains(info.Output, "returned=false") {
		t.Fatalf("%+v", info)
	}
}

// ---------------------------------------------------------------- Python

func pgrepShim(t *testing.T, tmp string) []string {
	t.Helper()
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("no pgrep")
	}
	out, _ := exec.Command("pgrep", "-f", filepath.Join(tmp, "hawser-py")).Output()
	return strings.Fields(string(out))
}

func TestPythonTraceback(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	p := filepath.Join(t.TempDir(), "tb_test.py")
	r, err := h.Start(RunRequest{Conn: cn.ID, Path: p, Source: "import os\n\ndef inner():\n    raise ValueError(\"deep\")\n\ndef outer():\n    inner()\n\nouter()\n"})
	if err != nil {
		t.Fatal(err)
	}
	info := r.Wait()
	if info.State != StateError {
		t.Fatalf("%+v", info)
	}
	for _, w := range []string{p + `", line 9, in <module>`, p + `", line 7, in outer`, p + `", line 4, in inner`, "ValueError: deep"} {
		if !strings.Contains(info.Error, w) {
			t.Errorf("error lacks %q:\n%s", w, info.Error)
		}
	}
	if strings.Contains(info.Error, "hawser_shim") {
		t.Errorf("shim frames leak into the traceback:\n%s", info.Error)
	}
	// a syntax error names the file and line too
	info = runScript(t, h, cn, "py", "x = 1\nif x:\nprint(x)\n")
	if info.State != StateError || !strings.Contains(info.Error, "line 3") {
		t.Errorf("syntax error: %+v", info)
	}
}

func TestPythonArgvAndExit(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	p := filepath.Join(t.TempDir(), "argv.py")
	r, _ := h.Start(RunRequest{Conn: cn.ID, Path: p, Args: []string{"a b", "é中", ""}, Source: "import sys\nprint(sys.argv[0] == __file__, sys.argv[1:], __name__)\nsys.exit()\nprint('unreachable')\n"})
	info := r.Wait()
	want := `True ['a b', 'é中', ''] __main__`
	if info.State != StateDone || !strings.Contains(info.Output, want) || strings.Contains(info.Output, "unreachable") {
		t.Fatalf("%+v", info)
	}
	for src, want := range map[string]string{"import sys\nsys.exit(0)": "", "import sys\nsys.exit(3)": "status 3", "raise SystemExit('bye')": "bye"} {
		info := runScript(t, h, cn, "py", src)
		if want == "" && info.State != StateDone || want != "" && (info.State != StateError || !strings.Contains(info.Error, want)) {
			t.Errorf("%q: %+v", src, info)
		}
	}
}

func TestPythonUnicode(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	info := runScript(t, h, cn, "py", `
s = crt.Screen; s.Synchronous = True
s.Send("echo é中$((1+1))\r")
print("ok=%d" % int(s.WaitForString("é中2", 10)))
text = s.ReadString("\r", 5)
print("arg=" + crt.Arguments[0] + " len=%d" % len(crt.Arguments[0]))
crt.Clipboard.Text = "\x00\x01\x1b[31m\U0001F600\u2028 \"quoted\" \\ end"
print("clip=%d" % int(crt.Clipboard.Text == "\x00\x01\x1b[31m\U0001F600\u2028 \"quoted\" \\ end"))
`, "ü€\U0001F600")
	if info.State != StateDone || !strings.Contains(info.Output, "ok=1") || !strings.Contains(info.Output, "arg=ü€\U0001F600 len=3") || !strings.Contains(info.Output, "clip=1") {
		t.Fatalf("%+v", info)
	}
	// a latin-1 session cannot carry the characters: they degrade, not fail
	cn2, err := h.Mgr.Open(conn.OpenRequest{Overrides: map[string]any{"protocol": "local", "local": map[string]any{"shell": "/bin/sh"}, "terminal": map[string]any{"encoding": "iso-8859-1"}}}, h.UI.(*fakeUI).sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cn2, conn.StateConnected)
	if got := encodeText(cn2, "é中"); string(got) != "\xe9?" {
		t.Errorf("latin-1 encoding: %q", got)
	}
}

func TestPythonProtocolRobustness(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	big := strings.Repeat("x", 300000)
	info := runScript(t, h, cn, "py", `
import sys, io, os, subprocess
print("arglen=%d" % len(crt.Arguments[0]))
crt.Clipboard.Text = "y" * 2000000
print("cliplen=%d" % len(crt.Clipboard.Text))
sys.stdout.write("z" * 3000000 + "\n")
for i in range(30000):
    print("line", i)
sys.stdout = io.StringIO()
print("swallowed")
crt.Window.Caption = "after stdout replace"
sys.stdout = sys.__stdout__
print("restored-stdout")
sys.stdout = open(os.devnull, "w")
sys.stdin = None
print("devnull")
crt.Window.Caption = "still works"
os.system("printf child-no-newline")
subprocess.run(["sh", "-c", "echo from-child; cat; echo after-cat"])
print("{\"op\": \"done\"}")
sys.stdout = sys.__stdout__
print("final")
print("rows=%d" % crt.Screen.Rows)
`, big)
	if info.State != StateDone {
		t.Fatalf("%+v", info.Error)
	}
	if len(info.Output) > maxOutput {
		t.Errorf("output %d bytes exceeds the cap", len(info.Output))
	}
	h.UI.(*fakeUI).mu.Lock()
	cap0 := h.UI.(*fakeUI).captions[0]
	h.UI.(*fakeUI).mu.Unlock()
	if cap0 != "still works" {
		t.Errorf("caption %q", cap0)
	}
	// the output is capped, so look at the end of a smaller run
	info = runScript(t, h, cn, "py", `
import sys, os, subprocess
sys.stdout = open(os.devnull, "w")
os.system("printf child-no-newline")
subprocess.run(["sh", "-c", "echo from-child; cat; echo after-cat"])
sys.stdout = sys.__stdout__
print("{\"op\": \"done\"}")
print("final=%d" % crt.Screen.Rows)
`)
	if info.State != StateDone {
		t.Fatalf("%+v", info)
	}
	for _, w := range []string{"child-no-newline", "from-child", "after-cat", "final=24"} {
		if !strings.Contains(info.Output, w) {
			t.Errorf("output lacks %q: %q", w, info.Output)
		}
	}
}

func TestPythonLeavesNothingBehind(t *testing.T) {
	needPython(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	h, cn, _ := setup(t)
	left := func(what string) {
		t.Helper()
		if m, _ := filepath.Glob(filepath.Join(tmp, "hawser-py*")); len(m) != 0 {
			t.Errorf("%s: temp files left: %v", what, m)
		}
		eventually(t, what+": python gone", func() bool { return len(pgrepShim(t, tmp)) == 0 })
	}

	// normal completion, and a failing script
	runScript(t, h, cn, "py", "print(1)")
	left("completion")
	runScript(t, h, cn, "py", "raise RuntimeError('x')")
	left("error")
	runScript(t, h, cn, "py", "crt.Quit()")
	left("quit")

	// a non-daemon thread must not keep the run alive
	start := time.Now()
	info := runScript(t, h, cn, "py", "import threading, time\nthreading.Thread(target=lambda: time.sleep(600)).start()\nprint('main done')\n")
	if info.State != StateDone || time.Since(start) > 5*time.Second {
		t.Errorf("thread: %+v after %v", info, time.Since(start))
	}
	left("thread")

	// Stop while waiting in a crt call, and while sleeping in Python
	for name, src := range map[string]string{
		"crt wait":  "print('go')\ncrt.Screen.WaitForString('never')\n",
		"py sleep":  "import time\nprint('go')\ntime.sleep(600)\n",
		"busy loop": "print('go')\nwhile True:\n    pass\n",
		"child":     "import subprocess\nprint('go')\nsubprocess.run(['sleep', '600'])\n",
	} {
		r, err := h.Start(RunRequest{Conn: cn.ID, Lang: "py", Source: src})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, name+" started", func() bool { return strings.Contains(r.Info().Output, "go") })
		if got := len(pgrepShim(t, tmp)); got != 1 {
			t.Errorf("%s: %d python processes running, want 1", name, got)
		}
		h.Stop(r.Info().ID)
		select {
		case <-r.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Stop did not end the run", name)
		}
		if st := r.Info().State; st != StateStopped {
			t.Errorf("%s: state %s", name, st)
		}
		left(name)
	}
	_ = exec.Command("pkill", "-f", "sleep 600").Run()
}

// ------------------------------------------------------------ concurrency

func TestTwoScriptsTwoTabs(t *testing.T) {
	needPython(t)
	h, cn1, ui := setup(t)
	cn2 := openShell(t, h.Mgr, ui.sink, "")
	loop := func(tag string) (py, js string) {
		py = fmt.Sprintf(`
s = crt.Screen; s.Synchronous = True
total = 0
for i in range(25):
    s.Send("echo %[1]s$((%[2]d+%%d))x\r" %% i)
    if not s.WaitForString("%[1]s%%d" %% (%[2]d+i) + "x", 10):
        raise RuntimeError("lost output at %%d" %% i)
    total += i
    s.Send("echo other-$((1+1))\r")
print("total=%%d" %% total)
`, tag, 1000)
		js = fmt.Sprintf(`
var s = crt.Screen; s.Synchronous = true;
var total = 0;
for (var i = 0; i < 25; i++) {
  s.Send("echo %[1]s$((%[2]d+" + i + "))x\r");
  if (!s.WaitForString("%[1]s" + (%[2]d + i) + "x", 10)) throw new Error("lost output at " + i);
  total += i;
  s.Send("echo other-$((1+1))\r");
}
print("total=" + total);
`, tag, 2000)
		return
	}
	pySrc, _ := loop("pa")
	_, jsSrc := loop("jb")
	r1, err := h.Start(RunRequest{Conn: cn1.ID, Lang: "py", Source: pySrc})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := h.Start(RunRequest{Conn: cn2.ID, Lang: "js", Source: jsSrc})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Run{r1, r2} {
		info := r.Wait()
		if info.State != StateDone || !strings.Contains(info.Output, "total=300") {
			t.Errorf("%+v", info)
		}
	}
	// neither tab received the other's commands
	if s := cn1.TextSlice(0, cn1.TextEnd()); strings.Contains(s, "jb2") {
		t.Error("tab 1 saw tab 2's commands")
	}
	if s := cn2.TextSlice(0, cn2.TextEnd()); strings.Contains(s, "pa1") {
		t.Error("tab 2 saw tab 1's commands")
	}
}

func TestSecondScriptOnBusyTabRefused(t *testing.T) {
	h, cn, ui := setup(t)
	r, err := h.Start(RunRequest{Conn: cn.ID, Lang: "js", Source: "print('up'); crt.Screen.WaitForString('never');"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "first script", func() bool { return strings.Contains(r.Info().Output, "up") })
	_, err = h.Start(RunRequest{Conn: cn.ID, Lang: "js", Source: "print('second')"})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start: %v", err)
	}
	if n := len(h.Runs()); n != 1 {
		t.Errorf("a refused start left %d runs", n)
	}
	// another tab is not affected
	cn2 := openShell(t, h.Mgr, ui.sink, "")
	if info := runScript(t, h, cn2, "js", "print('other tab ok')"); info.State != StateDone {
		t.Errorf("%+v", info)
	}
	// once the first script ends a new one may start
	h.Stop(r.Info().ID)
	r.Wait()
	if info := runScript(t, h, cn, "js", "print('now ok')"); info.State != StateDone {
		t.Errorf("%+v", info)
	}
}

func TestStopAllAndTabClose(t *testing.T) {
	needPython(t)
	h, cn1, ui := setup(t)
	cn2 := openShell(t, h.Mgr, ui.sink, "")
	cn3 := openShell(t, h.Mgr, ui.sink, "")
	src := "print('up')\nwhile True:\n    crt.Screen.WaitForString('never', 1)\n"
	var runs []*Run
	for _, c := range []*conn.Conn{cn1, cn2, cn3} {
		r, err := h.Start(RunRequest{Conn: c.ID, Lang: "py", Source: src})
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, r)
	}
	for _, r := range runs {
		r := r
		eventually(t, "start", func() bool { return strings.Contains(r.Info().Output, "up") })
	}
	// closing a tab ends the script that runs in it
	h.Mgr.Close(cn3.ID)
	select {
	case <-runs[2].Done():
	case <-time.After(3 * time.Second):
		t.Fatal("script survived the closing of its tab")
	}
	if st := runs[2].Info().State; st != StateStopped {
		t.Errorf("state %s", st)
	}
	start := time.Now()
	h.StopAll()
	for i, r := range runs[:2] {
		select {
		case <-r.Done():
		default:
			t.Errorf("run %d still going after StopAll", i)
		}
		if st := r.Info().State; st != StateStopped {
			t.Errorf("run %d: state %s", i, st)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("StopAll took %v", d)
	}
	// the host is reusable afterwards
	if info := runScript(t, h, cn1, "js", "print('again')"); info.State != StateDone {
		t.Errorf("%+v", info)
	}
	h.StopAll() // nothing running: returns at once
}

// blockedUI answers every request only when the run is stopped (a window that
// never answers).
type blockedUI struct{ *fakeUI }

func (blockedUI) MessageBox(ctx context.Context, _, _ string, _ int) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}
func (blockedUI) Prompt(ctx context.Context, _, _, _ string, _ bool) (string, bool, error) {
	<-ctx.Done()
	return "", false, ctx.Err()
}
func (blockedUI) Tabs(ctx context.Context) ([]uint32, uint32, error) {
	<-ctx.Done()
	return nil, 0, ctx.Err()
}
func (blockedUI) ScreenInfo(ctx context.Context, _ uint32) (int, int, int, int, string, error) {
	<-ctx.Done()
	return 0, 0, 0, 0, "", ctx.Err()
}
func (blockedUI) Clipboard(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestStopWhileUIBlocks(t *testing.T) {
	calls := map[string]string{
		"python":     "print('go')\ncrt.Dialog.MessageBox('x')\n",
		"javascript": "print('go'); crt.Screen.Rows;",
		"vbscript":   "WScript.Echo \"go\"\nx = crt.GetTabCount()\n",
	}
	for lang, src := range calls {
		t.Run(lang, func(t *testing.T) {
			if lang == "python" {
				needPython(t)
			}
			h, cn, ui := setup(t)
			h.UI = blockedUI{ui}
			r, err := h.Start(RunRequest{Conn: cn.ID, Lang: lang, Source: src})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "start", func() bool { return strings.Contains(r.Info().Output, "go") })
			time.Sleep(100 * time.Millisecond)
			if r.Info().State != StateRunning {
				t.Fatalf("%+v", r.Info())
			}
			h.Stop(r.Info().ID)
			select {
			case <-r.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("the run did not end although its UI request was cancelled")
			}
			if st := r.Info().State; st != StateStopped {
				t.Errorf("state %s", st)
			}
		})
	}
}

// ---------------------------------------------- JS / VBScript semantics

func TestJSQuitInsideTryAndVMReuse(t *testing.T) {
	h, cn, _ := setup(t)
	info := runScript(t, h, cn, "js", `
try { crt.Quit(); print("after-quit"); } catch (e) { print("caught"); } finally { print("finally"); }
print("after-try");
`)
	if info.State != StateDone || strings.Contains(info.Output, "caught") || strings.Contains(info.Output, "after") {
		t.Errorf("%+v", info)
	}
	// errors from the host are catchable and the VM stays usable
	info = runScript(t, h, cn, "js", `
for (var i = 0; i < 3; i++) { try { crt.GetTab(50); } catch (e) { print("e" + i + ":" + (e.message.indexOf("out of range") >= 0)); } }
crt.Screen.Send("echo js$((2+2))x\r");
print("ok=" + crt.Screen.WaitForString("js4x", 5));
`)
	if info.State != StateDone || !strings.Contains(info.Output, "e2:true") || !strings.Contains(info.Output, "ok=true") {
		t.Errorf("%+v", info)
	}
	// a script error carries the script name and line
	info = runScript(t, h, cn, "js", "var a = 1;\nvar b = a.nothing.deeper;\n")
	if info.State != StateError || !strings.Contains(info.Error, ":2") {
		t.Errorf("%+v", info)
	}
}

func TestVBScriptOnErrorResumeNextHostErrors(t *testing.T) {
	h, cn, _ := setup(t)
	info := runScript(t, h, cn, "vbs", `
On Error Resume Next
Set t = crt.GetTab(99)
WScript.Echo "n=" & (Err.Number <> 0) & " d=" & Err.Description
Err.Clear
crt.Session.NoSuchMember
WScript.Echo "member=" & (Err.Number <> 0)
Err.Clear
crt.Screen.WaitForString
WScript.Echo "args=" & (Err.Number <> 0)
Err.Clear
x = crt.Arguments.GetArg(5)
WScript.Echo "after=" & (Err.Number <> 0) & " x=[" & x & "]"
Err.Clear
WScript.Echo "ok=" & crt.GetTabCount()
On Error GoTo 0
WScript.Echo "strict"
crt.GetTab 99
WScript.Echo "unreachable"
`)
	if info.State != StateError || !strings.Contains(info.Error, "out of range") {
		t.Fatalf("%+v", info)
	}
	for _, w := range []string{"n=True d=", "out of range", "member=True", "args=True", "after=True x=[]", "ok=1", "strict"} {
		if !strings.Contains(info.Output, w) {
			t.Errorf("output lacks %q: %q", w, info.Output)
		}
	}
	if strings.Contains(info.Output, "unreachable") {
		t.Error("error after On Error GoTo 0 did not stop the script")
	}
	// Quit is not an error and cannot be swallowed by Resume Next
	info = runScript(t, h, cn, "vbs", "On Error Resume Next\ncrt.Quit\nWScript.Echo \"after-quit\"\n")
	if info.State != StateDone || strings.Contains(info.Output, "after-quit") {
		t.Errorf("%+v", info)
	}
}

// ---------------------------------------------------------------- examples

func TestExampleScripts(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "scripts")
	t.Run("wait_and_send.js", func(t *testing.T) {
		h, cn, ui := setup(t)
		info := runScriptFile(t, h, cn, filepath.Join(dir, "wait_and_send.js"))
		if info.State != StateDone {
			t.Fatalf("%+v", info)
		}
		ui.mu.Lock()
		defer ui.mu.Unlock()
		if len(ui.boxes) != 1 || !strings.Contains(ui.boxes[0], "Last lines") {
			t.Errorf("boxes %q", ui.boxes)
		}
	})
	t.Run("login.py", func(t *testing.T) {
		needPython(t)
		h, cn, _ := setup(t)
		info := runScriptFile(t, h, cn, filepath.Join(dir, "login.py"), "alice", "secret")
		if info.State != StateDone || !strings.Contains(info.Output, "Done: already logged in") {
			t.Fatalf("%+v", info)
		}
	})
	t.Run("login.py asks for a login", func(t *testing.T) {
		needPython(t)
		// a tiny "login" program: prints a prompt, reads the name, then the password
		h, cn, _ := setup(t)
		// (the prompts are assembled so that the echo of this line does not contain them)
		cn.Send([]byte("while :; do printf 'log%s: ' in; read u; [ -n \"$u\" ] && break; done; printf 'Pass%s: ' word; read p; echo \"welcome $u\"\r"), 0, 0)
		info := runScriptFile(t, h, cn, filepath.Join(dir, "login.py"), "alice", "secret")
		if info.State != StateDone || !strings.Contains(info.Output, "Done: logged in as alice") {
			t.Fatalf("%+v", info)
		}
	})
	t.Run("hostlist.vbs", func(t *testing.T) {
		h, cn, _ := setup(t)
		if err := h.Mgr.Store.Save("Lab/sh", shellOverrides("S$ ")); err != nil {
			t.Fatal(err)
		}
		bad := strconv.Itoa(closedPort(t))
		info := runScriptFile(t, h, cn, filepath.Join(dir, "hostlist.vbs"), `/S "Lab/sh"`, "/RAW 127.0.0.1 "+bad)
		if info.State != StateDone {
			t.Fatalf("%+v", info)
		}
		out := strings.ReplaceAll(info.Output, "\r", "")
		if !strings.Contains(out, `/S "Lab/sh": ok`) || !strings.Contains(out, "127.0.0.1 "+bad+": ") || strings.Contains(out, bad+": ok") {
			t.Errorf("output:\n%s", out)
		}
		if n := len(h.Mgr.List()); n != 3 { // script tab, the stored session, and the failed attempt's tab
			t.Errorf("%d tabs", n)
		}
	})
}

func runScriptFile(t *testing.T, h *Host, cn *conn.Conn, path string, args ...string) RunInfo {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	r, err := h.Start(RunRequest{Conn: cn.ID, Path: path, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
	case <-time.After(60 * time.Second):
		h.StopAll()
		t.Fatalf("example did not finish; output: %s", r.Info().Output)
	}
	return r.Info()
}
