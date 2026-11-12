package conn

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rawServer accepts raw TCP connections and hands each one to serve.
type rawServer struct {
	ln      net.Listener
	accepts atomic.Int32
}

func (s *rawServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func startRawServer(t *testing.T, serve func(n int, c net.Conn)) *rawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &rawServer{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := int(s.accepts.Add(1))
			go func() { defer c.Close(); serve(n, c) }()
		}
	}()
	return s
}

func rawOverrides(port int, extra map[string]any) map[string]any {
	ov := map[string]any{"protocol": "raw", "host": "127.0.0.1", "port": port, "raw": map[string]any{"tls": false}}
	for k, v := range extra {
		ov[k] = v
	}
	return ov
}

func waitCount(t *testing.T, what string, get func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: got %d, want %d", what, get(), want)
}

func TestAutoReconnect(t *testing.T) {
	m, sink := newTestManager(t)
	srv := startRawServer(t, func(n int, c net.Conn) {
		_, _ = c.Write([]byte("hello-" + string(rune('0'+n)) + "\r\n"))
		if n == 1 {
			return // the remote side closes the first connection
		}
		_, _ = c.Read(make([]byte, 1)) // later ones stay open until the client leaves
	})
	ov := rawOverrides(srv.port(), map[string]any{"connection": map[string]any{"autoReconnect": true, "reconnectDelay": 1}})
	start := time.Now()
	c := openConn(t, m, sink, ov, 80, 24)
	waitCount(t, "accepts", func() int { return int(srv.accepts.Load()) }, 2)
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Fatalf("reconnected after %v, before the 1 s delay", d)
	}
	waitState(t, c, StateConnected)
	expect(t, c, 0, "hello-2")

	// a user disconnect must not trigger a reconnect
	c.Disconnect()
	waitState(t, c, StateDisconnected)
	if c.Info(false).Failed {
		t.Fatal("user disconnect reported as failure")
	}
	time.Sleep(2500 * time.Millisecond)
	if n := srv.accepts.Load(); n != 2 || c.State() != StateDisconnected {
		t.Fatalf("session reconnected after a user disconnect: accepts=%d state=%s", n, c.State())
	}
	// but a manual reconnect still works
	c.Reconnect()
	waitState(t, c, StateConnected)
	if n := srv.accepts.Load(); n != 3 {
		t.Fatalf("manual reconnect: accepts=%d", n)
	}
}

func TestAutoReconnectKeepsTryingAfterFailure(t *testing.T) {
	m, sink := newTestManager(t)
	// reserve a port with nothing listening on it
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	ov := rawOverrides(port, map[string]any{"connection": map[string]any{"autoReconnect": true, "reconnectDelay": 1}})
	c := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c, StateDisconnected)
	if !c.Info(false).Failed {
		t.Fatal("refused connection should be a failure")
	}
	// bring the server up: the next automatic attempt must succeed
	l2, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Skipf("port got taken: %v", err)
	}
	defer l2.Close()
	go func() {
		for {
			cc, err := l2.Accept()
			if err != nil {
				return
			}
			defer cc.Close()
		}
	}()
	waitState(t, c, StateConnected)
}

