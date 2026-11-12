package script

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
)

// lsrc holds one script per language (empty = not tested in that language).
type lsrc struct{ py, js, vbs string }

type fixture struct {
	h  *Host
	cn *conn.Conn
	ui *fakeUI
}

// runLangs runs each script on a fresh host. prep may create stored sessions
// or extra tabs and returns the script arguments; check inspects the result.
func runLangs(t *testing.T, l lsrc, prep func(*testing.T, fixture) []string, check func(*testing.T, fixture, string), want ...string) {
	t.Helper()
	for _, c := range []struct{ lang, src string }{{"python", l.py}, {"javascript", l.js}, {"vbscript", l.vbs}} {
		lang, src := c.lang, c.src
		if src == "" {
			continue
		}
		t.Run(lang, func(t *testing.T) {
			if lang == "python" {
				needPython(t)
			}
			h, cn, ui := setup(t)
			f := fixture{h, cn, ui}
			var args []string
			if prep != nil {
				args = prep(t, f)
			}
			info := runScript(t, h, cn, lang, src, args...)
			out := strings.ReplaceAll(info.Output, "\r", "")
			if info.State != StateDone {
				t.Fatalf("state %s error %q output:\n%s", info.State, info.Error, out)
			}
			for _, w := range want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in output:\n%s", w, out)
				}
			}
			if check != nil {
				check(t, f, out)
			}
		})
	}
}

func saveShell(t *testing.T, f fixture, name, prompt string) {
	t.Helper()
	if err := f.h.Mgr.Store.Save(name, shellOverrides(prompt)); err != nil {
		t.Fatal(err)
	}
}

func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

const vbB = `
Function B(x)
  If x Then
    B = 1
  Else
    B = 0
  End If
End Function
`

// ------------------------------------------------------- Session.Connect

func TestSessionConnectNoArg(t *testing.T) {
	runLangs(t, lsrc{
		py: `
s = crt.Screen
s.Synchronous = True
s.Send("exit\r")
print("wait=%d" % int(s.WaitForString("never-x", 10)))
for i in range(100):
    if not crt.Session.Connected:
        break
    crt.Sleep(50)
print("down=%d" % int(not crt.Session.Connected))
crt.Session.Connect()
print("up=%d" % int(crt.Session.Connected))
s.Send("echo a$((1+2))b\r")
print("echo=%d" % int(s.WaitForString("a3b", 10)))
`,
		js: `
var s = crt.Screen; s.Synchronous = true;
s.Send("exit\r");
print("wait=" + (s.WaitForString("never-x", 10) ? 1 : 0));
for (var i = 0; i < 100 && crt.Session.Connected; i++) crt.Sleep(50);
print("down=" + (crt.Session.Connected ? 0 : 1));
crt.Session.Connect();
print("up=" + (crt.Session.Connected ? 1 : 0));
s.Send("echo a$((1+2))b\r");
print("echo=" + (s.WaitForString("a3b", 10) ? 1 : 0));
`,
		vbs: vbB + `
Set s = crt.Screen
s.Synchronous = True
s.Send "exit" & vbCr
WScript.Echo "wait=" & B(s.WaitForString("never-x", 10))
For i = 1 To 100
  If Not crt.Session.Connected Then Exit For
  crt.Sleep 50
Next
WScript.Echo "down=" & B(Not crt.Session.Connected)
crt.Session.Connect
WScript.Echo "up=" & B(crt.Session.Connected)
s.Send "echo a$((1+2))b" & vbCr
WScript.Echo "echo=" & B(s.WaitForString("a3b", 10))
`}, nil, nil, "wait=0", "down=1", "up=1", "echo=1")
}

