package script

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

type ackSink struct {
	mgr *conn.Manager
}

func (s *ackSink) Data(id uint32, p []byte) {
	if c := s.mgr.Get(id); c != nil {
		c.Ack(int64(len(p)))
	}
}
func (s *ackSink) Event(string, any) {}
func (s *ackSink) Prompt(context.Context, uint32, conn.PromptRequest) (conn.PromptResponse, error) {
	return conn.PromptResponse{}, conn.ErrCancelled
}

type fakeUI struct {
	mu       sync.Mutex
	mgr      *conn.Manager
	sink     conn.Sink
	boxes    []string
	captions map[uint32]string
	clip     string
	cursor   int
	active   uint32
	status   []string
}

func (u *fakeUI) MessageBox(_ context.Context, msg, title string, buttons int) (int, error) {
	u.mu.Lock()
	u.boxes = append(u.boxes, msg+"|"+title)
	u.mu.Unlock()
	return 6, nil
}
func (u *fakeUI) Prompt(_ context.Context, msg, title, def string, pw bool) (string, bool, error) {
	return "bob", true, nil
}
func (u *fakeUI) FileOpen(context.Context, string, string, string, string) (string, error) {
	return "/tmp/x.txt", nil
}
func (u *fakeUI) ScreenText(_ context.Context, c uint32, r1, c1, r2, c2 int) (string, error) {
	return "abc\ndef", nil
}
func (u *fakeUI) ScreenInfo(context.Context, uint32) (int, int, int, int, string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return 24, 80, 3, 7 + u.cursor, "sel", nil
}
func (u *fakeUI) moveCursor() {
	u.mu.Lock()
	u.cursor++
	u.mu.Unlock()
}
func (u *fakeUI) Tabs(context.Context) ([]uint32, uint32, error) {
	var ids []uint32
	for _, c := range u.mgr.List() {
		ids = append(ids, c.ID)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	act := u.active
	if len(ids) == 0 {
		return nil, 0, nil
	}
	found := false
	for _, id := range ids {
		found = found || id == act
	}
	if !found {
		act = ids[0]
	}
	return ids, act, nil
}
func (u *fakeUI) ActivateTab(_ context.Context, id uint32) error {
	u.mu.Lock()
	u.active = id
	u.mu.Unlock()
	return nil
}
func (u *fakeUI) SetCaption(_ context.Context, c uint32, s string) error {
	u.mu.Lock()
	u.captions[c] = s
	u.mu.Unlock()
	return nil
}
func (u *fakeUI) GetCaption(_ context.Context, c uint32) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.captions[c], nil
}
func (u *fakeUI) Clipboard(context.Context) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.clip, nil
}
func (u *fakeUI) SetClipboard(_ context.Context, t string) error {
	u.mu.Lock()
	u.clip = t
	u.mu.Unlock()
	return nil
}
func (u *fakeUI) Status(s string) {
	u.mu.Lock()
	u.status = append(u.status, s)
	u.mu.Unlock()
}
func (u *fakeUI) OpenTab(_ context.Context, req conn.OpenRequest) (*conn.Conn, error) {
	return u.mgr.Open(req, u.sink)
}
func (u *fakeUI) OpenSFTP(context.Context, uint32) error { return nil }

// shellOverrides describe a local /bin/sh session with a predictable prompt.
func shellOverrides(prompt string) map[string]any {
	if prompt == "" {
		prompt = "$ "
	}
	return map[string]any{"protocol": "local", "local": map[string]any{"shell": "/bin/sh", "env": map[string]any{"PS1": prompt}}}
}

