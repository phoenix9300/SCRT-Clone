package tftp

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func randBytes(n int) []byte {
	rng := rand.New(rand.NewPCG(uint64(n), 7))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// startServer runs a server on a loopback port and returns its address.
func startServer(t *testing.T, s *Server) string {
	t.Helper()
	before := runtime.NumGoroutine()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if s.Timeout == 0 {
		s.Timeout = 200 * time.Millisecond
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(pc) }()
	for s.Addr() == nil {
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		if err := <-done; !errors.Is(err, ErrServerClosed) {
			t.Errorf("Serve returned %v", err)
		}
		for i := 0; runtime.NumGoroutine() > before; i++ {
			if i > 300 {
				t.Errorf("goroutine leak: %d before, %d after", before, runtime.NumGoroutine())
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return pc.LocalAddr().String()
}

func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	var events []Event
	addr := startServer(t, &Server{Root: root, AllowRead: true, AllowWrite: true, OnEvent: func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}})
	ctx := context.Background()
	opts := map[string]*Options{
		"plain":       nil,
		"blksize1428": {BlockSize: 1428},
		"blksize8":    {BlockSize: 8},
		"blksize-max": {BlockSize: 65464},
		"tsize":       {TSize: 1},
	}
	for name, o := range opts {
		for _, size := range []int{0, 1, 511, 512, 513, 1428, 70000} {
			if o != nil && o.BlockSize == 8 && size > 2000 {
				continue
			}
			data := randBytes(size)
			file := name + "-" + string(rune('a'+size%26)) + ".bin"
			n, err := Put(ctx, addr, file, bytes.NewReader(data), o)
			if err != nil || n != int64(size) {
				t.Fatalf("%s/%d: Put = %d, %v", name, size, n, err)
			}
			stored, err := os.ReadFile(filepath.Join(root, file))
			if err != nil || !bytes.Equal(stored, data) {
				t.Fatalf("%s/%d: stored file differs (%d bytes, %v)", name, size, len(stored), err)
			}
			var got bytes.Buffer
			n, err = Get(ctx, addr, "/"+file, &got, o)
			if err != nil || n != int64(size) || !bytes.Equal(got.Bytes(), data) {
				t.Fatalf("%s/%d: Get = %d, %v", name, size, n, err)
			}
			os.Remove(filepath.Join(root, file))
		}
	}
	// The server reports "done" just after the client has its last packet.
	var kinds map[string]int
	for i := 0; i < 200; i++ {
		kinds = map[string]int{}
		mu.Lock()
		for _, e := range events {
			kinds[e.Kind]++
			if e.Remote == "" || e.File == "" || e.Time.IsZero() {
				t.Errorf("incomplete event %+v", e)
			}
		}
		mu.Unlock()
		if kinds["done"] == kinds["rrq"]+kinds["wrq"] {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if kinds["rrq"] == 0 || kinds["rrq"] != kinds["wrq"] || kinds["done"] != kinds["rrq"]+kinds["wrq"] || kinds["error"] != 0 {
		t.Errorf("events: %v", kinds)
	}
}

// TestWraparound transfers more than 65535 blocks, so the 16-bit block
// number wraps.
func TestWraparound(t *testing.T) {
	root := t.TempDir()
	data := randBytes(8*66000 + 3)
	os.WriteFile(filepath.Join(root, "big.bin"), data, 0o644)
	addr := startServer(t, &Server{Root: root, AllowRead: true, AllowWrite: true})
	var got bytes.Buffer
	n, err := Get(context.Background(), addr, "big.bin", &got, &Options{BlockSize: 8})
	if err != nil || n != int64(len(data)) || !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("Get = %d, %v", n, err)
	}
	n, err = Put(context.Background(), addr, "up.bin", bytes.NewReader(data), &Options{BlockSize: 8})
	stored, _ := os.ReadFile(filepath.Join(root, "up.bin"))
	if err != nil || n != int64(len(data)) || !bytes.Equal(stored, data) {
		t.Fatalf("Put = %d, %v", n, err)
	}
}

func wantCode(t *testing.T, what string, err error, code uint16) {
	t.Helper()
	var te *Error
	if !errors.As(err, &te) || te.Code != code {
		t.Errorf("%s: err = %v, want TFTP error %d", what, err, code)
	}
}

func TestConfinementAndPermissions(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(base, "secret.txt"), []byte("secret"), 0o644)
	os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o644)
	os.WriteFile(filepath.Join(root, "sub", "deep.txt"), []byte("deep"), 0o644)
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(root, "link.txt"))
		os.Symlink(base, filepath.Join(root, "updir"))
	}
	ctx := context.Background()
	addr := startServer(t, &Server{Root: root, AllowRead: true, AllowWrite: true})

	var buf bytes.Buffer
	if _, err := Get(ctx, addr, "sub/deep.txt", &buf, nil); err != nil || buf.String() != "deep" {
		t.Errorf("subdirectory read: %q, %v", buf.String(), err)
	}
	for _, name := range []string{
		"../secret.txt", "/../secret.txt", "..\\secret.txt", "sub/../../secret.txt",
		"sub/../ok.txt", "..", "", "link.txt", "updir/secret.txt", filepath.Join(base, "secret.txt") + "/../secret.txt",
	} {
		buf.Reset()
		_, err := Get(ctx, addr, name, &buf, nil)
		if err == nil || buf.Len() != 0 {
			t.Errorf("Get(%q) succeeded with %q", name, buf.String())
		}
		if _, err := Put(ctx, addr, name, bytes.NewReader([]byte("pwned")), nil); err == nil && name != "" {
			t.Errorf("Put(%q) succeeded", name)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(base, "secret.txt")); string(b) != "secret" {
		t.Errorf("file outside the root was modified: %q", b)
	}
	_, err := Get(ctx, addr, "missing.txt", &buf, nil)
	wantCode(t, "missing file", err, CodeNotFound)
	_, err = Get(ctx, addr, "sub", &buf, nil)
	wantCode(t, "directory", err, CodeNotFound)
	_, err = Put(ctx, addr, "ok.txt", bytes.NewReader([]byte("new")), nil)
	wantCode(t, "overwrite", err, CodeFileExists)
	if b, _ := os.ReadFile(filepath.Join(root, "ok.txt")); string(b) != "ok" {
		t.Errorf("existing file was replaced: %q", b)
	}

	ro := startServer(t, &Server{Root: root, AllowRead: true})
	_, err = Put(ctx, ro, "new.txt", bytes.NewReader([]byte("x")), nil)
	wantCode(t, "write disabled", err, CodeAccessViolation)
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Error("file created although writing is disabled")
	}
	wo := startServer(t, &Server{Root: root, AllowWrite: true, AllowOverwrite: true})
	_, err = Get(ctx, wo, "ok.txt", &buf, nil)
	wantCode(t, "read disabled", err, CodeAccessViolation)
	if _, err := Put(ctx, wo, "ok.txt", bytes.NewReader([]byte("replaced")), nil); err != nil {
		t.Errorf("overwrite allowed: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "ok.txt")); string(b) != "replaced" {
		t.Errorf("file not replaced: %q", b)
	}
}

