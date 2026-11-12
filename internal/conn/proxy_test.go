package conn

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// testProxy is a tiny in-process proxy server of one flavour.
type testProxy struct {
	ln   net.Listener
	mu   sync.Mutex
	hits int
	// what the last request carried
	target string // host:port as requested
	user   string
	pass   string
	rawIP  string // SOCKS4: the IPv4 field
}

func (p *testProxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *testProxy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits
}

func (p *testProxy) record(target, user, pass, ip string) {
	p.mu.Lock()
	p.hits++
	p.target, p.user, p.pass, p.rawIP = target, user, pass, ip
	p.mu.Unlock()
}

func (p *testProxy) last() (target, user, pass, ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.target, p.user, p.pass, p.rawIP
}

func startTestProxy(t *testing.T, handle func(p *testProxy, c net.Conn)) *testProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &testProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.SetDeadline(time.Now().Add(30 * time.Second))
				handle(p, c)
			}()
		}
	}()
	return p
}

// socks5Handler: wantUser == "" means no authentication. refuse makes the
// proxy answer "connection not allowed by ruleset".
func socks5Handler(wantUser, wantPass string, refuse bool) func(p *testProxy, c net.Conn) {
	return func(p *testProxy, c net.Conn) {
		defer c.Close()
		br := bufio.NewReader(c)
		var hdr [2]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil || hdr[0] != 5 {
			return
		}
		methods := make([]byte, hdr[1])
		if _, err := io.ReadFull(br, methods); err != nil {
			return
		}
		user, pass := "", ""
		if wantUser == "" {
			_, _ = c.Write([]byte{5, 0})
		} else {
			has2 := false
			for _, m := range methods {
				has2 = has2 || m == 2
			}
			if !has2 {
				_, _ = c.Write([]byte{5, 0xff})
				return
			}
			_, _ = c.Write([]byte{5, 2})
			ver, _ := br.ReadByte()
			ul, _ := br.ReadByte()
			ub := make([]byte, ul)
			_, _ = io.ReadFull(br, ub)
			pl, _ := br.ReadByte()
			pb := make([]byte, pl)
			_, _ = io.ReadFull(br, pb)
			user, pass = string(ub), string(pb)
			if ver != 1 || user != wantUser || pass != wantPass {
				_, _ = c.Write([]byte{1, 1})
				return
			}
			_, _ = c.Write([]byte{1, 0})
		}
		var rq [4]byte
		if _, err := io.ReadFull(br, rq[:]); err != nil {
			return
		}
		var host string
		switch rq[3] {
		case 1:
			var ip [4]byte
			_, _ = io.ReadFull(br, ip[:])
			host = net.IP(ip[:]).String()
		case 3:
			l, _ := br.ReadByte()
			nb := make([]byte, l)
			_, _ = io.ReadFull(br, nb)
			host = string(nb)
		case 4:
			var ip [16]byte
			_, _ = io.ReadFull(br, ip[:])
			host = net.IP(ip[:]).String()
		}
		var pb [2]byte
		_, _ = io.ReadFull(br, pb[:])
		target := net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(pb[:])))
		p.record(target, user, pass, "")
		if refuse {
			_, _ = c.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		r, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		_ = c.SetDeadline(time.Time{})
		pipeBoth(&bufferedConn{Conn: c, r: br}, r)
	}
}

func socks4Handler(refuse bool) func(p *testProxy, c net.Conn) {
	return func(p *testProxy, c net.Conn) {
		defer c.Close()
		br := bufio.NewReader(c)
		var hdr [8]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil || hdr[0] != 4 || hdr[1] != 1 {
			return
		}
		port := binary.BigEndian.Uint16(hdr[2:4])
		ip := net.IP(hdr[4:8])
		uid, _ := br.ReadString(0)
		host := ip.String()
		if hdr[4] == 0 && hdr[5] == 0 && hdr[6] == 0 && hdr[7] != 0 {
			name, _ := br.ReadString(0)
			host = strings.TrimSuffix(name, "\x00")
		}
		target := net.JoinHostPort(host, fmt.Sprint(port))
		p.record(target, strings.TrimSuffix(uid, "\x00"), "", ip.String())
		if refuse {
			_, _ = c.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
			return
		}
		r, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			_, _ = c.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
			return
		}
		_, _ = c.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0})
		_ = c.SetDeadline(time.Time{})
		pipeBoth(&bufferedConn{Conn: c, r: br}, r)
	}
}