func TestSessionConnectStored(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string { saveShell(t, f, "Lab/sh", "S$ "); return nil }
	runLangs(t, lsrc{
		py: `
crt.Session.Disconnect()
print("d=%d" % int(crt.Session.Connected))
crt.Session.Connect('/S "Lab/sh"')
print("path=" + crt.Session.Path)
s = crt.Screen; s.Synchronous = True
s.Send('echo "p${PS1}q"\r')
print("ok=%d" % int(s.WaitForString("pS$ q", 10)))
`,
		js: `
crt.Session.Disconnect();
print("d=" + (crt.Session.Connected ? 1 : 0));
crt.Session.Connect('/S "Lab/sh"');
print("path=" + crt.Session.Path);
var s = crt.Screen; s.Synchronous = true;
s.Send('echo "p${PS1}q"\r');
print("ok=" + (s.WaitForString("pS$ q", 10) ? 1 : 0));
`,
		vbs: vbB + `
crt.Session.Disconnect
WScript.Echo "d=" & B(crt.Session.Connected)
crt.Session.Connect "/S ""Lab/sh"""
WScript.Echo "path=" & crt.Session.Path
Set s = crt.Screen
s.Synchronous = True
s.Send "echo ""p${PS1}q""" & vbCr
WScript.Echo "ok=" & B(s.WaitForString("pS$ q", 10))
`}, prep, nil, "d=0", "path=Lab/sh", "ok=1")
}

func TestSessionConnectFailure(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string {
		err := f.h.Mgr.Store.Save("Bad", map[string]any{"protocol": "raw", "host": "127.0.0.1", "port": closedPort(t)})
		if err != nil {
			t.Fatal(err)
		}
		return nil
	}
	runLangs(t, lsrc{
		py: `
crt.Session.Disconnect()
try:
    crt.Session.Connect('/S "Bad"')
    print("raised=0")
except RuntimeError as e:
    print("raised=1 " + str(e))
print("code=%d" % crt.GetLastError())
print("msg=" + crt.GetLastErrorMessage())
print("connected=%d" % int(crt.Session.Connected))
crt.ClearLastError()
print("cleared=%d" % crt.GetLastError())
try:
    crt.Session.ConnectInTab('/S "Bad"')
except RuntimeError as e:
    print("tab-raised")
`,
		js: `
crt.Session.Disconnect();
try { crt.Session.Connect('/S "Bad"'); print("raised=0"); } catch (e) { print("raised=1 " + e.message); }
print("code=" + crt.GetLastError());
print("msg=" + crt.GetLastErrorMessage());
print("connected=" + (crt.Session.Connected ? 1 : 0));
crt.ClearLastError();
print("cleared=" + crt.GetLastError());
try { crt.Session.ConnectInTab('/S "Bad"'); } catch (e) { print("tab-raised"); }
`,
		vbs: vbB + `
crt.Session.Disconnect
On Error Resume Next
crt.Session.Connect "/S ""Bad"""
If Err.Number <> 0 Then WScript.Echo "raised=1 " & Err.Description
Err.Clear
On Error GoTo 0
WScript.Echo "code=" & crt.GetLastError
WScript.Echo "msg=" & crt.GetLastErrorMessage
WScript.Echo "connected=" & B(crt.Session.Connected)
crt.ClearLastError
WScript.Echo "cleared=" & crt.GetLastError
On Error Resume Next
crt.Session.ConnectInTab "/S ""Bad"""
If Err.Number <> 0 Then WScript.Echo "tab-raised"
`}, prep, nil, "raised=1", "code=1", "refused", "connected=0", "cleared=0", "tab-raised")
}

func TestSessionConnectUnhandledFailure(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	if err := h.Mgr.Store.Save("Bad", map[string]any{"protocol": "raw", "host": "127.0.0.1", "port": closedPort(t)}); err != nil {
		t.Fatal(err)
	}
	info := runScript(t, h, cn, "py", "crt.Session.Disconnect()\ncrt.Session.Connect('/S \"Bad\"')\nprint('unreachable')\n")
	if info.State != StateError || !strings.Contains(info.Error, "refused") || strings.Contains(info.Output, "unreachable") {
		t.Fatalf("%+v", info)
	}
	// a malformed or unknown target is an error too, not a hang
	info = runScript(t, h, cn, "py", "crt.Session.Connect('/S \"No/Such\"')\n")
	if info.State != StateError {
		t.Fatalf("%+v", info)
	}
	info = runScript(t, h, cn, "py", "crt.Session.Connect('/SSH2')\n")
	if info.State != StateError || !strings.Contains(info.Error, "no host") {
		t.Fatalf("%+v", info)
	}
}

// ------------------------------------------------------------------- Tabs