func waitState(t *testing.T, cn *conn.Conn, state string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for cn.State() != state {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s: %s", cn.State(), state, cn.Info(false).Message)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// openShell opens a connected local shell with the given prompt.
func openShell(t *testing.T, m *conn.Manager, sink conn.Sink, prompt string) *conn.Conn {
	t.Helper()
	cn, err := m.Open(conn.OpenRequest{Overrides: shellOverrides(prompt), Cols: 100, Rows: 30}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, cn, conn.StateConnected)
	return cn
}

func setup(t *testing.T) (*Host, *conn.Conn, *fakeUI) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := conn.NewManager(st)
	sink := &ackSink{mgr: m}
	m.AddSink(sink)
	t.Cleanup(m.CloseAll)
	ui := &fakeUI{mgr: m, sink: sink, captions: map[uint32]string{}}
	cn := openShell(t, m, sink, "")
	h := &Host{Mgr: m, UI: ui}
	t.Cleanup(h.StopAll)
	return h, cn, ui
}

func runScript(t *testing.T, h *Host, cn *conn.Conn, lang, src string, args ...string) RunInfo {
	t.Helper()
	r, err := h.Start(RunRequest{Conn: cn.ID, Source: src, Lang: lang, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Done():
	case <-time.After(30 * time.Second):
		h.StopAll()
		t.Fatalf("script did not finish; output: %s", r.Info().Output)
	}
	return r.Info()
}

var scenarios = map[string]string{
	"python": `
crt.Screen.Synchronous = True
crt.Screen.Send("echo ready-$((2+3))\r")
print("w1=%d" % (1 if crt.Screen.WaitForString("ready-5", 5) else 0))
crt.Screen.Send("echo second-marker\r")
print("idx=%d" % crt.Screen.WaitForStrings(["nope", "second-marker"], 5))
print("mi=%d" % crt.Screen.MatchIndex)
crt.Screen.Send("echo v$((1+1))=$((6*7)):end\r")
crt.Screen.ReadString("v2=", 5)
print("rs=" + crt.Screen.ReadString(":end", 5))
print("to=%d" % (1 if crt.Screen.WaitForString("never-appears", 1) else 0))
print("prompt=" + crt.Dialog.Prompt("name?", "t", "x"))
print("msg=%d" % crt.Dialog.MessageBox("hi", "t", 4))
print("args=%d:%s:%s:%s" % (crt.Arguments.Count, crt.Arguments.GetArg(0), crt.Arguments[1], crt.Arguments(0)))
print("conn=%d" % (1 if crt.Session.Connected else 0))
print("get=" + crt.Screen.Get(1, 1, 2, 3))
print("row=%d" % crt.Screen.CurrentRow)
`,
	"javascript": `
crt.Screen.Synchronous = true;
crt.Screen.Send("echo ready-$((2+3))\r");
print("w1=" + (crt.Screen.WaitForString("ready-5", 5) ? 1 : 0));
crt.Screen.Send("echo second-marker\r");
print("idx=" + crt.Screen.WaitForStrings(["nope", "second-marker"], 5));
print("mi=" + crt.Screen.MatchIndex);
crt.Screen.Send("echo v$((1+1))=$((6*7)):end\r");
crt.Screen.ReadString("v2=", 5);
print("rs=" + crt.Screen.ReadString(":end", 5));
print("to=" + (crt.Screen.WaitForString("never-appears", 1) ? 1 : 0));
print("prompt=" + crt.Dialog.Prompt("name?", "t", "x"));
print("msg=" + crt.Dialog.MessageBox("hi", "t", 4));
print("args=" + crt.Arguments.Count + ":" + crt.Arguments.GetArg(0) + ":" + crt.Arguments[1] + ":" + crt.Arguments[0]);
print("conn=" + (crt.Session.Connected ? 1 : 0));
print("get=" + crt.Screen.Get(1, 1, 2, 3));
print("row=" + crt.Screen.CurrentRow);
`,
	"vbscript": `
crt.Screen.Synchronous = True
crt.Screen.Send "echo ready-$((2+3))" & vbCr
If crt.Screen.WaitForString("ready-5", 5) Then WScript.Echo "w1=1"
crt.Screen.Send "echo second-marker" & vbCr
WScript.Echo "idx=" & crt.Screen.WaitForStrings(Array("nope", "second-marker"), 5)
WScript.Echo "mi=" & crt.Screen.MatchIndex
crt.Screen.Send "echo v$((1+1))=$((6*7)):end" & vbCr
crt.Screen.ReadString "v2=", 5
WScript.Echo "rs=" & crt.Screen.ReadString(":end", 5)
If Not crt.Screen.WaitForString("never-appears", 1) Then WScript.Echo "to=0"
WScript.Echo "prompt=" & crt.Dialog.Prompt("name?", "t", "x")
WScript.Echo "msg=" & crt.Dialog.MessageBox("hi", "t", 4)
WScript.Echo "args=" & crt.Arguments.Count & ":" & crt.Arguments.GetArg(0) & ":" & crt.Arguments(1) & ":" & crt.Arguments(0)
If crt.Session.Connected Then WScript.Echo "conn=1"
WScript.Echo "get=" & crt.Screen.Get(1, 1, 2, 3)
WScript.Echo "row=" & crt.Screen.CurrentRow
`,
}

var wantLines = []string{"w1=1", "idx=2", "mi=2", "rs=42", "to=0", "prompt=bob", "msg=6", "args=2:one:two:one", "conn=1", "get=abcdef", "row=3"}

func needPython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("no Python interpreter installed")
		}
	}
}

