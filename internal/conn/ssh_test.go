package conn

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// sshdFixture runs a private OpenSSH server for integration tests.
type sshdFixture struct {
	port      int
	clientKey string
	dir       string
}

func startSSHD(t *testing.T, extraConfig ...string) *sshdFixture {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		if _, serr := os.Stat("/usr/sbin/sshd"); serr != nil {
			t.Skip("sshd not installed")
		}
		sshd = "/usr/sbin/sshd"
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	for _, k := range []string{"hostkey", "client"} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, k)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	pub, _ := os.ReadFile(filepath.Join(dir, "client.pub"))
	_ = os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s/hostkey
PidFile %s/pid
AuthorizedKeysFile %s/authorized_keys
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin yes
AllowTcpForwarding yes
Subsystem sftp internal-sftp
LogLevel ERROR
`, port, dir, dir, dir)
	for _, e := range extraConfig {
		cfg += e + "\n"
	}
	cfgPath := filepath.Join(dir, "sshd_config")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o600)
	_ = os.MkdirAll("/run/sshd", 0o755)
	cmd := exec.Command(sshd, "-f", cfgPath, "-D", "-e")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sshd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return &sshdFixture{port: port, clientKey: filepath.Join(dir, "client"), dir: dir}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("sshd did not start: %s", stderr.String())
	return nil
}

func (f *sshdFixture) overrides() map[string]any {
	return map[string]any{
		"protocol": "ssh2", "host": "127.0.0.1", "port": f.port, "username": currentUser(),
		"ssh":   map[string]any{"identityFiles": []any{f.clientKey}, "useAgent": false, "useOpenSSHKnownHosts": false},
		"local": map[string]any{},
	}
}

func TestSSHSession(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	var prompts []PromptRequest
	sink.answers = func(req PromptRequest) PromptResponse {
		prompts = append(prompts, req)
		if req.Kind == "hostkey" {
			return PromptResponse{OK: true, Choice: "save"}
		}
		return PromptResponse{}
	}
	c, err := m.Open(OpenRequest{Overrides: f.overrides(), Cols: 90, Rows: 25}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	if len(prompts) != 1 || prompts[0].Kind != "hostkey" || prompts[0].HostKey.Changed || !strings.HasPrefix(prompts[0].HostKey.SHA256, "SHA256:") {
		t.Fatalf("unexpected prompts: %+v", prompts)
	}
	c.Input([]byte("echo ssh-$((20+3)); stty size; echo $TERM\n"))
	pos := expect(t, c, 0, "ssh-23")
	pos = expect(t, c, pos, "25 90")
	expect(t, c, pos, "xterm-256color")
	d := c.Info(false).Details
	if d["cipherOut"] == "" || d["kex"] == "" || !strings.Contains(d["serverVersion"], "OpenSSH") || !strings.Contains(d["authMethod"], "publickey") {
		t.Fatalf("connection details incomplete: %v", d)
	}

	// a clone shares the transport and survives the original being closed
	c2, err := m.Open(OpenRequest{CloneOf: c.ID, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c2, StateConnected)
	if len(prompts) != 1 {
		t.Fatalf("clone must not prompt again: %+v", prompts[1:])
	}
	m.Close(c.ID)
	c2.Input([]byte("echo clone-ok\n"))
	expect(t, c2, 0, "clone-ok")

	// second connection: the host key is known now, so no prompt
	c3, err := m.Open(OpenRequest{Overrides: f.overrides(), Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c3, StateConnected)
	if len(prompts) != 1 {
		t.Fatalf("known host prompted again: %+v", prompts[1:])
	}
	// remote exit ends the session cleanly
	c3.Input([]byte("exit\n"))
	waitState(t, c3, StateDisconnected)
	if c3.Info(false).Failed {
		t.Fatalf("clean exit reported as failure: %s", c3.Info(false).Message)
	}
}

func TestSSHHostKeyChangedIsDetected(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	// poison the database with a different key for this host
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(other + ".pub")
	fields := strings.Fields(string(pub))
	line := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", f.port, fields[0], fields[1])
	if err := os.WriteFile(m.Store.KnownHostsPath(), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen *HostKeyInfo
	sink.answers = func(req PromptRequest) PromptResponse {
		if req.Kind == "hostkey" {
			seen = req.HostKey
		}
		return PromptResponse{OK: false, Choice: "reject"}
	}
	c, err := m.Open(OpenRequest{Overrides: f.overrides()}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateDisconnected)
	if seen == nil || !seen.Changed || seen.OldSHA256 == "" {
		t.Fatalf("changed host key was not flagged: %+v", seen)
	}
	// strict policy refuses without asking
	ov := f.overrides()
	ov["ssh"].(map[string]any)["hostKeyPolicy"] = "strict"
	seen = nil
	c, _ = m.Open(OpenRequest{Overrides: ov}, sink)
	waitState(t, c, StateDisconnected)
	if seen != nil || !strings.Contains(c.Info(false).Message, "MISMATCH") {
		t.Fatalf("strict policy: prompt=%v message=%q", seen, c.Info(false).Message)
	}
}

func acceptAll(req PromptRequest) PromptResponse {
	if req.Kind == "hostkey" {
		return PromptResponse{OK: true, Choice: "save"}
	}
	return PromptResponse{}
}

func TestSSHForwardingJumpHostAndSFTP(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll

	// echo service reached through a local forward
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	echoPort := echo.Addr().(*net.TCPAddr).Port

	// the jump host is a stored session; the target reaches sshd through it
	if err := m.Store.Save("jump", f.overrides()); err != nil {
		t.Fatal(err)
	}
	ov := f.overrides()
	ov["firewall"] = "session:jump"
	c, err := m.Open(OpenRequest{Overrides: ov, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	c.Input([]byte("echo via-jump\n"))
	expect(t, c, 0, "via-jump")

	roundTrip := func(addr string, socks bool) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if socks {
			// SOCKS5 CONNECT 127.0.0.1:echoPort
			_, _ = conn.Write([]byte{5, 1, 0})
			buf := make([]byte, 10)
			if _, err := io.ReadFull(conn, buf[:2]); err != nil || buf[1] != 0 {
				t.Fatalf("socks greeting: %v %v", err, buf[:2])
			}
			_, _ = conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, byte(echoPort >> 8), byte(echoPort)})
			if _, err := io.ReadFull(conn, buf); err != nil || buf[1] != 0 {
				t.Fatalf("socks connect: %v %v", err, buf)
			}
		}
		msg := make([]byte, 4096)
		_, _ = rand.Read(msg)
		go func() { _, _ = conn.Write(msg) }()
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("forward round trip failed: %v", err)
		}
	}
	st, err := c.AddForward(store.PortForward{Name: "echo", Type: "local", BindAddr: "127.0.0.1", BindPort: 0, DestHost: "127.0.0.1", DestPort: echoPort, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(st.Address, false)
	ds, err := c.AddForward(store.PortForward{Name: "socks", Type: "dynamic", BindAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(ds.Address, true)
	if fw := c.Forwards(); len(fw) != 2 || fw[0].Total != 1 {
		t.Fatalf("forward status: %+v", fw)
	}
	if !c.RemoveForward("echo") {
		t.Fatal("RemoveForward failed")
	}
	if _, err := net.DialTimeout("tcp", st.Address, time.Second); err == nil {
		t.Fatal("removed forward still listening")
	}

	// SFTP on the same connection
	ss, err := m.OpenSFTPOn(c)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	local := t.TempDir()
	remote := t.TempDir()
	payload := make([]byte, 3<<20)
	_, _ = rand.Read(payload)
	_ = os.MkdirAll(filepath.Join(local, "tree", "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(local, "tree", "big.bin"), payload, 0o640)
	_ = os.WriteFile(filepath.Join(local, "tree", "sub", "note.txt"), []byte("hello"), 0o644)
	if _, err := ss.Enqueue("upload", filepath.Join(local, "tree"), remote, ""); err != nil {
		t.Fatal(err)
	}
	waitJobs := func() {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			busy := false
			for _, j := range ss.Jobs() {
				if j.State == "error" {
					t.Fatalf("job failed: %s", j.Error)
				}
				if j.State != "done" {
					busy = true
				}
			}
			if !busy {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("transfers did not finish")
	}
	waitJobs()
	got, err := os.ReadFile(filepath.Join(remote, "tree", "big.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("uploaded file differs: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(remote, "tree", "big.bin")); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode not preserved: %v", fi.Mode())
	}
	dir, entries, err := ss.List(filepath.Join(remote, "tree"))
	if err != nil || len(entries) != 2 || !entries[0].Dir || entries[1].Name != "big.bin" {
		t.Fatalf("list %s: %v %+v", dir, err, entries)
	}
	back := t.TempDir()
	if _, err := ss.Enqueue("download", back, filepath.Join(remote, "tree"), ""); err != nil {
		t.Fatal(err)
	}
	waitJobs()
	got, err = os.ReadFile(filepath.Join(back, "tree", "sub", "note.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("downloaded tree wrong: %v %q", err, got)
	}
	if err := ss.Rename(filepath.Join(remote, "tree", "sub", "note.txt"), filepath.Join(remote, "tree", "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := ss.Remove(filepath.Join(remote, "tree")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remote, "tree")); !os.IsNotExist(err) {
		t.Fatal("recursive remove left files behind")
	}
}

func TestSSHZmodemAndLogging(t *testing.T) {
	if _, err := exec.LookPath("sz"); err != nil {
		t.Skip("lrzsz not installed")
	}
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll
	down := t.TempDir()
	ov := f.overrides()
	ov["transfer"] = map[string]any{"downloadDir": down}
	c, err := m.Open(OpenRequest{Overrides: ov, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	logFile := filepath.Join(t.TempDir(), "s-%H.log")
	path, err := c.StartLog(LogRequest{File: logFile})
	if err != nil || !strings.HasSuffix(path, "s-127.0.0.1.log") {
		t.Fatalf("StartLog: %q %v", path, err)
	}

	src := t.TempDir()
	payload := make([]byte, 600_000)
	_, _ = rand.Read(payload)
	_ = os.WriteFile(filepath.Join(src, "zfile.bin"), payload, 0o644)
	c.Input([]byte("cd " + src + " && sz zfile.bin; echo sz-fin$((1+1))\n"))
	expect(t, c, 0, "sz-fin2")
	got, err := os.ReadFile(filepath.Join(down, "zfile.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("ZMODEM download wrong: err=%v len=%d", err, len(got))
	}

	// upload: remote rz announces itself, the UI answers with the file list
	up := t.TempDir()
	c.Input([]byte("cd " + up + " && rz; echo rz-fin$((1+1))\n"))
	deadline := time.Now().Add(10 * time.Second)
	for !c.AnswerUpload([]string{filepath.Join(src, "zfile.bin")}) {
		if time.Now().After(deadline) {
			t.Fatal("no upload request arrived")
		}
		time.Sleep(20 * time.Millisecond)
	}
	expect(t, c, 0, "rz-fin2")
	got, err = os.ReadFile(filepath.Join(up, "zfile.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("ZMODEM upload wrong: err=%v len=%d", err, len(got))
	}

	c.Input([]byte("printf '\\033[31mlogged-line\\033[0m\\n'\n"))
	expect(t, c, 0, "logged-line\r\n")
	c.StopLog()
	logged, _ := os.ReadFile(path)
	if !bytes.Contains(logged, []byte("\nlogged-line\n")) || bytes.Contains(logged, []byte("\x1b")) {
		t.Fatalf("log should hold clean text; got:\n%q", tail(logged, 300))
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

func TestExpandLogPattern(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 89e6, time.UTC)
	ctx := LogContext{Session: "Lab/Core A", Host: "10.0.0.1", Port: 22, Username: "admin"}
	got := ExpandLogPattern("%S_%H_%P_%U_%Y%M%D-%h%m%s.%t_100%%.log", ctx, now, true)
	if want := "Core A_10.0.0.1_22_admin_20260304-050607.089_100%.log"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := ExpandLogPattern("%H", LogContext{Host: "a/b:c"}, now, true); got != "a_b_c" {
		t.Fatalf("unsafe characters not replaced: %q", got)
	}
}

func TestTelnetNegotiation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type result struct {
		ttype      string
		cols, rows int
		line       string
		err        error
	}
	resc := make(chan result, 1)
	go func() {
		var r result
		c, err := ln.Accept()
		if err != nil {
			resc <- result{err: err}
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = c.Write([]byte{tnIAC, tnDO, optTType, tnIAC, tnDO, optNAWS, tnIAC, tnWILL, optEcho, tnIAC, tnWILL, optSGA, tnIAC, tnDO, optLinemode})
		_, _ = c.Write([]byte{tnIAC, tnSB, optTType, tnSEND, tnIAC, tnSE})
		_, _ = c.Write([]byte("login: "))
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for r.line == "" {
			n, err := c.Read(tmp)
			if err != nil {
				r.err = err
				break
			}
			buf = append(buf, tmp[:n]...)
			if i := bytes.Index(buf, []byte{tnIAC, tnSB, optTType, tnIS}); i >= 0 {
				if j := bytes.Index(buf[i:], []byte{tnIAC, tnSE}); j >= 0 {
					r.ttype = string(buf[i+4 : i+j])
				}
			}
			if i := bytes.Index(buf, []byte{tnIAC, tnSB, optNAWS}); i >= 0 && len(buf) >= i+7 {
				r.cols, r.rows = int(buf[i+3])<<8|int(buf[i+4]), int(buf[i+5])<<8|int(buf[i+6])
			}
			if i := bytes.Index(buf, []byte("user\r\x00")); i >= 0 && r.ttype != "" && r.cols != 0 {
				r.line = "user"
				if !bytes.Contains(buf, []byte{tnIAC, tnWONT, optLinemode}) {
					r.err = fmt.Errorf("LINEMODE was not refused: %v", buf)
				}
			}
		}
		_, _ = c.Write([]byte("\r\nWelcome \xff\xff!\r\n"))
		resc <- r
	}()
	m, sink := newTestManager(t)
	port := ln.Addr().(*net.TCPAddr).Port
	c, err := m.Open(OpenRequest{Overrides: map[string]any{"protocol": "telnet", "host": "127.0.0.1", "port": port}, Cols: 132, Rows: 43}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	expect(t, c, 0, "login: ")
	c.Input([]byte("user\r"))
	select {
	case r := <-resc:
		if r.err != nil || r.ttype != "XTERM-256COLOR" || r.cols != 132 || r.rows != 43 {
			t.Fatalf("negotiation: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("telnet server did not finish")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := c.WaitFor(ctx, 0, &Matcher{Patterns: []string{"Welcome"}})
	if err != nil || res.Index < 0 {
		t.Fatal("did not receive data after negotiation")
	}
	// an escaped IAC IAC pair arrives as a single 0xff data byte
	time.Sleep(100 * time.Millisecond)
	if !strings.Contains(sink.text(c.ID), "Welcome \xff!") {
		t.Fatalf("IAC unescaping wrong: %q", sink.text(c.ID))
	}
}