func TestConnectInTab(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string { saveShell(t, f, "Lab/sh", "S$ "); return nil }
	runLangs(t, lsrc{
		py: `
tab = crt.Session.ConnectInTab('/S "Lab/sh"')
ts = tab.Screen; ts.Synchronous = True
ts.Send("echo t$((4+4))x\r")
print("tabout=%d" % int(ts.WaitForString("t8x", 10)))
print("scriptsaw=%d" % int(crt.Screen.WaitForString("t8x", 1)))
print("count=%d index=%d" % (crt.GetTabCount(), tab.Index))
print("tabconn=%d path=%s" % (int(tab.Session.Connected), tab.Session.Path))
`,
		js: `
var tab = crt.Session.ConnectInTab('/S "Lab/sh"');
var ts = tab.Screen; ts.Synchronous = true;
ts.Send("echo t$((4+4))x\r");
print("tabout=" + (ts.WaitForString("t8x", 10) ? 1 : 0));
print("scriptsaw=" + (crt.Screen.WaitForString("t8x", 1) ? 1 : 0));
print("count=" + crt.GetTabCount() + " index=" + tab.Index);
print("tabconn=" + (tab.Session.Connected ? 1 : 0) + " path=" + tab.Session.Path);
`,
		vbs: vbB + `
Set tab = crt.Session.ConnectInTab("/S ""Lab/sh""")
Set ts = tab.Screen
ts.Synchronous = True
ts.Send "echo t$((4+4))x" & vbCr
WScript.Echo "tabout=" & B(ts.WaitForString("t8x", 10))
WScript.Echo "scriptsaw=" & B(crt.Screen.WaitForString("t8x", 1))
WScript.Echo "count=" & crt.GetTabCount() & " index=" & tab.Index
WScript.Echo "tabconn=" & B(tab.Session.Connected) & " path=" & tab.Session.Path
`}, prep, func(t *testing.T, f fixture, out string) {
		if n := len(f.h.Mgr.List()); n != 2 {
			t.Errorf("%d tabs, want 2", n)
		}
	}, "tabout=1", "scriptsaw=0", "count=2 index=2", "tabconn=1 path=Lab/sh")
}

const tabAPI = `
tab = crt.Session.ConnectInTab('/S "Lab/sh"')
tab.Caption = "Cap1"
print("cap=" + tab.Caption + "/" + crt.GetTab(2).Caption)
print("active=%d" % crt.GetActiveTab().Index)
tab.Activate()
print("active2=%d script=%d" % (crt.GetActiveTab().Index, crt.GetScriptTab().Index))
cl = tab.Clone()
print("clone=%d count=%d" % (cl.Index, crt.GetTabCount()))
cs = cl.Screen; cs.Synchronous = True
cs.Send("echo c$((4+5))x\r")
print("cloneout=%d" % int(cs.WaitForString("c9x", 10)))
ts = tab.Screen; ts.Synchronous = True
print("cloneleak=%d" % int(ts.WaitForString("c9x", 1)))
cl.Close()
print("afterclone=%d" % crt.GetTabCount())
tab.Close()
print("afterclose=%d" % crt.GetTabCount())
try:
    crt.GetTab(2)
except RuntimeError:
    print("gone")
`

func TestTabAPI(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string { saveShell(t, f, "Lab/sh", "S$ "); return nil }
	js := `
var tab = crt.Session.ConnectInTab('/S "Lab/sh"');
tab.Caption = "Cap1";
print("cap=" + tab.Caption + "/" + crt.GetTab(2).Caption);
print("active=" + crt.GetActiveTab().Index);
tab.Activate();
print("active2=" + crt.GetActiveTab().Index + " script=" + crt.GetScriptTab().Index);
var cl = tab.Clone();
print("clone=" + cl.Index + " count=" + crt.GetTabCount());
var cs = cl.Screen; cs.Synchronous = true;
cs.Send("echo c$((4+5))x\r");
print("cloneout=" + (cs.WaitForString("c9x", 10) ? 1 : 0));
var ts = tab.Screen; ts.Synchronous = true;
print("cloneleak=" + (ts.WaitForString("c9x", 1) ? 1 : 0));
cl.Close();
print("afterclone=" + crt.GetTabCount());
tab.Close();
print("afterclose=" + crt.GetTabCount());
try { crt.GetTab(2); } catch (e) { print("gone"); }
`
	runLangs(t, lsrc{py: tabAPI, js: js}, prep, func(t *testing.T, f fixture, out string) {
		if n := len(f.h.Mgr.List()); n != 1 {
			t.Errorf("%d tabs left, want 1", n)
		}
		if f.ui.captions[f.cn.ID+1] != "Cap1" {
			t.Errorf("captions %v", f.ui.captions)
		}
	}, "cap=Cap1/Cap1", "active=1", "active2=2 script=1", "clone=3 count=3", "cloneout=1", "cloneleak=0", "afterclone=2", "afterclose=1", "gone")
}