func TestScenario(t *testing.T) {
	for lang, src := range scenarios {
		t.Run(lang, func(t *testing.T) {
			if lang == "python" {
				needPython(t)
			}
			h, cn, _ := setup(t)
			start := time.Now()
			info := runScript(t, h, cn, lang, src, "one", "two")
			if info.State != StateDone {
				t.Fatalf("state %s error %q output:\n%s", info.State, info.Error, info.Output)
			}
			if d := time.Since(start); d < 900*time.Millisecond {
				t.Errorf("failed wait returned after %v, want >= 1s", d)
			}
			lines := strings.Fields(strings.ReplaceAll(info.Output, "\r", ""))
			for _, w := range wantLines {
				found := false
				for _, l := range lines {
					if l == w {
						found = true
					}
				}
				if !found {
					t.Errorf("missing %q in output:\n%s", w, info.Output)
				}
			}
		})
	}
}

func TestErrors(t *testing.T) {
	cases := map[string]struct{ src, msg, line string }{
		"python":     {"x = 1\nraise ValueError(\"boom\")\n", "boom", "line 2"},
		"javascript": {"var a = 1;\nthrow new Error(\"boom\");\n", "boom", ":2"},
		"vbscript":   {"x = 1\nErr.Raise 1001, \"t\", \"boom\"\n", "boom", "(2,"},
	}
	for lang, c := range cases {
		t.Run(lang, func(t *testing.T) {
			if lang == "python" {
				needPython(t)
			}
			h, cn, _ := setup(t)
			info := runScript(t, h, cn, lang, c.src)
			if info.State != StateError || !strings.Contains(info.Error, c.msg) || !strings.Contains(info.Error, c.line) {
				t.Fatalf("state %s error %q, want error containing %q and %q", info.State, info.Error, c.msg, c.line)
			}
		})
	}
}

func TestStop(t *testing.T) {
	loops := map[string]string{
		"python":     "while True:\n    crt.Screen.WaitForString('never')\n",
		"javascript": "while (true) { crt.Screen.WaitForString('never'); }\n",
		"vbscript":   "Do\n crt.Screen.WaitForString \"never\"\nLoop\n",
	}
	busy := map[string]string{
		"javascript": "while (true) {}\n",
	}
	for lang, src := range loops {
		t.Run(lang, func(t *testing.T) {
			if lang == "python" {
				needPython(t)
			}
			h, cn, _ := setup(t)
			r, err := h.Start(RunRequest{Conn: cn.ID, Source: src, Lang: lang})
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(400 * time.Millisecond)
			if r.Info().State != StateRunning {
				t.Fatalf("not running: %+v", r.Info())
			}
			start := time.Now()
			h.StopConn(cn.ID)
			select {
			case <-r.Done():
			case <-time.After(time.Second):
				t.Fatal("stop did not end the script within 1s")
			}
			if st := r.Info().State; st != StateStopped {
				t.Errorf("state %s, want stopped (took %v)", st, time.Since(start))
			}
		})
	}
	h, cn, _ := setup(t)
	r, _ := h.Start(RunRequest{Conn: cn.ID, Source: busy["javascript"], Lang: "js"})
	time.Sleep(200 * time.Millisecond)
	h.Stop(r.Info().ID)
	select {
	case <-r.Done():
	case <-time.After(time.Second):
		t.Fatal("busy JS loop not interrupted")
	}
}

func TestVBScriptMainAndErrors(t *testing.T) {
	h, cn, _ := setup(t)
	src := `
Sub Main
  On Error Resume Next
  crt.GetTab 99
  WScript.Echo "err=" & crt.GetLastError
  WScript.Echo "msg=" & crt.GetLastErrorMessage
  crt.ClearLastError
  WScript.Echo "cleared=" & crt.GetLastError
  On Error GoTo 0
  crt.Screen.Send "echo vbs-main-$((3+4))" & vbCr
  If crt.Screen.WaitForString("vbs-main-7", 5) Then WScript.Echo "main-ok"
  crt.Window.Caption = "Hello"
  WScript.Echo "cap=" & crt.Window.Caption
  x = MsgBox("ok?", 4, "Title")
  WScript.Echo "mb=" & x & " " & InputBox("p", "t", "d")
End Sub
`
	info := runScript(t, h, cn, "vbs", src)
	if info.State != StateDone {
		t.Fatalf("state %s error %q output %q", info.State, info.Error, info.Output)
	}
	for _, w := range []string{"err=1", "out of range", "cleared=0", "main-ok", "cap=Hello", "mb=6 bob"} {
		if !strings.Contains(info.Output, w) {
			t.Errorf("missing %q in %q", w, info.Output)
		}
	}
}

