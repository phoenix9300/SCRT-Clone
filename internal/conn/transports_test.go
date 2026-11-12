package conn

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------------ test peers

// wire accumulates everything a fake server received from the client.
type wire struct {
	mu  sync.Mutex
	buf []byte
}

func (w *wire) add(p []byte) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	w.mu.Unlock()
}

func (w *wire) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf...)
}

// waitFor blocks until the wire contains sub (at or after from) and returns
// the offset just past it.
func (w *wire) waitFor(t *testing.T, from int, sub []byte) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b := w.bytes()
		if from <= len(b) {
			if i := bytes.Index(b[from:], sub); i >= 0 {
				return from + i + len(sub)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never received % x; wire so far: % x", sub, w.bytes())
	return 0
}

type peerConn struct {
	net.Conn
	w wire
}

type peer struct {
	ln    net.Listener
	conns chan *peerConn
}

func (p *peer) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *peer) next(t *testing.T) *peerConn {
	t.Helper()
	select {
	case c := <-p.conns:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no connection reached the server")
		return nil
	}
}

// startPeer listens (optionally with TLS), sends greet to each client and
// records what it sends back.
func startPeer(t *testing.T, tlsCfg *tls.Config, greet []byte) *peer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	p := &peer{ln: ln, conns: make(chan *peerConn, 8)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			pc := &peerConn{Conn: c}
			go func() {
				defer c.Close()
				if len(greet) > 0 {
					if _, err := c.Write(greet); err != nil {
						return // e.g. the TLS handshake failed
					}
				}
				p.conns <- pc
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						pc.w.add(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return p
}

func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hawser-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func openConn(t *testing.T, m *Manager, sink *testSink, ov map[string]any, cols, rows int) *Conn {
	t.Helper()
	c, err := m.Open(OpenRequest{Overrides: ov, Cols: cols, Rows: rows}, sink)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ------------------------------------------------------------------- raw

func TestRawTCPAndTLS(t *testing.T) {
	m, sink := newTestManager(t)
	port := startEcho(t)
	c := openConn(t, m, sink, map[string]any{"protocol": "raw", "host": "127.0.0.1", "port": port}, 80, 24)
	waitState(t, c, StateConnected)
	c.Input([]byte("raw-echo-1\n"))
	expect(t, c, 0, "raw-echo-1")
	if d := c.Info(false).Details; d["remoteAddress"] != "127.0.0.1:"+itoa(port) || d["tlsVersion"] != "" {
		t.Fatalf("details: %v", d)
	}
	c.Resize(100, 30) // no-op for raw, must not fail or break the stream
	c.Input([]byte("raw-echo-2\n"))
	expect(t, c, 0, "raw-echo-2")

	// TLS: self-signed certificate
	cfg := selfSignedTLS(t)
	tp := startTLSEcho(t, cfg)
	ov := func(verify bool) map[string]any {
		return map[string]any{"protocol": "raw", "host": "127.0.0.1", "port": tp,
			"raw": map[string]any{"tls": true, "tlsVerify": verify}}
	}
	bad := openConn(t, m, sink, ov(true), 80, 24)
	waitState(t, bad, StateDisconnected)
	inf := bad.Info(false)
	if !inf.Failed || !strings.Contains(inf.Message, "TLS handshake failed") || !strings.Contains(inf.Message, "certificate") {
		t.Fatalf("verification failure message: %q (failed=%v)", inf.Message, inf.Failed)
	}
	good := openConn(t, m, sink, ov(false), 80, 24)
	waitState(t, good, StateConnected)
	good.Input([]byte("tls-echo\n"))
	expect(t, good, 0, "tls-echo")
	d := good.Info(false).Details
	if !strings.HasPrefix(d["tlsVersion"], "TLS 1.") || d["tlsCipher"] == "" {
		t.Fatalf("TLS details missing: %v", d)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func startTLSEcho(t *testing.T, cfg *tls.Config) int {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
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
			go func() {
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						_ = c.Close()
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// ---------------------------------------------------------------- telnet

func nawsSB(cols, rows int) []byte {
	out := []byte{tnIAC, tnSB, optNAWS}
	for _, v := range []int{cols >> 8, cols & 0xff, rows >> 8, rows & 0xff} {
		out = append(out, byte(v))
		if byte(v) == tnIAC {
			out = append(out, tnIAC)
		}
	}
	return append(out, tnIAC, tnSE)
}

func TestTelnetTLSAndWire(t *testing.T) {
	cfg := selfSignedTLS(t)
	greet := []byte{tnIAC, tnDO, optNAWS, tnIAC, tnDO, optTType, tnIAC, tnWILL, optSGA, tnIAC, tnSB, optTType, tnSEND, tnIAC, tnSE, 'l', 'o', 'g', 'i', 'n', ':', ' '}
	p := startPeer(t, cfg, greet)
	m, sink := newTestManager(t)
	ov := func(telnet map[string]any) map[string]any {
		return map[string]any{"protocol": "telnet-tls", "host": "127.0.0.1", "port": p.port(), "telnet": telnet}
	}

	// certificate verification is on by default
	bad := openConn(t, m, sink, ov(map[string]any{"tlsVerify": true}), 80, 24)
	waitState(t, bad, StateDisconnected)
	if msg := bad.Info(false).Message; !strings.Contains(msg, "TLS handshake failed") || !strings.Contains(msg, "certificate") {
		t.Fatalf("verification failure message: %q", msg)
	}

	c := openConn(t, m, sink, ov(map[string]any{"tlsVerify": false}), 100, 30)
	waitState(t, c, StateConnected)
	expect(t, c, 0, "login: ")
	if d := c.Info(false).Details; !strings.HasPrefix(d["tlsVersion"], "TLS 1.") || !strings.Contains(d["telnetOptions"], "NAWS") {
		t.Fatalf("details: %v", d)
	}
	pc := p.next(t)
	// negotiation: NAWS announced with the initial size, terminal type answered
	off := pc.w.waitFor(t, 0, []byte{tnIAC, tnWILL, optNAWS})
	off = pc.w.waitFor(t, off, nawsSB(100, 30))
	pc.w.waitFor(t, 0, append(append([]byte{tnIAC, tnSB, optTType, tnIS}, "XTERM-256COLOR"...), tnIAC, tnSE))
	pc.w.waitFor(t, 0, []byte{tnIAC, tnWILL, optTType})

	// resize sends a fresh NAWS; values equal to 255 are doubled
	c.Resize(120, 40)
	off = pc.w.waitFor(t, off, nawsSB(120, 40))
	c.Resize(255, 300)
	off = pc.w.waitFor(t, off, []byte{tnIAC, tnSB, optNAWS, 0, 255, 255, 1, 44, tnIAC, tnSE})

	// 0xFF in user input is doubled; a bare CR becomes CR NUL, CR LF is kept
	c.Input([]byte("a\xffb"))
	off = pc.w.waitFor(t, off, []byte{'a', tnIAC, tnIAC, 'b'})
	c.Input([]byte("x\ry"))
	off = pc.w.waitFor(t, off, []byte{'x', '\r', 0, 'y'})
	c.Input([]byte("z\r\nq"))
	off = pc.w.waitFor(t, off, []byte{'z', '\r', '\n', 'q'})

	// BREAK and keep-alive
	if err := c.SendBreak(); err != nil {
		t.Fatal(err)
	}
	off = pc.w.waitFor(t, off, []byte{tnIAC, tnBRK})
	c.mu.Lock()
	tr := c.transport
	c.mu.Unlock()
	ka, ok := tr.(KeepAliver)
	if !ok {
		t.Fatal("telnet transport has no KeepAlive")
	}
	if err := ka.KeepAlive(); err != nil {
		t.Fatal(err)
	}
	pc.w.waitFor(t, off, []byte{tnIAC, tnNOP})

	// the ctrl-c variant of BREAK
	c2 := openConn(t, m, sink, ov(map[string]any{"tlsVerify": false, "breakSends": "ctrl-c"}), 80, 24)
	waitState(t, c2, StateConnected)
	pc2 := p.next(t)
	pc2.w.waitFor(t, 0, []byte{tnIAC, tnWILL, optNAWS})
	if err := c2.SendBreak(); err != nil {
		t.Fatal(err)
	}
	o2 := pc2.w.waitFor(t, 0, []byte{0x03})
	time.Sleep(50 * time.Millisecond)
	if bytes.Contains(pc2.w.bytes()[o2-1:], []byte{tnIAC, tnBRK}) || bytes.Contains(pc2.w.bytes(), []byte{tnIAC, tnBRK}) {
		t.Fatal("ctrl-c variant must not send IAC BRK")
	}
}

func TestTelnetBinaryAndPassiveStart(t *testing.T) {
	p := startPeer(t, nil, []byte{tnIAC, tnDO, optBinary})
	m, sink := newTestManager(t)
	c := openConn(t, m, sink, map[string]any{"protocol": "telnet", "host": "127.0.0.1", "port": p.port()}, 80, 24)
	waitState(t, c, StateConnected)
	pc := p.next(t)
	off := pc.w.waitFor(t, 0, []byte{tnIAC, tnWILL, optBinary})
	// on a non-standard port the client does not open negotiation itself
	if bytes.Contains(pc.w.bytes(), []byte{tnIAC, tnDO, optSGA}) {
		t.Fatalf("client initiated negotiation on a custom port: % x", pc.w.bytes())
	}
	// with BINARY on, CR is sent as is
	c.Input([]byte("x\ry"))
	pc.w.waitFor(t, off, []byte{'x', '\r', 'y'})
	// a server that says DONT BINARY switches it off again
	_, _ = pc.Write([]byte{tnIAC, tnDONT, optBinary})
	pc.w.waitFor(t, off, []byte{tnIAC, tnWONT, optBinary})
	c.Input([]byte("p\rq"))
	pc.w.waitFor(t, off, []byte{'p', '\r', 0, 'q'})
	// unsupported options are refused
	_, _ = pc.Write([]byte{tnIAC, tnDO, optLFlow, tnIAC, tnWILL, optStatus})
	pc.w.waitFor(t, off, []byte{tnIAC, tnWONT, optLFlow})
	pc.w.waitFor(t, off, []byte{tnIAC, tnDONT, optStatus})
	// received CR NUL is delivered as a single CR
	_, _ = pc.Write([]byte("one\r\x00two"))
	expect(t, c, 0, "one\rtwo")
}

// ---------------------------------------------------------------- rlogin

func TestRlogin(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type seen struct {
		hello string
		win   [][]byte
	}
	got := make(chan *seen, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(20 * time.Second))
		s := &seen{}
		var hb []byte
		one := make([]byte, 1)
		for bytes.Count(hb, []byte{0}) < 4 {
			if _, err := c.Read(one); err != nil {
				return
			}
			hb = append(hb, one[0])
		}
		s.hello = string(hb)
		_, _ = c.Write([]byte{0})
		readWin := func() bool {
			w := make([]byte, 12)
			n := 0
			for n < 12 {
				k, err := c.Read(w[n:])
				if err != nil {
					return false
				}
				n += k
			}
			s.win = append(s.win, w)
			return true
		}
		if !readWin() {
			return
		}
		got <- s
		// echo, but treat further window messages separately
		buf := make([]byte, 256)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			_, _ = c.Write(buf[:n])
		}
	}()
	m, sink := newTestManager(t)
	port := ln.Addr().(*net.TCPAddr).Port
	c := openConn(t, m, sink, map[string]any{"protocol": "rlogin", "host": "127.0.0.1", "port": port, "username": "remoteguy",
		"rlogin": map[string]any{"localUser": "localguy", "terminalSpeed": "9600"}}, 100, 40)
	waitState(t, c, StateConnected)
	var s *seen
	select {
	case s = <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("rlogin server saw no handshake")
	}
	if want := "\x00localguy\x00remoteguy\x00xterm-256color/9600\x00"; s.hello != want {
		t.Fatalf("handshake %q, want %q", s.hello, want)
	}
	if want := []byte{0xff, 0xff, 's', 's', 0, 40, 0, 100, 0, 0, 0, 0}; !bytes.Equal(s.win[0], want) {
		t.Fatalf("window size message % x, want % x", s.win[0], want)
	}
	c.Input([]byte("hello-rlogin\n"))
	expect(t, c, 0, "hello-rlogin")
	// resize: the control sequence is written to the stream and (as this
	// server simply echoes) comes straight back, so check the echo bytes
	c.Resize(132, 50)
	want := []byte{0xff, 0xff, 's', 's', 0, 50, 0, 132, 0, 0, 0, 0}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(sink.text(c.ID), string(want)) {
		if time.Now().After(deadline) {
			t.Fatalf("no resize message on the wire; got %q", sink.text(c.ID))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := c.Info(false).Details; d["remoteAddress"] == "" {
		t.Fatalf("details: %v", d)
	}
}

func TestRloginRefused(t *testing.T) {
	p := startPeer(t, nil, nil)
	m, sink := newTestManager(t)
	go func() {
		pc := <-p.conns
		// reply once the handshake has arrived
		for i := 0; i < 500 && bytes.Count(pc.w.bytes(), []byte{0}) < 4; i++ {
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = pc.Write([]byte("\x01rlogind: Permission denied.\r\n"))
	}()
	c := openConn(t, m, sink, map[string]any{"protocol": "rlogin", "host": "127.0.0.1", "port": p.port(), "username": "u"}, 80, 24)
	msg := failedMessage(t, c)
	if msg != "rlogin refused: rlogind: Permission denied." {
		t.Fatalf("refusal message %q", msg)
	}
}

// ---------------------------------------------------------------- serial

var ptyRe = regexp.MustCompile(`PTY is (/dev/\S+)`)

// startPtyPair runs socat to create two linked pseudo-terminals.
func startPtyPair(t *testing.T) (string, string) {
	t.Helper()
	socat, err := exec.LookPath("socat")
	if err != nil {
		t.Skip("socat not installed")
	}
	cmd := exec.Command(socat, "-d", "-d", "pty,raw,echo=0", "pty,raw,echo=0")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot run socat: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	names := make(chan string, 2)
	go func() {
		buf := make([]byte, 4096)
		var acc string
		for {
			n, err := stderr.Read(buf)
			acc += string(buf[:n])
			for _, mm := range ptyRe.FindAllStringSubmatch(acc, -1) {
				names <- mm[1]
			}
			if err != nil {
				close(names)
				return
			}
		}
	}()
	var out []string
	timeout := time.After(10 * time.Second)
	for len(out) < 2 {
		select {
		case n, ok := <-names:
			if !ok {
				t.Skip("socat did not report two pseudo-terminals")
			}
			out = append(out, n)
		case <-timeout:
			t.Skip("socat did not report pseudo-terminals in time")
		}
	}
	return out[0], out[1]
}

func TestSerialPty(t *testing.T) {
	a, b := startPtyPair(t)
	m, sink := newTestManager(t)
	c := openConn(t, m, sink, map[string]any{"protocol": "serial", "serial": map[string]any{"port": a, "baud": 115200, "parity": "none", "stopBits": "1", "dataBits": 8}}, 80, 24)
	deadline := time.Now().Add(15 * time.Second)
	for c.State() == StateConnecting && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if c.State() != StateConnected {
		t.Skipf("serial library cannot open this pty: %s", c.Info(false).Message)
	}
	d := c.Info(false).Details
	if d["port"] != a || !strings.HasPrefix(d["mode"], "115200 8N1") {
		t.Fatalf("details: %v", d)
	}
	if tg := c.Info(false).Target; !strings.Contains(tg, a) || !strings.Contains(tg, "115200") {
		t.Fatalf("target: %q", tg)
	}
	other, err := os.OpenFile(b, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// peer -> terminal
	if _, err := other.Write([]byte("from-peer\r\n")); err != nil {
		t.Fatal(err)
	}
	expect(t, c, 0, "from-peer")
	// terminal -> peer
	c.Input([]byte("to-peer\r"))
	got := make(chan string, 1)
	go func() {
		var acc []byte
		buf := make([]byte, 256)
		for !bytes.Contains(acc, []byte("to-peer\r")) {
			n, err := other.Read(buf)
			if err != nil {
				break
			}
			acc = append(acc, buf[:n]...)
		}
		got <- string(acc)
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, "to-peer\r") {
			t.Fatalf("peer received %q", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("peer received nothing")
	}
	c.Resize(100, 30) // must be harmless on a serial line
	c.Disconnect()
	waitState(t, c, StateDisconnected)
}

func TestSerialErrors(t *testing.T) {
	m, sink := newTestManager(t)
	c := openConn(t, m, sink, map[string]any{"protocol": "serial", "serial": map[string]any{"port": "/dev/hawser-no-such-port", "baud": 9600}}, 80, 24)
	if msg := failedMessage(t, c); !strings.Contains(msg, "cannot open /dev/hawser-no-such-port") {
		t.Fatalf("invalid port message: %q", msg)
	}
	c2 := openConn(t, m, sink, map[string]any{"protocol": "serial", "serial": map[string]any{"port": ""}}, 80, 24)
	if msg := failedMessage(t, c2); !strings.Contains(msg, "no serial port selected") {
		t.Fatalf("empty port message: %q", msg)
	}
}

// ------------------------------------------------------------------- X11

func TestParseDisplay(t *testing.T) {
	type want struct {
		network, addr string
		screen        uint32
	}
	ok := map[string]want{
		":0":         {"unix", "/tmp/.X11-unix/X0", 0},
		":12.3":      {"unix", "/tmp/.X11-unix/X12", 3},
		"unix:1.2":   {"unix", "/tmp/.X11-unix/X1", 2},
		"host:10.1":  {"tcp", "host:6010", 1},
		"10.0.0.5:2": {"tcp", "10.0.0.5:6002", 0},
		"::1:0":      {"tcp", "[::1]:6000", 0},
		"[::1]:1":    {"tcp", "[::1]:6001", 0},
		"/private/tmp/com.apple.launchd.AbC/org.xquartz:0": {"unix", "/private/tmp/com.apple.launchd.AbC/org.xquartz", 0},
		"/tmp/xsock": {"unix", "/tmp/xsock", 0},
	}
	for in, w := range ok {
		n, a, s, err := parseDisplay(in)
		if err != nil || n != w.network || a != w.addr || s != w.screen {
			t.Errorf("parseDisplay(%q) = %q %q %d %v, want %+v", in, n, a, s, err, w)
		}
	}
	for _, in := range []string{"nodisplay", "host:abc", ":", "host:", ":x.1"} {
		if _, _, _, err := parseDisplay(in); err == nil || !strings.Contains(err.Error(), "malformed display") {
			t.Errorf("parseDisplay(%q) should be malformed, got %v", in, err)
		}
	}
	t.Setenv("DISPLAY", "")
	if _, _, _, err := parseDisplay(""); err == nil || !strings.Contains(err.Error(), "DISPLAY") {
		t.Errorf("empty display without $DISPLAY: %v", err)
	}
	t.Setenv("DISPLAY", ":7")
	if n, a, _, err := parseDisplay(""); err != nil || n != "unix" || a != "/tmp/.X11-unix/X7" {
		t.Errorf("$DISPLAY fallback: %q %q %v", n, a, err)
	}
}

func x11Setup(order byte, name string, cookie []byte) []byte {
	pad := func(n int) int { return (n + 3) &^ 3 }
	hdr := make([]byte, 12)
	hdr[0] = order
	put := func(off int, v int) {
		if order == 'B' {
			hdr[off], hdr[off+1] = byte(v>>8), byte(v)
		} else {
			hdr[off], hdr[off+1] = byte(v), byte(v>>8)
		}
	}
	put(2, 11)
	put(6, len(name))
	put(8, len(cookie))
	body := make([]byte, pad(len(name))+pad(len(cookie)))
	copy(body, name)
	copy(body[pad(len(name)):], cookie)
	return append(hdr, body...)
}

func TestX11Handshake(t *testing.T) {
	fake := bytes.Repeat([]byte{0xaa}, 16)
	real := bytes.Repeat([]byte{0x55}, 16)
	for _, order := range []byte{'l', 'B'} {
		st := &x11State{fakeCookie: fake, realCookie: real}
		var out bytes.Buffer
		in := append(x11Setup(order, x11AuthProto, fake), "trailing-request"...)
		rd := bytes.NewReader(in)
		if err := x11Handshake(rd, &out, st); err != nil {
			t.Fatalf("order %c: %v", order, err)
		}
		if want := x11Setup(order, x11AuthProto, real); !bytes.Equal(out.Bytes(), want) {
			t.Fatalf("order %c: cookie not replaced\n got % x\nwant % x", order, out.Bytes(), want)
		}
		if rd.Len() != len("trailing-request") {
			t.Fatalf("handshake consumed %d bytes beyond the setup block", len("trailing-request")-rd.Len())
		}
	}
	// wrong cookie / wrong protocol are rejected and nothing is forwarded
	st := &x11State{fakeCookie: fake, realCookie: real}
	for _, in := range [][]byte{
		x11Setup('l', x11AuthProto, bytes.Repeat([]byte{1}, 16)),
		x11Setup('l', "XDM-AUTHORIZATION-1", fake),
		x11Setup('l', x11AuthProto, nil),
		{'Z', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		x11Setup('l', x11AuthProto, fake)[:14], // truncated
	} {
		var out bytes.Buffer
		if err := x11Handshake(bytes.NewReader(in), &out, st); err == nil || out.Len() != 0 {
			t.Errorf("setup % x should be rejected (err=%v, forwarded %d bytes)", in, err, out.Len())
		}
	}
	// without a local cookie the authorisation is stripped
	st = &x11State{fakeCookie: fake}
	var out bytes.Buffer
	if err := x11Handshake(bytes.NewReader(x11Setup('l', x11AuthProto, fake)), &out, st); err != nil {
		t.Fatal(err)
	}
	if want := x11Setup('l', "", nil); !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("authorisation not stripped: % x", out.Bytes())
	}
}