// Tab.Clone of a non-SSH session opens an independent connection.
func TestTabCloneIsIndependent(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	info := runScript(t, h, cn, "py", `
cl = crt.GetScriptTab().Clone()
cs = cl.Screen; cs.Synchronous = True
cs.Send("echo $$\r")
crt.Screen.Send("echo $$\r")
print("alive=%d" % int(cl.Session.Connected))
cl.Session.Disconnect()
print("orig=%d clone=%d" % (int(crt.Session.Connected), int(cl.Session.Connected)))
`)
	if info.State != StateDone || !strings.Contains(info.Output, "alive=1") || !strings.Contains(info.Output, "orig=1 clone=0") {
		t.Fatalf("%+v", info)
	}
}

// ---------------------------------------------- Disconnect, Log, status

func TestSessionLogDisconnectStatus(t *testing.T) {
	var logPath string
	prep := func(t *testing.T, f fixture) []string {
		logPath = filepath.Join(t.TempDir(), "sess.log")
		return []string{logPath}
	}
	runLangs(t, lsrc{
		py: `
path = crt.Arguments.GetArg(0)
print("before=%d" % int(crt.Session.Logging))
crt.Session.LogFileName = path
crt.Session.Log(True)
print("logging=%d name=%s" % (int(crt.Session.Logging), crt.Session.LogFileName))
s = crt.Screen; s.Synchronous = True
s.Send("echo L$((9*9))x\r")
print("seen=%d" % int(s.WaitForString("L81x", 10)))
crt.Session.Log(False)
print("after=%d" % int(crt.Session.Logging))
crt.Session.SetStatusText("status-one")
crt.Session.Disconnect()
print("conn=%d" % int(crt.Session.Connected))
`,
		js: `
var path = crt.Arguments.GetArg(0);
print("before=" + (crt.Session.Logging ? 1 : 0));
crt.Session.LogFileName = path;
crt.Session.Log(true);
print("logging=" + (crt.Session.Logging ? 1 : 0) + " name=" + crt.Session.LogFileName);
var s = crt.Screen; s.Synchronous = true;
s.Send("echo L$((9*9))x\r");
print("seen=" + (s.WaitForString("L81x", 10) ? 1 : 0));
crt.Session.Log(false);
print("after=" + (crt.Session.Logging ? 1 : 0));
crt.Session.SetStatusText("status-one");
crt.Session.Disconnect();
print("conn=" + (crt.Session.Connected ? 1 : 0));
`,
		vbs: vbB + `
path = crt.Arguments.GetArg(0)
WScript.Echo "before=" & B(crt.Session.Logging)
crt.Session.LogFileName = path
crt.Session.Log True
WScript.Echo "logging=" & B(crt.Session.Logging) & " name=" & crt.Session.LogFileName
Set s = crt.Screen
s.Synchronous = True
s.Send "echo L$((9*9))x" & vbCr
WScript.Echo "seen=" & B(s.WaitForString("L81x", 10))
crt.Session.Log False
WScript.Echo "after=" & B(crt.Session.Logging)
crt.Session.SetStatusText "status-one"
crt.Session.Disconnect
WScript.Echo "conn=" & B(crt.Session.Connected)
`}, prep, func(t *testing.T, f fixture, out string) {
		if !strings.Contains(mustRead(t, logPath), "L81x") {
			t.Errorf("log file lacks the output: %q", mustRead(t, logPath))
		}
		f.ui.mu.Lock()
		defer f.ui.mu.Unlock()
		if len(f.ui.status) != 1 || f.ui.status[0] != "status-one" {
			t.Errorf("status %v", f.ui.status)
		}
	}, "before=0", "logging=1 name="+"", "seen=1", "after=0", "conn=0")
}

// ------------------------------------------------ SessionConfiguration