// httpHandler: user == "" means open proxy. status != 0 refuses every request
// with that status code.
func httpHandler(user, pass string, status int) func(p *testProxy, c net.Conn) {
	return func(p *testProxy, c net.Conn) {
		defer c.Close()
		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil || req.Method != "CONNECT" {
			_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
			return
		}
		gotU, gotP := "", ""
		if h := req.Header.Get("Proxy-Authorization"); strings.HasPrefix(h, "Basic ") {
			if b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic ")); err == nil {
				gotU, gotP, _ = strings.Cut(string(b), ":")
			}
		}
		p.record(req.Host, gotU, gotP, "")
		if status != 0 {
			_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", status, http.StatusText(status))
			return
		}
		if user != "" && (gotU != user || gotP != pass) {
			_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"t\"\r\nContent-Length: 0\r\n\r\n")
			return
		}
		r, err := net.DialTimeout("tcp", req.Host, 5*time.Second)
		if err != nil {
			_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
			return
		}
		_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
		_ = c.SetDeadline(time.Time{})
		pipeBoth(&bufferedConn{Conn: c, r: br}, r)
	}
}

// proxiedOverrides returns sshd overrides routed through the proxy.
func proxiedOverrides(f *sshdFixture, typ string, p *testProxy, user string) map[string]any {
	ov := f.overrides()
	ov["firewall"] = "proxy"
	ov["proxy"] = map[string]any{"type": typ, "host": "127.0.0.1", "port": p.port(), "username": user}
	return ov
}

