package xfer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// leakCheck fails the test if goroutines started during it are still running
// when it ends.
func leakCheck(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			n := runtime.NumGoroutine()
			if n <= before {
				return
			}
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<20)
				buf = buf[:runtime.Stack(buf, true)]
				t.Errorf("goroutine leak: %d before, %d after\n%s", before, n, buf)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

// link is one direction of an in-memory connection: a bounded byte queue
// with blocking writes, timed reads and optional fault injection.
type link struct {
	mu     sync.Mutex
	buf    []byte
	limit  int
	fault  func([]byte) []byte
	total  int64 // bytes accepted from the writer
	notify chan struct{}
	space  chan struct{}
}

func newLink(limit int) *link {
	return &link{limit: limit, notify: make(chan struct{}, 1), space: make(chan struct{}, 1)}
}

func (l *link) write(p []byte) (int, error) {
	n := len(p)
	l.mu.Lock()
	l.total += int64(n)
	if l.fault != nil {
		p = l.fault(append([]byte(nil), p...))
	}
	l.mu.Unlock()
	for len(p) > 0 {
		l.mu.Lock()
		room := l.limit - len(l.buf)
		if room > 0 {
			k := min(room, len(p))
			l.buf = append(l.buf, p[:k]...)
			p = p[k:]
			l.mu.Unlock()
			signal(l.notify)
			continue
		}
		l.mu.Unlock()
		select {
		case <-l.space:
		case <-time.After(10 * time.Millisecond):
		}
	}
	return n, nil
}

func (l *link) read(p []byte, d time.Duration) (int, error) {
	var timer *time.Timer
	for {
		l.mu.Lock()
		if n := copy(p, l.buf); n > 0 {
			l.buf = l.buf[:copy(l.buf, l.buf[n:])]
			l.mu.Unlock()
			signal(l.space)
			return n, nil
		}
		l.mu.Unlock()
		if d <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		if timer == nil {
			timer = time.NewTimer(d)
			defer timer.Stop()
		}
		select {
		case <-l.notify:
		case <-timer.C:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// unbound lifts the size limit, so that a writer is never blocked by a reader
// that has returned. The data is kept: tests inspect what was left unread.
func (l *link) unbound() {
	l.mu.Lock()
	l.limit = math.MaxInt
	l.mu.Unlock()
	signal(l.space)
}

func (l *link) written() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// endpoint is one side of an in-memory duplex connection. It implements
// Stream and Unreader.
type endpoint struct {
	in, out *link
	// tap, if set, sees every Write before it is sent and may replace it.
	tap func([]byte) []byte
}

func (e *endpoint) Write(p []byte) (int, error) {
	n := len(p)
	if e.tap != nil {
		p = e.tap(p)
	}
	_, err := e.out.write(p)
	return n, err
}

func (e *endpoint) ReadTimeout(p []byte, d time.Duration) (int, error) {
	return e.in.read(p, d)
}

func (e *endpoint) Unread(p []byte) {
	e.in.mu.Lock()
	e.in.buf = append(append([]byte(nil), p...), e.in.buf...)
	e.in.mu.Unlock()
	signal(e.in.notify)
}

// readAll returns everything that arrives within the quiet period.
func (e *endpoint) readAll(quiet time.Duration) []byte {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := e.in.read(buf, quiet)
		out = append(out, buf[:n]...)
		if err != nil {
			return out
		}
	}
}

// newDuplex returns the two ends of an in-memory connection whose
// directions each buffer at most limit bytes.
func newDuplex(limit int) (a, b *endpoint) {
	ab, ba := newLink(limit), newLink(limit)
	return &endpoint{in: ba, out: ab}, &endpoint{in: ab, out: ba}
}

// noise returns a fault injector that flips a bit with probability flip and
// drops a byte with probability drop, per byte. It counts what it did.
type noise struct {
	rng        *rand.Rand
	flip, drop float64
	flips      int
	drops      int
}

func newNoise(seed uint64, flip, drop float64) *noise {
	return &noise{rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), flip: flip, drop: drop}
}

func (n *noise) apply(p []byte) []byte {
	out := p[:0]
	for _, c := range p {
		r := n.rng.Float64()
		switch {
		case r < n.drop:
			n.drops++
			continue
		case r < n.drop+n.flip:
			n.flips++
			c ^= 1 << n.rng.IntN(8)
		}
		out = append(out, c)
	}
	return out
}

// runPair runs the two sides of a transfer concurrently and returns their
// results. A side that has returned stops consuming, so its link becomes
// unbounded to keep the other side from blocking forever.
func runPair(t *testing.T, a, b *endpoint, fa, fb func() error) (errA, errB error) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errA = fa()
		a.in.unbound()
	}()
	go func() {
		defer wg.Done()
		errB = fb()
		b.in.unbound()
	}()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("transfer did not finish\n%s", buf)
	}
	return errA, errB
}