// lossyConn drops every nth packet in each direction.
type lossyConn struct {
	net.PacketConn
	n          int32
	sent, rcvd atomic.Int32
	dropped    *atomic.Int32
}

func (c *lossyConn) WriteTo(p []byte, a net.Addr) (int, error) {
	if c.sent.Add(1)%c.n == 0 {
		c.dropped.Add(1)
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, a)
}

func (c *lossyConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, a, err := c.PacketConn.ReadFrom(p)
		if err == nil && c.rcvd.Add(1)%(c.n+2) == 0 {
			c.dropped.Add(1)
			continue
		}
		return n, a, err
	}
}

// TestLostPackets drops packets on both the client and the server side;
// retransmission must deliver the files intact.
func TestLostPackets(t *testing.T) {
	root := t.TempDir()
	data := randBytes(40000)
	os.WriteFile(filepath.Join(root, "f.bin"), data, 0o644)
	var dropped atomic.Int32
	s := &Server{Root: root, AllowRead: true, AllowWrite: true, Timeout: 40 * time.Millisecond, Retries: 8}
	s.listen = func(a *net.UDPAddr) (net.PacketConn, error) {
		pc, err := net.ListenUDP("udp", a)
		return &lossyConn{PacketConn: pc, n: 7, dropped: &dropped}, err
	}
	addr := startServer(t, s)
	o := &Options{BlockSize: 1024, Timeout: 40 * time.Millisecond, Retries: 8, wrap: func(pc net.PacketConn) net.PacketConn {
		return &lossyConn{PacketConn: pc, n: 5, dropped: &dropped}
	}}
	var got bytes.Buffer
	if n, err := Get(context.Background(), addr, "f.bin", &got, o); err != nil || n != int64(len(data)) || !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("Get = %d, %v", n, err)
	}
	if n, err := Put(context.Background(), addr, "up.bin", bytes.NewReader(data), o); err != nil || n != int64(len(data)) {
		t.Fatalf("Put = %d, %v", n, err)
	}
	if stored, _ := os.ReadFile(filepath.Join(root, "up.bin")); !bytes.Equal(stored, data) {
		t.Fatalf("uploaded file differs (%d bytes)", len(stored))
	}
	if dropped.Load() < 10 {
		t.Fatalf("only %d packets dropped; the test proves nothing", dropped.Load())
	}
}

// TestDuplicateACK: duplicate acknowledgements must not make the server
// send data twice (Sorcerer's Apprentice Syndrome).
func TestDuplicateACK(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f.bin"), randBytes(512*20+100), 0o644)
	addr := startServer(t, &Server{Root: root, AllowRead: true, Timeout: 2 * time.Second})
	server, _ := net.ResolveUDPAddr("udp", addr)
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	pc.WriteTo(buildRequest(opRRQ, "f.bin", nil), server)
	buf := make([]byte, 600)
	packets := 0
	for {
		pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		packets++
		ack := ackPacket(uint16(buf[2])<<8 | uint16(buf[3]))
		pc.WriteTo(ack, from)
		pc.WriteTo(ack, from) // duplicate
		if n < 516 {
			pc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			if _, _, err := pc.ReadFrom(buf); err == nil {
				packets++
			}
			break
		}
	}
	if packets != 21 {
		t.Errorf("server sent %d DATA packets for 21 blocks", packets)
	}
}

func TestClientErrors(t *testing.T) {
	// Nobody listening: the client gives up after its retries.
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := pc.LocalAddr().String()
	pc.Close()
	start := time.Now()
	_, err := Get(context.Background(), addr, "x", &bytes.Buffer{}, &Options{Timeout: 20 * time.Millisecond, Retries: 2})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("Get from a dead server: %v after %v", err, time.Since(start))
	}
	// Cancellation interrupts a waiting client.
	pc, _ = net.ListenPacket("udp", "127.0.0.1:0")
	defer pc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = Put(ctx, pc.LocalAddr().String(), "x", bytes.NewReader([]byte("x")), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Put with an expiring context: %v", err)
	}
}