func runProxied(t *testing.T, m *Manager, sink *testSink, ov map[string]any, session string) *Conn {
	t.Helper()
	if err := m.Store.Save(session, ov); err != nil {
		t.Fatal(err)
	}
	c, err := m.Open(OpenRequest{SessionPath: session, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	c.Input([]byte("echo proxied-$((6*7))\n"))
	expect(t, c, 0, "proxied-42")
	return c
}

func failedMessage(t *testing.T, c *Conn) string {
	t.Helper()
	waitState(t, c, StateDisconnected)
	inf := c.Info(false)
	if !inf.Failed {
		t.Fatalf("expected a failed connection, message %q", inf.Message)
	}
	return inf.Message
}

func TestProxySOCKS5(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll

	open := startTestProxy(t, socks5Handler("", "", false))
	runProxied(t, m, sink, proxiedOverrides(f, "socks5", open, ""), "s5open")
	if target, _, _, _ := open.last(); open.count() != 1 || target != fmt.Sprintf("127.0.0.1:%d", f.port) {
		t.Fatalf("proxy saw %d connections, target %q", open.count(), target)
	}

	auth := startTestProxy(t, socks5Handler("alice", "p4ss", false))
	ov := proxiedOverrides(f, "socks5", auth, "alice")
	if err := m.Store.Save("s5auth", ov); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.Vault.Set(store.SecretKey("s5auth", "proxy"), "p4ss"); err != nil {
		t.Fatal(err)
	}
	c, err := m.Open(OpenRequest{SessionPath: "s5auth", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	c.Input([]byte("echo authed-$((1+1))\n"))
	expect(t, c, 0, "authed-2")
	if _, u, pw, _ := auth.last(); u != "alice" || pw != "p4ss" {
		t.Fatalf("proxy credentials: %q %q", u, pw)
	}

	// wrong password in the vault
	if err := m.Store.Vault.Set(store.SecretKey("s5auth", "proxy"), "nope"); err != nil {
		t.Fatal(err)
	}
	c2, _ := m.Open(OpenRequest{SessionPath: "s5auth", Cols: 80, Rows: 24}, sink)
	msg := failedMessage(t, c2)
	if !strings.Contains(msg, "SOCKS5") || !strings.Contains(strings.ToLower(msg), "authentication") {
		t.Fatalf("unhelpful SOCKS5 auth error: %q", msg)
	}
}

func TestProxySOCKS5Refusal(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll
	p := startTestProxy(t, socks5Handler("", "", true))
	ov := proxiedOverrides(f, "socks5", p, "")
	c, err := m.Open(OpenRequest{Overrides: ov, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	msg := failedMessage(t, c)
	if !strings.Contains(msg, "SOCKS5 proxy 127.0.0.1:") || !strings.Contains(msg, "not allowed") {
		t.Fatalf("unhelpful refusal message: %q", msg)
	}
}

func TestProxySOCKS4And4a(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll

	p4 := startTestProxy(t, socks4Handler(false))
	runProxied(t, m, sink, proxiedOverrides(f, "socks4", p4, "bob"), "s4")
	target, user, _, ip := p4.last()
	if target != fmt.Sprintf("127.0.0.1:%d", f.port) || user != "bob" || ip != "127.0.0.1" {
		t.Fatalf("socks4 request: target=%q user=%q ip=%q", target, user, ip)
	}

	// SOCKS4a passes the host name through untouched
	p4a := startTestProxy(t, socks4Handler(false))
	ov := proxiedOverrides(f, "socks4a", p4a, "bob")
	ov["host"] = "localhost"
	runProxied(t, m, sink, ov, "s4a")
	target, _, _, ip = p4a.last()
	if target != fmt.Sprintf("localhost:%d", f.port) || ip != "0.0.0.1" {
		t.Fatalf("socks4a request: target=%q ip=%q", target, ip)
	}

	// refused by the proxy
	pr := startTestProxy(t, socks4Handler(true))
	c, _ := m.Open(OpenRequest{Overrides: proxiedOverrides(f, "socks4", pr, ""), Cols: 80, Rows: 24}, sink)
	msg := failedMessage(t, c)
	if !strings.Contains(msg, "SOCKS4 proxy") || !strings.Contains(msg, "rejected") {
		t.Fatalf("unhelpful refusal message: %q", msg)
	}
}

func TestProxyHTTPConnect(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll

	open := startTestProxy(t, httpHandler("", "", 0))
	runProxied(t, m, sink, proxiedOverrides(f, "http", open, ""), "http-open")
	target, user, _, _ := open.last()
	if target != fmt.Sprintf("127.0.0.1:%d", f.port) || user != "" {
		t.Fatalf("CONNECT request: target=%q user=%q", target, user)
	}

	auth := startTestProxy(t, httpHandler("carol", "hunter2", 0))
	if err := m.Store.Save("http-auth", proxiedOverrides(f, "http", auth, "carol")); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.Vault.Set(store.SecretKey("http-auth", "proxy"), "hunter2"); err != nil {
		t.Fatal(err)
	}
	c, _ := m.Open(OpenRequest{SessionPath: "http-auth", Cols: 80, Rows: 24}, sink)
	waitState(t, c, StateConnected)
	c.Input([]byte("echo http-ok-$((2+2))\n"))
	expect(t, c, 0, "http-ok-4")
	if _, u, pw, _ := auth.last(); u != "carol" || pw != "hunter2" {
		t.Fatalf("Basic credentials: %q %q", u, pw)
	}

	// wrong credentials: 407 is reported with its status text
	if err := m.Store.Vault.Set(store.SecretKey("http-auth", "proxy"), "wrong"); err != nil {
		t.Fatal(err)
	}
	c2, _ := m.Open(OpenRequest{SessionPath: "http-auth", Cols: 80, Rows: 24}, sink)
	msg := failedMessage(t, c2)
	if !strings.Contains(msg, "HTTP proxy 127.0.0.1:") || !strings.Contains(msg, "407 Proxy Authentication Required") {
		t.Fatalf("unhelpful 407 message: %q", msg)
	}
	// no username at all against an authenticating proxy
	c3, _ := m.Open(OpenRequest{Overrides: proxiedOverrides(f, "http", auth, ""), Cols: 80, Rows: 24}, sink)
	if msg := failedMessage(t, c3); !strings.Contains(msg, "407") {
		t.Fatalf("missing credentials message: %q", msg)
	}
	// forbidden target
	forbid := startTestProxy(t, httpHandler("", "", 403))
	c4, _ := m.Open(OpenRequest{Overrides: proxiedOverrides(f, "http", forbid, ""), Cols: 80, Rows: 24}, sink)
	if msg := failedMessage(t, c4); !strings.Contains(msg, "403 Forbidden") {
		t.Fatalf("403 message: %q", msg)
	}
}

func TestProxyRawTCPAndMisconfiguration(t *testing.T) {
	m, sink := newTestManager(t)
	echo := startEcho(t)
	p := startTestProxy(t, socks5Handler("", "", false))
	ov := map[string]any{"protocol": "raw", "host": "127.0.0.1", "port": echo, "firewall": "proxy",
		"proxy": map[string]any{"type": "socks5", "host": "127.0.0.1", "port": p.port()}}
	c, err := m.Open(OpenRequest{Overrides: ov, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	c.Input([]byte("ping-through-proxy\n"))
	expect(t, c, 0, "ping-through-proxy")
	if p.count() != 1 {
		t.Fatalf("proxy not used: %d", p.count())
	}
	// no proxy host / unknown type
	for _, px := range []map[string]any{{"type": "socks5", "host": ""}, {"type": "carrier-pigeon", "host": "127.0.0.1", "port": 1}} {
		ov["proxy"] = px
		c, _ := m.Open(OpenRequest{Overrides: ov}, sink)
		msg := failedMessage(t, c)
		if !strings.Contains(msg, "proxy") {
			t.Fatalf("message should mention the proxy: %q", msg)
		}
	}
	// proxy not listening
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	dp := dead.Addr().(*net.TCPAddr).Port
	_ = dead.Close()
	ov["proxy"] = map[string]any{"type": "http", "host": "127.0.0.1", "port": dp}
	c2, _ := m.Open(OpenRequest{Overrides: ov}, sink)
	if msg := failedMessage(t, c2); !strings.Contains(msg, "cannot connect to 127.0.0.1:") {
		t.Fatalf("dead proxy message: %q", msg)
	}
}

// startEcho runs a TCP echo server and returns its port.
func startEcho(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}
