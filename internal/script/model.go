package script

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
)

// Version reported by crt.Version.
const Version = "0.1.0"

func sleepCtx(ctx context.Context, ms float64) error {
	if ms <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(time.Duration(ms * float64(time.Millisecond)))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newCRT(e *env) *object {
	o := newObject(e, "crt")
	ro := func(name string, f func() *object) {
		o.prop(name, func() (any, error) { return f(), nil }, nil)
	}
	ro("Screen", func() *object { return e.screen(e.cn) })
	ro("Session", func() *object { return e.session(e.cn) })
	ro("Dialog", func() *object { return e.cached("dialog", func() *object { return newDialog(e) }) })
	ro("Window", func() *object { return e.cached("window", func() *object { return newWindow(e) }) })
	ro("Arguments", func() *object { return e.cached("args", func() *object { return newArguments(e) }) })
	ro("Clipboard", func() *object { return e.cached("clip", func() *object { return newClipboard(e) }) })
	ro("FileTransfer", func() *object { return e.cached("ft", func() *object { return newFileTransfer(e) }) })
	ro("CommandWindow", func() *object { return e.cached("cmd", func() *object { return newCommandWindow(e) }) })
	o.prop("ScriptFullName", func() (any, error) { return e.path, nil }, nil)
	o.prop("Version", func() (any, error) { return Version, nil }, nil)

	o.method("Sleep", func(a []any) (any, error) { return nil, e.sleep(argFloat(a, 0, 0)) })
	o.method("Quit", func(a []any) (any, error) { return nil, errQuit })
	o.method("GetTab", func(a []any) (any, error) {
		ids, _, err := e.ui().Tabs(e.ctx)
		if err != nil {
			return nil, err
		}
		i := argInt(a, 0, 0)
		if i < 1 || i > len(ids) {
			return nil, fmt.Errorf("tab index %d out of range (1-%d)", i, len(ids))
		}
		return e.tab(ids[i-1])
	})
	o.method("GetTabCount", func(a []any) (any, error) {
		ids, _, err := e.ui().Tabs(e.ctx)
		return int64(len(ids)), err
	})
	o.method("GetActiveTab", func(a []any) (any, error) {
		_, act, err := e.ui().Tabs(e.ctx)
		if err != nil {
			return nil, err
		}
		return e.tab(act)
	})
	o.method("GetScriptTab", func(a []any) (any, error) { return e.tab(e.cn.ID) })
	o.method("OpenSessionConfiguration", func(a []any) (any, error) {
		return newSessionConfig(e, argStr(a, 0, ""))
	})
	o.method("GetLastError", func(a []any) (any, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return int64(e.lastErr), nil
	})
	o.method("GetLastErrorMessage", func(a []any) (any, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.lastMsg, nil
	})
	o.method("ClearLastError", func(a []any) (any, error) {
		e.mu.Lock()
		e.lastErr, e.lastMsg = 0, ""
		e.mu.Unlock()
		return nil, nil
	})
	return o
}

func (e *env) tab(id uint32) (*object, error) {
	cn := e.h.Mgr.Get(id)
	if cn == nil {
		return nil, errors.New("tab no longer exists")
	}
	return e.cached(fmt.Sprintf("tab%d", id), func() *object { return newTab(e, cn) }), nil
}

func (e *env) screen(cn *conn.Conn) *object {
	return e.cached(fmt.Sprintf("screen%d", cn.ID), func() *object { return newScreen(e, cn) })
}

func (e *env) session(cn *conn.Conn) *object {
	return e.cached(fmt.Sprintf("session%d", cn.ID), func() *object { return newSession(e, cn) })
}

// ---------------------------------------------------------------- Screen

func newScreen(e *env, cn *conn.Conn) *object {
	o := newObject(e, "Screen")
	var pos int64
	var sync, ignoreEsc, ignoreCase bool
	var matchIndex int
	if cn != nil {
		pos = cn.TextEnd()
	}
	from := func() int64 {
		if sync {
			return pos
		}
		return cn.TextEnd()
	}
	info := func() (rows, cols, r, c int, sel string, err error) {
		return e.ui().ScreenInfo(e.ctx, cn.ID)
	}
	infoProp := func(name string, pick func(rows, cols, r, c int) int) {
		o.prop(name, func() (any, error) {
			rows, cols, r, c, _, err := info()
			return int64(pick(rows, cols, r, c)), err
		}, nil)
	}
	infoProp("CurrentRow", func(rows, cols, r, c int) int { return r })
	infoProp("CurrentColumn", func(rows, cols, r, c int) int { return c })
	infoProp("Rows", func(rows, cols, r, c int) int { return rows })
	infoProp("Columns", func(rows, cols, r, c int) int { return cols })
	o.prop("Selection", func() (any, error) {
		_, _, _, _, sel, err := info()
		return sel, err
	}, nil)
	o.prop("Synchronous", func() (any, error) { return sync, nil }, func(v any) error { sync = toBool(v); return nil })
	o.prop("IgnoreEscape", func() (any, error) { return ignoreEsc, nil }, func(v any) error { ignoreEsc = toBool(v); return nil })
	o.prop("IgnoreCase", func() (any, error) { return ignoreCase, nil }, func(v any) error { ignoreCase = toBool(v); return nil })
	o.prop("MatchIndex", func() (any, error) { return int64(matchIndex), nil }, nil)

	send := func(s string) error {
		if !cn.Connected() {
			return errors.New("the session is not connected")
		}
		return e.sendTo(cn, encodeText(cn, s), 0, 0)
	}
	o.method("Send", func(a []any) (any, error) { return nil, send(argStr(a, 0, "")) })
	o.method("SendKeys", func(a []any) (any, error) { return nil, send(argStr(a, 0, "")) })
	// sendBreak is a no-op on connection types that have no break (local, raw).
	sendBreak := func() error {
		switch cn.Options().Protocol {
		case "local", "raw":
			return nil
		}
		return cn.SendBreak()
	}
	o.method("SendSpecial", func(a []any) (any, error) {
		switch strings.ToUpper(argStr(a, 0, "")) {
		case "TN_BREAK", "BREAK", "SERIAL_BREAK":
			return nil, sendBreak()
		}
		return nil, nil
	})
	o.method("SendBreak", func(a []any) (any, error) { return nil, sendBreak() })
	o.method("Clear", func(a []any) (any, error) { return nil, nil })

	// wait runs a matcher from the right start position and handles the
	// bookkeeping shared by all wait methods.
	wait := func(patterns []string, timeout float64, ci bool) (conn.MatchResult, int, error) {
		ctx := e.ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
			defer cancel()
		}
		start := from()
		res, _ := cn.WaitFor(ctx, start, &conn.Matcher{Patterns: patterns, IgnoreCase: ci || ignoreCase})
		if err := e.ctx.Err(); err != nil {
			return res, 0, err
		}
		if res.Index < 0 {
			matchIndex = 0
			return res, 0, nil
		}
		pos = res.End
		matchIndex = res.Index + 1
		return res, matchIndex, nil
	}
	o.method("WaitForString", func(a []any) (any, error) {
		if len(a) == 0 {
			return false, errors.New("WaitForString needs a string")
		}
		_, idx, err := wait([]string{toStr(a[0])}, argFloat(a, 1, 0), argBool(a, 2, false))
		return idx > 0, err
	})
	o.method("WaitForStrings", func(a []any) (any, error) {
		timeout := 0.0
		if n := len(a); n > 1 && isNumber(a[n-1]) {
			timeout = toFloat(a[n-1])
			a = a[:n-1]
		}
		pats := flattenStrings(a)
		if len(pats) == 0 {
			return int64(0), errors.New("WaitForStrings needs at least one string")
		}
		_, idx, err := wait(pats, timeout, false)
		return int64(idx), err
	})
	o.method("ReadString", func(a []any) (any, error) {
		timeout := 0.0
		if n := len(a); n > 1 && isNumber(a[n-1]) {
			timeout = toFloat(a[n-1])
			a = a[:n-1]
		}
		delims := flattenStrings(a)
		if len(delims) == 0 {
			return "", errors.New("ReadString needs at least one delimiter")
		}
		start := from()
		res, idx, err := wait(delims, timeout, false)
		if err != nil {
			return "", err
		}
		if idx > 0 {
			return res.Before, nil
		}
		end := cn.TextEnd()
		pos = end
		return cn.TextSlice(start, end), nil
	})
	o.method("WaitForCursor", func(a []any) (any, error) {
		timeout := argFloat(a, 0, 0)
		_, _, r0, c0, _, err := info()
		if err != nil {
			return false, err
		}
		deadline := time.Now().Add(time.Duration(timeout * float64(time.Second)))
		for {
			if err := sleepCtx(e.ctx, 100); err != nil {
				return false, err
			}
			_, _, r, c, _, err := info()
			if err != nil {
				return false, err
			}
			if r != r0 || c != c0 {
				return true, nil
			}
			if timeout > 0 && time.Now().After(deadline) {
				return false, nil
			}
		}
	})
	get := func(sep string) method {
		return func(a []any) (any, error) {
			txt, err := e.ui().ScreenText(e.ctx, cn.ID, argInt(a, 0, 1), argInt(a, 1, 1), argInt(a, 2, 1), argInt(a, 3, 1))
			if err != nil {
				return "", err
			}
			return strings.Join(strings.Split(txt, "\n"), sep), nil
		}
	}
	o.method("Get", get(""))
	o.method("Get2", get("\r\n"))
	return o
}