func TestSessionConfiguration(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string {
		err := f.h.Mgr.Store.Save("Net/r1", map[string]any{"protocol": "ssh2", "host": "h1", "username": "u1", "port": 2200})
		if err != nil {
			t.Fatal(err)
		}
		return nil
	}
	check := func(t *testing.T, f fixture, out string) {
		st := f.h.Mgr.Store
		for _, p := range []string{"Net/r1", "Copy/r2"} {
			ov, err := st.Overrides(p)
			if err != nil {
				t.Fatalf("%s: %v", p, err)
			}
			if ov["description"] != "Core router" || ov["port"] != float64(2201) || ov["protocol"] != "telnet" || ov["host"] != "h1" {
				t.Errorf("%s: %v", p, ov)
			}
			o, err := st.Session(p)
			if err != nil || o.Terminal.Scrollback != 1234 {
				t.Errorf("%s: scrollback %d %v", p, o.Terminal.Scrollback, err)
			}
		}
	}
	runLangs(t, lsrc{
		py: `
c = crt.OpenSessionConfiguration("Net/r1")
print("host=%s user=%s port=%s/%s proto=%s" % (c.GetOption("Hostname"), c.GetOption("Username"), c.GetOption("Port"), c.GetOption("[SSH2] Port"), c.GetOption("Protocol Name")))
print("emu=%s dotted=%s" % (c.GetOption("Emulation") != "", c.GetOption("terminal.emulation") == c.GetOption("emulation")))
c.SetOption("Description", "Core router")
c.SetOption("Port", "2201")
c.SetOption("Scrollback", 1234)
c.SetOption("Protocol Name", "Telnet")
print("desc=" + c.GetOption("description") + " sb=%s" % c.GetOption("terminal.scrollback"))
try:
    c.GetOption("no.such.option")
    print("unknown=ok")
except RuntimeError:
    print("unknown=raised")
c.Save()
c.Save("Copy/r2")
`,
		js: `
var c = crt.OpenSessionConfiguration("Net/r1");
print("host=" + c.GetOption("Hostname") + " user=" + c.GetOption("Username") + " port=" + c.GetOption("Port") + "/" + c.GetOption("[SSH2] Port") + " proto=" + c.GetOption("Protocol Name"));
print("emu=" + (c.GetOption("Emulation") != "") + " dotted=" + (c.GetOption("terminal.emulation") == c.GetOption("emulation")));
c.SetOption("Description", "Core router");
c.SetOption("Port", "2201");
c.SetOption("Scrollback", 1234);
c.SetOption("Protocol Name", "Telnet");
print("desc=" + c.GetOption("description") + " sb=" + c.GetOption("terminal.scrollback"));
try { c.GetOption("no.such.option"); print("unknown=ok"); } catch (e) { print("unknown=raised"); }
c.Save();
c.Save("Copy/r2");
`,
		vbs: `
Set c = crt.OpenSessionConfiguration("Net/r1")
WScript.Echo "host=" & c.GetOption("Hostname") & " user=" & c.GetOption("Username") & " port=" & c.GetOption("Port") & "/" & c.GetOption("[SSH2] Port") & " proto=" & c.GetOption("Protocol Name")
c.SetOption "Description", "Core router"
c.SetOption "Port", "2201"
c.SetOption "Scrollback", 1234
c.SetOption "Protocol Name", "Telnet"
WScript.Echo "desc=" & c.GetOption("description") & " sb=" & c.GetOption("terminal.scrollback")
On Error Resume Next
x = c.GetOption("no.such.option")
If Err.Number <> 0 Then WScript.Echo "unknown=raised"
Err.Clear
On Error GoTo 0
c.Save
c.Save "Copy/r2"
`}, prep, check, "host=h1 user=u1 port=2200/2200 proto=SSH2", "desc=Core router sb=1234", "unknown=raised")
}

