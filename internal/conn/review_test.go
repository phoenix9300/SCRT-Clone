package conn

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A rejected login must not be retried by auto-reconnect: it would prompt
// again and again and could lock the account.
func TestAutoReconnectSkipsAuthFailures(t *testing.T) {
	port := fakeSSHServer(t, "s3cret")
	m, sink := newTestManager(t)
	pw := 0
	sink.answers = func(req PromptRequest) PromptResponse {
		if req.Kind == "hostkey" {
			return PromptResponse{OK: true, Choice: "save"}
		}
		pw++
		return PromptResponse{OK: true, Values: []string{"wrong"}}
	}
	ov := map[string]any{"protocol": "ssh2", "host": "127.0.0.1", "port": port, "username": "alice",
		"connection": map[string]any{"autoReconnect": true, "reconnectDelay": 1},
		"ssh":        map[string]any{"auth": []any{"password"}, "useAgent": false, "useOpenSSHKnownHosts": false, "identityFiles": []any{"/nonexistent"}}}
	c, err := m.Open(OpenRequest{Overrides: ov}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateDisconnected)
	first := pw
	time.Sleep(2500 * time.Millisecond)
	if pw != first || c.State() != StateDisconnected {
		t.Fatalf("authentication failure was retried: %d prompts, then %d; state %s", first, pw, c.State())
	}
	if !strings.Contains(c.Info(false).Message, "authentication failed") {
		t.Fatalf("message: %q", c.Info(false).Message)
	}
}

// A host key database that cannot be parsed must fail closed, even with the
// accept-new policy.
func TestUnreadableHostKeyDatabaseFailsClosed(t *testing.T) {
	port := fakeSSHServer(t, "s3cret")
	m, sink := newTestManager(t)
	sink.answers = func(req PromptRequest) PromptResponse {
		t.Errorf("unexpected prompt %s", req.Kind)
		return PromptResponse{}
	}
	bad := "this is not a known_hosts line at all !!!\n"
	if err := os.WriteFile(m.Store.KnownHostsPath(), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	ov := map[string]any{"protocol": "ssh2", "host": "127.0.0.1", "port": port, "username": "alice",
		"ssh": map[string]any{"hostKeyPolicy": "accept-new", "auth": []any{"password"}, "useAgent": false, "useOpenSSHKnownHosts": false, "identityFiles": []any{"/nonexistent"}}}
	c, err := m.Open(OpenRequest{Overrides: ov, Password: "s3cret"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateDisconnected)
	if msg := c.Info(false).Message; !strings.Contains(msg, "cannot be verified") {
		t.Fatalf("message: %q", msg)
	}
	if b, _ := os.ReadFile(m.Store.KnownHostsPath()); string(b) != bad {
		t.Fatalf("database was modified: %q", b)
	}
}

// Keyboard input must never block the caller, even when the transport has
// stopped accepting data, and queued sends keep their order.
func TestInputNeverBlocksAndKeepsOrder(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(20 * time.Second) // never reads
		}
	}()
	m, sink := newTestManager(t)
	c, err := m.Open(OpenRequest{Overrides: map[string]any{"protocol": "raw", "host": "127.0.0.1", "port": ln.Addr().(*net.TCPAddr).Port}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	done := make(chan struct{})
	go func() {
		chunk := make([]byte, 64*1024)
		for i := 0; i < 400; i++ { // 25 MB: far more than socket buffers and the queue hold
			c.Input(chunk)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Input blocked on a stalled transport")
	}
	closed := make(chan struct{})
	go func() { m.Close(c.ID); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closing a stalled connection hung")
	}

	// ordering of queued sends
	sh, err := m.Open(OpenRequest{Overrides: map[string]any{"protocol": "local", "local": map[string]any{"shell": "/bin/sh"}}, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, sh, StateConnected)
	var want strings.Builder
	for i := 0; i < 40; i++ {
		sh.QueueString(fmt.Sprintf("echo n%d-$((1+1))\\r", i))
		sh.QueueSend([]byte(fmt.Sprintf("echo m%d-$((2+2))\n", i)), 0, 0)
		fmt.Fprintf(&want, "n%d-2 m%d-4 ", i, i)
	}
	expect(t, sh, 0, "m39-4")
	var got strings.Builder
	for _, tok := range regexp.MustCompile(`[nm]\d+-[24]\b`).FindAllString(sh.TextSlice(0, sh.TextEnd()), -1) {
		got.WriteString(tok + " ")
	}
	if got.String() != want.String() {
		t.Fatalf("sends arrived out of order:\n got %s\nwant %s", got.String(), want.String())
	}
}

func TestDownloadPathConfinement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dl")
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := safeLocalJoin(dir, bad); err == nil {
			t.Errorf("safeLocalJoin accepted %q", bad)
		}
	}
	if runtime.GOOS == "windows" {
		if _, err := safeLocalJoin(dir, "C:evil"); err == nil {
			t.Error("drive-relative name accepted")
		}
	}
	if p, err := safeLocalJoin(dir, "ok.txt"); err != nil || p != filepath.Join(dir, "ok.txt") {
		t.Fatalf("%q %v", p, err)
	}
	for target, want := range map[string]bool{
		dir: true, filepath.Join(dir, "a", "b"): true, filepath.Join(dir, "..", "x"): false,
		filepath.Dir(dir): false, dir + "-other": false,
	} {
		if got := withinDir(dir, target); got != want {
			t.Errorf("withinDir(%q) = %v", target, got)
		}
	}
}