func TestReconnectPicksUpStoredSessionEdits(t *testing.T) {
	m, sink := newTestManager(t)
	mk := func(name string) *rawServer {
		return startRawServer(t, func(n int, c net.Conn) {
			_, _ = c.Write([]byte("I am " + name + "\r\n"))
			_, _ = c.Read(make([]byte, 1))
		})
	}
	a, b := mk("server-a"), mk("server-b")
	if err := m.Store.Save("lab/box", rawOverrides(a.port(), map[string]any{"username": "orig"})); err != nil {
		t.Fatal(err)
	}
	c, err := m.Open(OpenRequest{SessionPath: "lab/box", Overrides: map[string]any{"description": "kept override"}, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	expect(t, c, 0, "I am server-a")
	c.Disconnect()
	waitState(t, c, StateDisconnected)

	if err := m.Store.Save("lab/box", rawOverrides(b.port(), map[string]any{"username": "edited"})); err != nil {
		t.Fatal(err)
	}
	c.Reconnect()
	waitState(t, c, StateConnected)
	expect(t, c, c.TextEnd()-int64(len("I am server-b\r\n")), "I am server-b")
	o := c.Options()
	if o.Port != b.port() || o.Username != "edited" || o.Description != "kept override" {
		t.Fatalf("options after reconnect: port=%d user=%q desc=%q", o.Port, o.Username, o.Description)
	}
	if a.accepts.Load() != 1 || b.accepts.Load() != 1 {
		t.Fatalf("accepts a=%d b=%d", a.accepts.Load(), b.accepts.Load())
	}
}

// slowSink never acknowledges output by itself.
type slowSink struct {
	mu    sync.Mutex
	bytes int64
	mgr   *Manager
	auto  bool
}

func (s *slowSink) Data(id uint32, p []byte) {
	s.mu.Lock()
	s.bytes += int64(len(p))
	auto := s.auto
	s.mu.Unlock()
	if auto {
		if c := s.mgr.Get(id); c != nil {
			c.Ack(int64(len(p)))
		}
	}
}
func (s *slowSink) Event(string, any) {}
func (s *slowSink) Prompt(context.Context, uint32, PromptRequest) (PromptResponse, error) {
	return PromptResponse{}, ErrCancelled
}
func (s *slowSink) received() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

func TestOutputFlowControl(t *testing.T) {
	const total = 6 << 20
	m, _ := newTestManager(t)
	sink := &slowSink{mgr: m}
	m.AddSink(sink)
	line := strings.Repeat("0123456789abcdefghijklmnopqrstuvwxyz", 2) + "\r\n" // 74 bytes
	srv := startRawServer(t, func(n int, c net.Conn) {
		chunk := []byte(strings.Repeat(line, 800)) // ~59 KB
		for sent := 0; sent < total; {
			k := len(chunk)
			if total-sent < k {
				k = total - sent
			}
			if _, err := c.Write(chunk[:k]); err != nil {
				return
			}
			sent += k
		}
	})
	c, err := m.Open(OpenRequest{Overrides: rawOverrides(srv.port(), nil), Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)

	// wait for the output to stall
	stalled := func() int64 {
		var last int64 = -1
		stable := time.Now()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			cur := sink.received()
			if cur != last {
				last, stable = cur, time.Now()
			} else if time.Since(stable) > 600*time.Millisecond {
				return cur
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("output never settled (%d bytes)", last)
		return 0
	}
	got := stalled()
	if got <= flowHighWater || got > flowHighWater+256<<10 {
		t.Fatalf("sink was fed %d bytes before stalling, want just above the %d high-water mark", got, flowHighWater)
	}
	if got >= total {
		t.Fatal("flow control never engaged")
	}
	// the stall is stable: nothing more arrives without an Ack
	time.Sleep(300 * time.Millisecond)
	if sink.received() != got {
		t.Fatal("data kept flowing without acknowledgement")
	}
	// acknowledging resumes the stream; with auto-ack the rest arrives
	sink.mu.Lock()
	sink.auto = true
	sink.mu.Unlock()
	c.Ack(got)
	waitCount(t, "bytes after Ack", func() int { return int(sink.received() / 1024) }, total/1024)
	if sink.received() != total {
		t.Fatalf("received %d bytes, want %d", sink.received(), total)
	}
}

func TestAttachReturnsReplayBuffer(t *testing.T) {
	m, sink := newTestManager(t)
	srv := startRawServer(t, func(n int, c net.Conn) {
		_, _ = c.Write([]byte("first\x1b[1mline\x1b[0m\r\n"))
		time.Sleep(300 * time.Millisecond)
		_, _ = c.Write([]byte("second line\r\n"))
		_, _ = c.Read(make([]byte, 1))
	})
	c := openConn(t, m, sink, rawOverrides(srv.port(), nil), 80, 24)
	waitState(t, c, StateConnected)
	expect(t, c, 0, "second line")
	// a new window attaches later and gets everything, escape sequences included
	other := &slowSink{mgr: m, auto: true}
	m.AddSink(other)
	replay := string(c.Attach(other))
	if replay != "first\x1b[1mline\x1b[0m\r\nsecond line\r\n" {
		t.Fatalf("replay = %q", replay)
	}
	if c.OutputOffset() != int64(len(replay)) {
		t.Fatalf("offset %d vs replay %d", c.OutputOffset(), len(replay))
	}
	// from now on only the new sink is fed
	before := sink.text(c.ID)
	c.emit([]byte("live\r\n"), true)
	if other.received() != 6 || sink.text(c.ID) != before {
		t.Fatalf("new sink got %d bytes, old sink changed=%v", other.received(), sink.text(c.ID) != before)
	}
}

func TestSessionLogging(t *testing.T) {
	m, sink := newTestManager(t)
	srv := startRawServer(t, func(n int, c net.Conn) {
		_, _ = c.Write([]byte("alpha\r\n\x1b[31mred\x1b[0m\r\nbeta"))
		time.Sleep(400 * time.Millisecond)
		_, _ = c.Write([]byte(" gamma\r\n"))
		time.Sleep(200 * time.Millisecond)
	})
	dir := t.TempDir()
	logOpts := func(raw bool, file string) map[string]any {
		return rawOverrides(srv.port(), map[string]any{"username": "tester", "log": map[string]any{
			"startOnConnect": true, "file": file, "raw": raw,
			"onConnect": "Start %S on %H:%P by %U 100%%", "onDisconnect": "End %H",
			"onEachLine": "[%h:%m:%s.%t] ",
		}})
	}
	if err := m.Store.Save("lab/core", logOpts(false, filepath.Join(dir, "%S_%H_%P.log"))); err != nil {
		t.Fatal(err)
	}
	c, err := m.Open(OpenRequest{SessionPath: "lab/core", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	wantPath := filepath.Join(dir, "core_127.0.0.1_"+itoa(srv.port())+".log")
	if got := c.Info(false); !got.Logging || got.LogFile != wantPath {
		t.Fatalf("log info: %+v want %s", got, wantPath)
	}
	waitState(t, c, StateDisconnected)
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	ts := `\[\d\d:\d\d:\d\d\.\d{3}\] `
	wants := []string{
		`^Start core on 127\.0\.0\.1:` + itoa(srv.port()) + ` by tester 100%$`,
		`^` + ts + `alpha$`,
		`^` + ts + `red$`,
		`^` + ts + `beta gamma$`,
		`^End 127\.0\.0\.1$`,
	}
	if len(lines) != len(wants) {
		t.Fatalf("log has %d lines, want %d:\n%s", len(lines), len(wants), data)
	}
	for i, w := range wants {
		if !regexp.MustCompile(w).MatchString(lines[i]) {
			t.Fatalf("log line %d = %q, want match %s\nfull log:\n%s", i, lines[i], w, data)
		}
	}
	if strings.Contains(string(data), "\x1b") || strings.Contains(string(data), "\r") {
		t.Fatalf("text log contains control characters: %q", data)
	}

	// raw logging keeps every byte, no timestamps
	rawFile := filepath.Join(dir, "raw.log")
	c2 := openConn(t, m, sink, logOpts(true, rawFile), 80, 24)
	waitState(t, c2, StateConnected)
	waitState(t, c2, StateDisconnected)
	rdata, _ := os.ReadFile(rawFile)
	if !strings.Contains(string(rdata), "alpha\r\n\x1b[31mred\x1b[0m\r\nbeta gamma\r\n") {
		t.Fatalf("raw log altered the stream: %q", rdata)
	}
	if strings.Contains(string(rdata), "[") && regexp.MustCompile(`\[\d\d:\d\d:\d\d\.`).Match(rdata) {
		t.Fatalf("raw log must not carry per-line timestamps: %q", rdata)
	}
}