func TestJSSleepAndQuit(t *testing.T) {
	h, cn, _ := setup(t)
	src := `
var t = Date.now();
crt.Sleep(300);
print("slept=" + (Date.now() - t >= 290));
print("ver=" + (crt.Version.length > 0) + " tabs=" + crt.GetTabCount() + " idx=" + crt.GetScriptTab().Index);
crt.Window.caption = "js";
print("cap=" + crt.Window.Caption);
try { crt.GetTab(9); } catch (e) { print("caught=" + (crt.GetLastError() == 1)); }
crt.Quit();
print("unreachable");
`
	info := runScript(t, h, cn, "js", src)
	if info.State != StateDone {
		t.Fatalf("state %s error %q output %q", info.State, info.Error, info.Output)
	}
	for _, w := range []string{"slept=true", "ver=true tabs=1 idx=1", "cap=js", "caught=true"} {
		if !strings.Contains(info.Output, w) {
			t.Errorf("missing %q in %q", w, info.Output)
		}
	}
	if strings.Contains(info.Output, "unreachable") {
		t.Error("Quit did not end the script")
	}
}

func TestPythonQuitAndConfig(t *testing.T) {
	needPython(t)
	h, cn, _ := setup(t)
	src := `
cfg = crt.OpenSessionConfiguration("Default")
cfg.SetOption("Scrollback", 777)
print("sb=%s" % cfg.GetOption("terminal.scrollback"))
print("proto=%s" % cfg.GetOption("Protocol Name"))
print("tab=%d" % crt.GetScriptTab().Index)
crt.Clipboard.Text = "clip"
print("clip=" + crt.Clipboard.Text)
try:
    crt.GetTab(42)
except RuntimeError as e:
    print("caught")
crt.Quit()
print("unreachable")
`
	info := runScript(t, h, cn, "py", src)
	if info.State != StateDone {
		t.Fatalf("state %s error %q output %q", info.State, info.Error, info.Output)
	}
	for _, w := range []string{"sb=777", "proto=SSH2", "tab=1", "clip=clip", "caught"} {
		if !strings.Contains(info.Output, w) {
			t.Errorf("missing %q in %q", w, info.Output)
		}
	}
	if strings.Contains(info.Output, "unreachable") {
		t.Error("Quit did not end the script")
	}
}

func TestLangDetect(t *testing.T) {
	cases := []struct {
		req  RunRequest
		want string
	}{
		{RunRequest{Path: "a.vbs"}, "vbscript"},
		{RunRequest{Path: "a.JS"}, "javascript"},
		{RunRequest{Path: "a.txt", Source: "# $language = \"VBScript\"\n"}, "vbscript"},
		{RunRequest{Path: "a.txt", Source: "' $language = \"JScript\"\n"}, "javascript"},
		{RunRequest{Source: "print(1)"}, "python"},
	}
	for _, c := range cases {
		if got := detectLang(c.req); got != c.want {
			t.Errorf("%+v: %s, want %s", c.req, got, c.want)
		}
	}
}

func TestParseConnectArg(t *testing.T) {
	r, err := parseConnectArg(`/SSH2 /L admin /P 2222 /PASSWORD "s3 cret" host.example`)
	if err != nil || r.Overrides["host"] != "host.example" || r.Overrides["port"] != 2222 || r.Overrides["username"] != "admin" || r.Password != "s3 cret" || r.Overrides["protocol"] != "ssh2" {
		t.Fatalf("%+v %v", r, err)
	}
	r, _ = parseConnectArg(`/S "Folder/Name"`)
	if r.SessionPath != "Folder/Name" {
		t.Fatalf("%+v", r)
	}
	r, _ = parseConnectArg(`/TELNET router 2001`)
	if r.Overrides["protocol"] != "telnet" || r.Overrides["port"] != 2001 {
		t.Fatalf("%+v", r)
	}
}