// memFile is a file kept in memory.
type memFile struct {
	info FileInfo
	data []byte
}

func (f memFile) sendFile() SendFile {
	return SendFile{
		Info: f.info,
		Open: func() (io.ReadSeekCloser, error) {
			return readSeekNopCloser{bytes.NewReader(f.data)}, nil
		},
	}
}

type readSeekNopCloser struct{ *bytes.Reader }

func (readSeekNopCloser) Close() error { return nil }

func sendFiles(files []memFile) []SendFile {
	out := make([]SendFile, len(files))
	for i, f := range files {
		out[i] = f.sendFile()
	}
	return out
}

// memSink collects received files in memory.
type memSink struct {
	mu     sync.Mutex
	files  []*memSinkFile
	skip   map[string]bool   // names to decline with ErrSkip
	resume map[string][]byte // partial content already present
	fail   error             // returned by Create if set
}

type memSinkFile struct {
	info   FileInfo
	buf    bytes.Buffer
	offset int64
	closed bool
}

func (f *memSinkFile) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *memSinkFile) Close() error                { f.closed = true; return nil }

func (s *memSink) Create(info FileInfo) (io.WriteCloser, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, 0, s.fail
	}
	if s.skip[info.Name] {
		return nil, 0, ErrSkip
	}
	f := &memSinkFile{info: info}
	if part, ok := s.resume[info.Name]; ok {
		f.buf.Write(part)
		f.offset = int64(len(part))
	}
	s.files = append(s.files, f)
	return f, f.offset, nil
}

func (s *memSink) get(name string) *memSinkFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		if f.info.Name == name {
			return f
		}
	}
	return nil
}

// eventLog records transfer events.
type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(e Event) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) kinds(skipProgress bool) []EventKind {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []EventKind
	for _, e := range l.events {
		if skipProgress && e.Kind == Progress {
			continue
		}
		out = append(out, e.Kind)
	}
	return out
}

func (l *eventLog) count(kind EventKind) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// pattern returns n deterministic pseudo-random bytes.
func pattern(seed uint64, n int) []byte {
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	out := make([]byte, n)
	for i := 0; i+8 <= n; i += 8 {
		v := rng.Uint64()
		for j := range 8 {
			out[i+j] = byte(v >> (8 * j))
		}
	}
	for i := n &^ 7; i < n; i++ {
		out[i] = byte(rng.Uint32())
	}
	return out
}

// nastyBytes is binary data built to exercise every escaping rule: all byte
// values in both orders, runs of ZDLE, XON, XOFF, DLE with and without the
// high bit, CR after '@', and the frame-end letters after ZDLE.
func nastyBytes() []byte {
	var b []byte
	for i := range 256 {
		b = append(b, byte(i))
	}
	for i := 255; i >= 0; i-- {
		b = append(b, byte(i))
	}
	for _, c := range []byte{0x18, 0x11, 0x13, 0x10, 0x91, 0x93, 0x90, 0x0d, 0x8d, 0x7f, 0xff, 0x00, 0x1a} {
		b = append(b, bytes.Repeat([]byte{c}, 17)...)
	}
	b = append(b, "@\r@\x8d\xc0\r\xc0\x8d**\x18B00000000000000\r\x8a\x11"...)
	b = append(b, "\x18h\x18i\x18j\x18k\x18l\x18m\x18\x18\x18\x18\x18\x18\x18\x18\x18\x18"...)
	b = append(b, "*\x18A\x00\x00\x00\x00\x00*\x18C\x01\x02\x03rz\r**\x18B01"...)
	for i := range 256 {
		b = append(b, 0x18, byte(i), '@', byte(i), '*', byte(i))
	}
	return b
}