func TestSessionConfigurationDefaultAndConnect(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	if err := h.Mgr.Store.Save("Lab/sh", shellOverrides("S$ ")); err != nil {
		t.Fatal(err)
	}
	info := runScript(t, h, cn, "py", `
c = crt.OpenSessionConfiguration("Lab/sh")
c.SetOption("local.env.PS1", "Q$ ")
tab = c.ConnectInTab()
ts = tab.Screen; ts.Synchronous = True
ts.Send('echo "p${PS1}q"\r')
print("ok=%d" % int(ts.WaitForString("pQ$ q", 10)))
d = crt.OpenSessionConfiguration("")
d.SetOption("Scrollback", 4321)
d.Save()
`)
	if info.State != StateDone || !strings.Contains(info.Output, "ok=1") {
		t.Fatalf("%+v", info)
	}
	// ConnectInTab does not save; the stored session is unchanged
	ov, _ := h.Mgr.Store.Overrides("Lab/sh")
	if env := ov["local"].(map[string]any)["env"].(map[string]any); env["PS1"] != "S$ " {
		t.Errorf("stored session modified: %v", ov)
	}
	if def, err := h.Mgr.Store.DefaultOverrides(); err != nil || toInt(def["terminal"].(map[string]any)["scrollback"]) != 4321 {
		t.Errorf("default %v %v", def, err)
	}
}

// ---------------------------------------------------------- CommandWindow

func TestCommandWindow(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string {
		openShell(t, f.h.Mgr, f.ui.sink, "")
		return nil
	}
	runLangs(t, lsrc{
		py: `
t2 = crt.GetTab(2).Screen; t2.Synchronous = True
s = crt.Screen; s.Synchronous = True
cw = crt.CommandWindow
cw.Text = "echo c$((5+5))x"
cw.Send()
print("own=%d" % int(s.WaitForString("c10x", 10)))
print("other=%d" % int(t2.WaitForString("c10x", 1)))
cw.SendToAllSessions = True
cw.Text = "echo d$((6+6))x"
cw.Send()
print("all1=%d all2=%d" % (int(s.WaitForString("d12x", 10)), int(t2.WaitForString("d12x", 10))))
`,
		js: `
var t2 = crt.GetTab(2).Screen; t2.Synchronous = true;
var s = crt.Screen; s.Synchronous = true;
var cw = crt.CommandWindow;
cw.Text = "echo c$((5+5))x";
cw.Send();
print("own=" + (s.WaitForString("c10x", 10) ? 1 : 0));
print("other=" + (t2.WaitForString("c10x", 1) ? 1 : 0));
cw.SendToAllSessions = true;
cw.Text = "echo d$((6+6))x";
cw.Send();
print("all1=" + (s.WaitForString("d12x", 10) ? 1 : 0) + " all2=" + (t2.WaitForString("d12x", 10) ? 1 : 0));
`,
		vbs: vbB + `
Set t2 = crt.GetTab(2).Screen
t2.Synchronous = True
Set s = crt.Screen
s.Synchronous = True
Set cw = crt.CommandWindow
cw.Text = "echo c$((5+5))x"
cw.Send
WScript.Echo "own=" & B(s.WaitForString("c10x", 10))
WScript.Echo "other=" & B(t2.WaitForString("c10x", 1))
cw.SendToAllSessions = True
cw.Text = "echo d$((6+6))x"
cw.Send
WScript.Echo "all1=" & B(s.WaitForString("d12x", 10)) & " all2=" & B(t2.WaitForString("d12x", 10))
`}, prep, nil, "own=1", "other=0", "all1=1 all2=1")
}

// ----------------------------------------------------------- FileTransfer

func TestSendAscii(t *testing.T) {
	var dst string
	prep := func(t *testing.T, f fixture) []string {
		dir := t.TempDir()
		src := filepath.Join(dir, "src.txt")
		dst = filepath.Join(dir, "dst.txt")
		if err := os.WriteFile(src, []byte("line one\nline two\r\nlast line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return []string{src, dst}
	}
	check := func(t *testing.T, f fixture, out string) {
		if got := mustRead(t, dst); got != "line one\nline two\nlast line\n" {
			t.Errorf("dst = %q", got)
		}
	}
	runLangs(t, lsrc{
		py: `
src, dst = crt.Arguments[0], crt.Arguments[1]
s = crt.Screen; s.Synchronous = True
s.Send("cat > " + dst + "\r")
print("sent=%s" % crt.FileTransfer.SendAscii(src))
s.Send("\x04")
s.Send("echo done$((1+1))x\r")
print("done=%d" % int(s.WaitForString("done2x", 10)))
try:
    crt.FileTransfer.SendAscii(dst + ".missing")
    print("missing=ok")
except RuntimeError:
    print("missing=raised")
`,
		js: `
var src = crt.Arguments[0], dst = crt.Arguments[1];
var s = crt.Screen; s.Synchronous = true;
s.Send("cat > " + dst + "\r");
print("sent=" + crt.FileTransfer.SendAscii(src));
s.Send("\x04");
s.Send("echo done$((1+1))x\r");
print("done=" + (s.WaitForString("done2x", 10) ? 1 : 0));
`,
		vbs: vbB + `
src = crt.Arguments(0)
dst = crt.Arguments(1)
Set s = crt.Screen
s.Synchronous = True
s.Send "cat > " & dst & vbCr
crt.FileTransfer.SendAscii src
s.Send Chr(4)
s.Send "echo done$((1+1))x" & vbCr
WScript.Echo "done=" & B(s.WaitForString("done2x", 10))
`}, prep, check, "done=1")
}

func needTool(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s is not installed", n)
		}
	}
}