// sendTo writes data to cn and waits until it is written or the run is
// stopped (a stalled transport must not make a script unstoppable).
func (e *env) sendTo(cn *conn.Conn, data []byte, charDelay, lineDelay time.Duration) error {
	done := make(chan struct{})
	go func() { cn.Send(data, charDelay, lineDelay); close(done) }()
	select {
	case <-done:
		return e.ctx.Err()
	case <-e.ctx.Done():
		return e.ctx.Err()
	}
}

// encodeText converts s to the bytes sent on the wire.
func encodeText(cn *conn.Conn, s string) []byte {
	enc := strings.ToLower(strings.TrimSpace(cn.Options().Terminal.Encoding))
	if enc == "" || enc == "utf-8" || enc == "utf8" {
		return []byte(s)
	}
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 256 {
			out = append(out, byte(r))
		} else {
			out = append(out, '?')
		}
	}
	return out
}

// --------------------------------------------------------------- Session

func newSession(e *env, cn *conn.Conn) *object {
	o := newObject(e, "Session")
	o.prop("Connected", func() (any, error) { return cn.Connected(), nil }, nil)
	o.prop("Logging", func() (any, error) { return cn.LogPath() != "", nil }, nil)
	o.prop("LogFileName", func() (any, error) {
		if p := cn.LogPath(); p != "" {
			return p, nil
		}
		return e.logName, nil
	}, func(v any) error { e.logName = toStr(v); return nil })
	o.prop("Path", func() (any, error) { return cn.SessionPath, nil }, nil)
	detail := func(name string, keys ...string) {
		o.prop(name, func() (any, error) {
			d := cn.Info(false).Details
			for _, k := range keys {
				if v := d[k]; v != "" {
					return v, nil
				}
			}
			return "", nil
		}, nil)
	}
	detail("RemoteAddress", "remoteAddress")
	detail("LocalAddress", "localAddress")
	o.prop("RemotePort", func() (any, error) {
		if v := cn.Info(false).Details["remotePort"]; v != "" {
			return int64(toInt(v)), nil
		}
		return int64(cn.Options().Port), nil
	}, nil)

	o.method("Connect", func(a []any) (any, error) {
		arg := argStr(a, 0, "")
		if strings.TrimSpace(arg) == "" {
			if cn.State() == conn.StateDisconnected {
				cn.Reconnect()
			}
			return nil, e.waitConnected(cn)
		}
		req, err := parseConnectArg(arg)
		if err != nil {
			return nil, err
		}
		if err := e.h.Mgr.Retarget(cn, req); err != nil {
			return nil, err
		}
		return nil, e.waitConnected(cn)
	})
	o.method("ConnectInTab", func(a []any) (any, error) {
		req, err := parseConnectArg(argStr(a, 0, ""))
		if err != nil {
			return nil, err
		}
		return e.openTab(req)
	})
	o.method("Disconnect", func(a []any) (any, error) {
		cn.Disconnect()
		// the transport closes asynchronously; Connected must be false on return
		for i := 0; i < 200 && cn.State() != conn.StateDisconnected; i++ {
			if err := sleepCtx(e.ctx, 15); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	o.method("Log", func(a []any) (any, error) {
		if !argBool(a, 0, true) {
			cn.StopLog()
			return nil, nil
		}
		req := conn.LogRequest{File: e.logName}
		if len(a) > 1 && a[1] != nil {
			b := toBool(a[1])
			req.Append = &b
		}
		if len(a) > 2 && a[2] != nil {
			b := toBool(a[2])
			req.Raw = &b
		}
		_, err := cn.StartLog(req)
		return nil, err
	})
	o.method("Lock", func(a []any) (any, error) { return nil, nil })
	o.method("Unlock", func(a []any) (any, error) { return nil, nil })
	o.method("SetStatusText", func(a []any) (any, error) {
		e.ui().Status(argStr(a, 0, ""))
		return nil, nil
	})
	return o
}

// openTab opens a connection in a new tab and waits for it.
func (e *env) openTab(req conn.OpenRequest) (any, error) {
	cn, err := e.ui().OpenTab(e.ctx, req)
	if err != nil {
		return nil, err
	}
	if err := e.waitConnected(cn); err != nil {
		return nil, err
	}
	return e.tab(cn.ID)
}

// waitConnected blocks until cn is connected, failing when the attempt fails.
func (e *env) waitConnected(cn *conn.Conn) error {
	start := time.Now()
	sawConnecting := false
	for time.Since(start) < 5*time.Minute {
		switch cn.State() {
		case conn.StateConnected:
			return nil
		case conn.StateConnecting:
			sawConnecting = true
		default:
			if sawConnecting || time.Since(start) > time.Second {
				msg := cn.Info(false).Message
				if msg == "" {
					msg = "connection failed"
				}
				return errors.New(msg)
			}
		}
		if err := sleepCtx(e.ctx, 25); err != nil {
			return err
		}
	}
	return errors.New("timed out waiting for the connection")
}

// splitArgs splits a command line honouring double quotes.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQ, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQ, has = !inQ, true
		case (r == ' ' || r == '\t') && !inQ:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// parseConnectArg understands the classic /S, /SSH2, /TELNET ... switches.
func parseConnectArg(arg string) (conn.OpenRequest, error) {
	var req conn.OpenRequest
	ov := map[string]any{}
	var rest []string
	toks := splitArgs(arg)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if !strings.HasPrefix(t, "/") {
			rest = append(rest, t)
			continue
		}
		next := func() string {
			if i+1 < len(toks) {
				i++
				return toks[i]
			}
			return ""
		}
		switch strings.ToUpper(t) {
		case "/S":
			req.SessionPath = next()
		case "/SSH2", "/SSH", "/SSH1":
			ov["protocol"] = "ssh2"
		case "/TELNET":
			ov["protocol"] = "telnet"
		case "/RLOGIN":
			ov["protocol"] = "rlogin"
		case "/RAW":
			ov["protocol"] = "raw"
		case "/L":
			ov["username"] = next()
		case "/P":
			ov["port"] = toInt(next())
		case "/PASSWORD":
			req.Password = next()
		case "/C", "/AUTH", "/ARG", "/FIREWALL", "/IDENTITY", "/D", "/LOGON":
			next()
		}
	}
	if req.SessionPath == "" {
		if len(rest) == 0 {
			return req, errors.New("no host or session given")
		}
		ov["host"] = rest[0]
		if len(rest) > 1 {
			ov["port"] = toInt(rest[1])
		}
		if _, ok := ov["protocol"]; !ok {
			ov["protocol"] = "ssh2"
		}
	}
	if len(ov) > 0 {
		req.Overrides = ov
	}
	return req, nil
}

// ---------------------------------------------------------------- Dialog

func newDialog(e *env) *object {
	o := newObject(e, "Dialog")
	o.method("MessageBox", func(a []any) (any, error) {
		v, err := e.ui().MessageBox(e.ctx, argStr(a, 0, ""), argStr(a, 1, "Hawser"), argInt(a, 2, 0))
		return int64(v), err
	})
	o.method("Prompt", func(a []any) (any, error) {
		v, ok, err := e.ui().Prompt(e.ctx, argStr(a, 0, ""), argStr(a, 1, "Hawser"), argStr(a, 2, ""), argBool(a, 3, false))
		if !ok {
			v = ""
		}
		return v, err
	})
	o.method("FileOpenDialog", func(a []any) (any, error) {
		return e.ui().FileOpen(e.ctx, argStr(a, 0, "Open"), argStr(a, 1, "Open"), argStr(a, 2, ""), argStr(a, 3, ""))
	})
	return o
}

// ---------------------------------------------------------------- Window

func newWindow(e *env) *object {
	o := newObject(e, "Window")
	o.prop("Caption", func() (any, error) { return e.ui().GetCaption(e.ctx, 0) },
		func(v any) error { return e.ui().SetCaption(e.ctx, 0, toStr(v)) })
	o.prop("State", func() (any, error) { return int64(0), nil }, nil)
	o.prop("Active", func() (any, error) { return true, nil }, nil)
	o.method("Activate", func(a []any) (any, error) { return nil, nil })
	o.method("Show", func(a []any) (any, error) { return nil, nil })
	return o
}

// ------------------------------------------------------------------- Tab

func newTab(e *env, cn *conn.Conn) *object {
	o := newObject(e, "Tab")
	o.prop("Caption", func() (any, error) { return e.ui().GetCaption(e.ctx, cn.ID) },
		func(v any) error { return e.ui().SetCaption(e.ctx, cn.ID, toStr(v)) })
	o.prop("Index", func() (any, error) {
		ids, _, err := e.ui().Tabs(e.ctx)
		for i, id := range ids {
			if id == cn.ID {
				return int64(i + 1), err
			}
		}
		return int64(0), err
	}, nil)
	o.prop("Screen", func() (any, error) { return e.screen(cn), nil }, nil)
	o.prop("Session", func() (any, error) { return e.session(cn), nil }, nil)
	o.method("Activate", func(a []any) (any, error) { return nil, e.ui().ActivateTab(e.ctx, cn.ID) })
	o.method("Clone", func(a []any) (any, error) {
		return e.openTab(conn.OpenRequest{CloneOf: cn.ID})
	})
	o.method("Close", func(a []any) (any, error) { e.h.Mgr.Close(cn.ID); return nil, nil })
	o.method("ConnectSftp", func(a []any) (any, error) { return nil, e.ui().OpenSFTP(e.ctx, cn.ID) })
	return o
}

// ------------------------------------------------------------- Arguments

func newArguments(e *env) *object {
	o := newObject(e, "Arguments")
	get := func(a []any) (any, error) {
		i := argInt(a, 0, 0)
		if i < 0 || i >= len(e.args) {
			return nil, fmt.Errorf("argument index %d out of range", i)
		}
		return e.args[i], nil
	}
	o.prop("Count", func() (any, error) { return int64(len(e.args)), nil }, nil)
	o.method("GetArg", get)
	o.call = get
	o.indexed = func() []any {
		out := make([]any, len(e.args))
		for i, s := range e.args {
			out[i] = s
		}
		return out
	}
	return o
}

// ------------------------------------------------------------- Clipboard

func newClipboard(e *env) *object {
	o := newObject(e, "Clipboard")
	o.prop("Text", func() (any, error) { return e.ui().Clipboard(e.ctx) },
		func(v any) error { return e.ui().SetClipboard(e.ctx, toStr(v)) })
	o.val("Format", &e.clipFmt)
	return o
}

// ---------------------------------------------------------- FileTransfer

func newFileTransfer(e *env) *object {
	o := newObject(e, "FileTransfer")
	cn := e.cn
	// xfer runs a transfer and returns when it has finished.
	xfer := func(proto, dir string, files []string, dest string) (any, error) {
		w := &xferWatch{id: cn.ID, done: make(chan string, 1)}
		e.h.Mgr.AddSink(w)
		defer e.h.Mgr.RemoveSink(w)
		if err := cn.StartTransfer(conn.TransferRequest{Protocol: proto, Direction: dir, Files: files, Dest: dest}); err != nil {
			return false, err
		}
		select {
		case msg := <-w.done:
			if msg != "" {
				return false, errors.New(msg)
			}
			return true, nil
		case <-e.ctx.Done():
			return false, e.ctx.Err()
		}
	}
	o.method("ZmodemUploadAddFile", func(a []any) (any, error) {
		cn.QueueZmodemUpload([]string{argStr(a, 0, "")})
		return true, nil
	})
	o.method("ClearZmodemUploadList", func(a []any) (any, error) { cn.ClearZmodemUpload(); return nil, nil })
	o.method("SendXmodem", func(a []any) (any, error) { return xfer("xmodem", "upload", []string{argStr(a, 0, "")}, "") })
	o.method("ReceiveXmodem", func(a []any) (any, error) { return xfer("xmodem", "download", nil, argStr(a, 0, "")) })
	o.method("SendYmodem", func(a []any) (any, error) { return xfer("ymodem", "upload", []string{argStr(a, 0, "")}, "") })
	o.method("ReceiveYmodem", func(a []any) (any, error) { return xfer("ymodem", "download", nil, "") })
	o.method("SendAscii", func(a []any) (any, error) {
		// conn.SendASCII sends in the background; a script needs it written
		// before its next Send, so do it here.
		path := argStr(a, 0, "")
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		nl := "\r"
		switch cn.Options().Terminal.NewlineOnSend {
		case "lf":
			nl = "\n"
		case "crlf":
			nl = "\r\n"
		}
		text := strings.NewReplacer("\r\n", nl, "\r", nl, "\n", nl).Replace(string(data))
		t := cn.Options().Terminal
		err = e.sendTo(cn, []byte(text), time.Duration(t.CharSendDelay)*time.Millisecond, time.Duration(t.LineSendDelay)*time.Millisecond)
		return err == nil, err
	})
	return o
}

// xferWatch is a manager sink that reports the end of a connection's transfer.
type xferWatch struct {
	id   uint32
	done chan string // "" on success, else the error
}

func (w *xferWatch) Data(uint32, []byte) {}
func (w *xferWatch) Prompt(context.Context, uint32, conn.PromptRequest) (conn.PromptResponse, error) {
	return conn.PromptResponse{}, conn.ErrCancelled
}
func (w *xferWatch) Event(name string, payload any) {
	ev, ok := payload.(conn.TransferEvent)
	if !ok || name != "xfer.progress" || ev.ID != w.id || (ev.Kind != "done" && ev.Kind != "error") {
		return
	}
	msg := ev.Error
	if ev.Kind == "error" && msg == "" {
		msg = "transfer failed"
	}
	select {
	case w.done <- msg:
	default:
	}
}

// --------------------------------------------------------- CommandWindow

func newCommandWindow(e *env) *object {
	o := newObject(e, "CommandWindow")
	e.cmdText, e.cmdAll, e.cmdVisble = "", false, false
	o.val("Text", &e.cmdText)
	o.val("Visible", &e.cmdVisble)
	o.val("SendToAllSessions", &e.cmdAll)
	o.method("Send", func(a []any) (any, error) {
		text := toStr(e.cmdText) + "\r"
		targets := []*conn.Conn{e.cn}
		if toBool(e.cmdAll) {
			targets = nil
			for _, c := range e.h.Mgr.List() {
				if c.Connected() {
					targets = append(targets, c)
				}
			}
		}
		for _, c := range targets {
			if err := e.sendTo(c, encodeText(c, text), 0, 0); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return o
}