var testTime = time.Unix(1700000000, 0)

// batch is the standard multi-file test batch.
func batch() []memFile {
	return []memFile{
		{FileInfo{Name: "empty.dat", Size: 0, ModTime: testTime, Mode: 0o644}, nil},
		{FileInfo{Name: "one.dat", Size: 1, ModTime: testTime.Add(time.Hour), Mode: 0o600}, []byte{0x18}},
		{FileInfo{Name: "nasty.bin", Size: -1, ModTime: testTime, Mode: 0o755}, nastyBytes()},
		{FileInfo{Name: "exact-1024.bin", Size: 1024, ModTime: testTime, Mode: 0o644}, pattern(1, 1024)},
		{FileInfo{Name: "random.bin", Size: 150000, ModTime: testTime, Mode: 0o640}, pattern(2, 150000)},
		{FileInfo{Name: "text file with spaces.txt", Size: 27, ModTime: testTime, Mode: 0o444}, []byte("hello, world\r\nsecond line\r\n")},
	}
}

// checkBatch verifies that the sink holds exactly the given files.
func checkBatch(t *testing.T, sink *memSink, want []memFile, meta bool) {
	t.Helper()
	if len(sink.files) != len(want) {
		t.Errorf("received %d files, want %d", len(sink.files), len(want))
	}
	for _, w := range want {
		got := sink.get(w.info.Name)
		if got == nil {
			t.Errorf("%s: not received", w.info.Name)
			continue
		}
		if !bytes.Equal(got.buf.Bytes(), w.data) {
			t.Errorf("%s: content differs (got %d bytes, want %d)%s", w.info.Name, got.buf.Len(), len(w.data), firstDiff(got.buf.Bytes(), w.data))
		}
		if !got.closed {
			t.Errorf("%s: writer not closed", w.info.Name)
		}
		if !meta {
			continue
		}
		if got.info.Size != int64(len(w.data)) {
			t.Errorf("%s: announced size %d, want %d", w.info.Name, got.info.Size, len(w.data))
		}
		if !got.info.ModTime.Equal(w.info.ModTime) {
			t.Errorf("%s: mtime %v, want %v", w.info.Name, got.info.ModTime, w.info.ModTime)
		}
		if got.info.Mode != w.info.Mode {
			t.Errorf("%s: mode %o, want %o", w.info.Name, got.info.Mode, w.info.Mode)
		}
	}
}

func firstDiff(a, b []byte) string {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return fmt.Sprintf(", first difference at offset %d", i)
		}
	}
	return ""
}

func isErr(err, target error) bool { return errors.Is(err, target) }

// hookWriter calls fn once after at bytes have been written to it.
type hookWriter struct {
	n, at int64
	fn    func()
}

func (w *hookWriter) Write(p []byte) (int, error) {
	if w.n < w.at && w.n+int64(len(p)) >= w.at {
		w.fn()
	}
	w.n += int64(len(p))
	return len(p), nil
}

func (w *hookWriter) Close() error { return nil }

// hookReader calls fn once after at bytes have been read from it.
type hookReader struct {
	*bytes.Reader
	n, at int64
	fn    func()
}

func (r *hookReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.n < r.at && r.n+int64(n) >= r.at {
		r.fn()
	}
	r.n += int64(n)
	return n, err
}

func (r *hookReader) Close() error { return nil }

// sinkFunc adapts a function to the Sink interface.
type sinkFunc func(FileInfo) (io.WriteCloser, int64, error)

func (f sinkFunc) Create(info FileInfo) (io.WriteCloser, int64, error) { return f(info) }