func TestZmodemUploadQueue(t *testing.T) {
	needPython(t)
	needTool(t, "rz")
	h, cn, _ := setup(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "cleared.txt"), filepath.Join(dir, "kept.txt")
	dst := filepath.Join(dir, "dst")
	_ = os.Mkdir(dst, 0o700)
	_ = os.WriteFile(a, []byte("should never arrive"), 0o600)
	payload := strings.Repeat("zmodem payload\n", 500)
	_ = os.WriteFile(b, []byte(payload), 0o600)
	info := runScript(t, h, cn, "py", `
a, b, dst = crt.Arguments[0], crt.Arguments[1], crt.Arguments[2]
ft = crt.FileTransfer
ft.ZmodemUploadAddFile(a)
ft.ClearZmodemUploadList()
ft.ZmodemUploadAddFile(b)
s = crt.Screen; s.Synchronous = True
s.Send("cd " + dst + " && rz; echo rzend$((5+5))\r")
print("end=%d" % int(s.WaitForString("rzend10", 30)))
`, a, b, dst)
	if info.State != StateDone || !strings.Contains(info.Output, "end=1") {
		t.Fatalf("%+v", info)
	}
	if got := mustRead(t, filepath.Join(dst, "kept.txt")); got != payload {
		t.Errorf("received %d bytes, want %d", len(got), len(payload))
	}
	if _, err := os.Stat(filepath.Join(dst, "cleared.txt")); err == nil {
		t.Error("the cleared file was sent")
	}
}

func TestXmodem(t *testing.T) {
	needPython(t)
	needTool(t, "rx", "sx")
	h, cn, _ := setup(t)
	dir := t.TempDir()
	src, up, down := filepath.Join(dir, "src.bin"), filepath.Join(dir, "up.bin"), filepath.Join(dir, "down.bin")
	payload := strings.Repeat("0123456789abcdef", 40) // 640 bytes: five blocks
	_ = os.WriteFile(src, []byte(payload), 0o600)
	info := runScript(t, h, cn, "py", `
src, up, down = crt.Arguments[0], crt.Arguments[1], crt.Arguments[2]
s = crt.Screen; s.Synchronous = True
s.Send("rx " + up + "\r")
print("up=%s" % crt.FileTransfer.SendXmodem(src))
s.Send("sx -X " + src + "\r")
print("down=%s" % crt.FileTransfer.ReceiveXmodem(down))
s.Send("echo xend$((5+5))\r")
print("end=%d" % int(s.WaitForString("xend10", 30)))
`, src, up, down)
	if info.State != StateDone || !strings.Contains(info.Output, "end=1") || !strings.Contains(info.Output, "up=True") || !strings.Contains(info.Output, "down=True") {
		t.Fatalf("%+v", info)
	}
	for _, p := range []string{up, down} {
		if got := mustRead(t, p); !strings.HasPrefix(got, payload) {
			t.Errorf("%s: %d bytes, content differs", p, len(got))
		}
	}
	// failures are reported, not swallowed
	info = runScript(t, h, cn, "py", "crt.FileTransfer.SendXmodem('/nonexistent/file')\n")
	if info.State != StateError {
		t.Errorf("missing file: %+v", info)
	}
}

// ------------------------------------------- Clipboard, Window, Arguments

func TestClipboardWindowArgumentsVersion(t *testing.T) {
	prep := func(t *testing.T, f fixture) []string {
		f.ui.clip = "initial-é"
		return []string{"one", "two words", "é中"}
	}
	check := func(t *testing.T, f fixture, out string) {
		f.ui.mu.Lock()
		defer f.ui.mu.Unlock()
		if f.ui.clip != "new clip é中\nline2" || f.ui.captions[0] != "My Window" {
			t.Errorf("clip %q captions %v", f.ui.clip, f.ui.captions)
		}
	}
	runLangs(t, lsrc{
		py: `
print("clip0=" + crt.Clipboard.Text)
crt.Clipboard.Text = "new clip é中\nline2"
print("clip1=" + crt.Clipboard.Text.replace("\n", "|"))
crt.Window.Caption = "My Window"
print("win=" + crt.Window.Caption)
print("args=%d %s %s %s %s %d" % (crt.Arguments.Count, crt.Arguments.GetArg(0), crt.Arguments[1], crt.Arguments(2), crt.arguments.getarg(1), len(crt.Arguments)))
print("iter=" + ",".join(crt.Arguments))
try:
    crt.Arguments.GetArg(3)
except RuntimeError:
    print("range=raised")
print("ver=%s" % (crt.Version != ""))
print("script=[" + crt.ScriptFullName + "]")
`,
		js: `
print("clip0=" + crt.Clipboard.Text);
crt.Clipboard.Text = "new clip é中\nline2";
print("clip1=" + crt.Clipboard.Text.replace("\n", "|"));
crt.Window.Caption = "My Window";
print("win=" + crt.Window.Caption);
print("args=" + crt.Arguments.Count + " " + crt.Arguments.GetArg(0) + " " + crt.Arguments[1] + " " + crt.Arguments[2] + " " + crt.arguments.getArg(1) + " " + crt.Arguments.length);
try { crt.Arguments.GetArg(3); } catch (e) { print("range=raised"); }
print("ver=" + (crt.Version != ""));
print("script=[" + crt.ScriptFullName + "]");
`,
		vbs: `
WScript.Echo "clip0=" & crt.Clipboard.Text
crt.Clipboard.Text = "new clip é中" & vbLf & "line2"
WScript.Echo "clip1=" & Replace(crt.Clipboard.Text, vbLf, "|")
crt.Window.Caption = "My Window"
WScript.Echo "win=" & crt.Window.Caption
WScript.Echo "args=" & crt.Arguments.Count & " " & crt.Arguments.GetArg(0) & " " & crt.Arguments(1) & " " & crt.Arguments(2)
On Error Resume Next
x = crt.Arguments.GetArg(3)
If Err.Number <> 0 Then WScript.Echo "range=raised"
On Error GoTo 0
WScript.Echo "ver=" & (crt.Version <> "")
WScript.Echo "script=[" & crt.ScriptFullName & "]"
`}, prep, check, "clip0=initial-é", "clip1=new clip é中|line2", "win=My Window", "range=raised", "script=[]")
	// the version is the package constant
	needPython(t)
	h, cn, _ := setup(t)
	if info := runScript(t, h, cn, "py", "print(crt.Version)"); strings.TrimSpace(info.Output) != Version {
		t.Errorf("version %q", info.Output)
	}
}

func TestScriptFullName(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"hello.py":  "print('n=' + crt.ScriptFullName)\n",
		"hello.js":  "print('n=' + crt.ScriptFullName);\n",
		"hello.vbs": "WScript.Echo \"n=\" & crt.ScriptFullName\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			if strings.HasSuffix(name, ".py") {
				needPython(t)
			}
			h, cn, _ := setup(t)
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			// the source is read from the path when none is given
			r, err := h.Start(RunRequest{Conn: cn.ID, Path: p})
			if err != nil {
				t.Fatal(err)
			}
			info := r.Wait()
			if info.State != StateDone || !strings.Contains(info.Output, "n="+p) {
				t.Fatalf("%+v", info)
			}
		})
	}
	h, cn, _ := setup(t)
	if _, err := h.Start(RunRequest{Conn: cn.ID, Path: filepath.Join(dir, "absent.py")}); err == nil {
		t.Error("missing script file accepted")
	}
	if _, err := h.Start(RunRequest{Conn: 9999, Source: "x"}); err == nil {
		t.Error("unknown connection accepted")
	}
	if _, err := h.Start(RunRequest{Conn: cn.ID}); err == nil {
		t.Error("empty request accepted")
	}
}
