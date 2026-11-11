package xfer

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// zmodemLoop runs ZmodemSend against ZmodemReceive over an in-memory link.
func zmodemLoop(t *testing.T, a, b *endpoint, files []SendFile, sink Sink, so, ro Options) (sendErr, recvErr error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return runPair(t, a, b,
		func() error { return ZmodemSend(ctx, a, files, so) },
		func() error { return ZmodemReceive(ctx, b, sink, ro) },
	)
}

func TestZmodemLoopback(t *testing.T) {
	cases := []struct {
		name     string
		send, rx Options
	}{
		{"default", Options{}, Options{}},
		{"crc16-sender", Options{CRC16: true}, Options{}},
		{"crc16-receiver", Options{}, Options{CRC16: true}},
		{"escape-sender", Options{EscapeControl: true}, Options{}},
		{"escape-receiver", Options{}, Options{EscapeControl: true}},
		{"escape-both-crc16", Options{EscapeControl: true, CRC16: true}, Options{EscapeControl: true}},
		{"window-4096", Options{WindowSize: 4096}, Options{}},
		{"window-tiny", Options{WindowSize: 300, BlockSize: 128}, Options{}},
		{"block-8192", Options{BlockSize: 8192}, Options{}},
		{"block-32", Options{BlockSize: 32}, Options{}},
		{"window-escape-8k", Options{WindowSize: 16384, BlockSize: 8192, EscapeControl: true}, Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			files := batch()
			sink := &memSink{}
			var sendLog, recvLog eventLog
			tc.send.OnEvent = sendLog.add
			tc.rx.OnEvent = recvLog.add
			serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, tc.send, tc.rx)
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			checkBatch(t, sink, files, true)

			// Both sides report each file once and finish with Done.
			var want []EventKind
			for range files {
				want = append(want, FileStart, FileDone)
			}
			want = append(want, Done)
			if got := sendLog.kinds(true); !slices.Equal(got, want) {
				t.Errorf("sender events = %v, want %v", got, want)
			}
			if got := recvLog.kinds(true); !slices.Equal(got, want) {
				t.Errorf("receiver events = %v, want %v", got, want)
			}
			// Nothing but protocol bytes was exchanged, and all of them were
			// consumed.
			if left := a.readAll(30 * time.Millisecond); len(left) != 0 {
				t.Errorf("sender left %q unread", left)
			}
			if left := b.readAll(30 * time.Millisecond); len(left) != 0 {
				t.Errorf("receiver left %q unread", left)
			}
		})
	}
}

func TestZmodemLargeFile(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(256 << 10)
	data := pattern(42, 6<<20+12345)
	files := []memFile{{FileInfo{Name: "large.bin", Size: int64(len(data)), ModTime: testTime, Mode: 0o644}, data}}
	sink := &memSink{}
	var log eventLog
	start := time.Now()
	serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, Options{BlockSize: 8192}, Options{OnEvent: log.add})
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	checkBatch(t, sink, files, true)
	elapsed := time.Since(start)
	t.Logf("%d bytes in %v (%.1f MB/s)", len(data), elapsed, float64(len(data))/elapsed.Seconds()/1e6)
	// Progress events are throttled to about ten per second.
	if n, limit := log.count(Progress), int(elapsed/(100*time.Millisecond))+2; n > limit {
		t.Errorf("%d progress events in %v, want at most %d", n, elapsed, limit)
	}
}

// TestZmodemLossy proves error recovery: the link corrupts and drops bytes
// in both directions and the file must still arrive intact.
func TestZmodemLossy(t *testing.T) {
	cases := []struct {
		name     string
		send, rx Options
		size     int
		fwd, rev [2]float64 // flip, drop probability per byte
	}{
		{"streaming", Options{}, Options{}, 400000, [2]float64{1.0 / 20000, 1.0 / 60000}, [2]float64{1.0 / 2000, 1.0 / 5000}},
		{"escape", Options{EscapeControl: true}, Options{}, 300000, [2]float64{1.0 / 20000, 1.0 / 60000}, [2]float64{1.0 / 2000, 0}},
		{"window", Options{WindowSize: 8192}, Options{}, 300000, [2]float64{1.0 / 15000, 1.0 / 50000}, [2]float64{1.0 / 3000, 1.0 / 6000}},
		{"crc16-8k", Options{CRC16: true, BlockSize: 8192}, Options{}, 400000, [2]float64{1.0 / 30000, 1.0 / 90000}, [2]float64{0, 0}},
		{"drops-only", Options{}, Options{}, 300000, [2]float64{0, 1.0 / 10000}, [2]float64{0, 1.0 / 3000}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(32 << 10)
			fwd := newNoise(uint64(100+i), tc.fwd[0], tc.fwd[1])
			rev := newNoise(uint64(200+i), tc.rev[0], tc.rev[1])
			a.out.fault = fwd.apply
			b.out.fault = rev.apply
			files := []memFile{
				{FileInfo{Name: "first.bin", Size: -1, ModTime: testTime, Mode: 0o644}, pattern(uint64(i), tc.size)},
				{FileInfo{Name: "nasty.bin", Size: -1, ModTime: testTime, Mode: 0o644}, bytes.Repeat(nastyBytes(), 20)},
			}
			sink := &memSink{}
			tc.send.Timeout, tc.rx.Timeout = 400*time.Millisecond, 400*time.Millisecond
			tc.send.Retries, tc.rx.Retries = 25, 25
			serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, tc.send, tc.rx)
			t.Logf("forward: %d flips, %d drops; reverse: %d flips, %d drops; %d bytes on the wire for %d bytes of data",
				fwd.flips, fwd.drops, rev.flips, rev.drops, a.out.written(), tc.size+len(files[1].data))
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			if fwd.flips+fwd.drops == 0 {
				t.Fatal("no faults were injected; the test proves nothing")
			}
			// The metadata may legitimately arrive from a retransmitted
			// ZFILE; the content must be exact.
			checkBatch(t, sink, files, false)
		})
	}
}

func TestZmodemResume(t *testing.T) {
	leakCheck(t)
	data := pattern(7, 200000)
	const have = 123456
	files := []memFile{{FileInfo{Name: "resume.bin", Size: int64(len(data)), ModTime: testTime, Mode: 0o644}, data}}

	t.Run("memSink", func(t *testing.T) {
		a, b := newDuplex(64 << 10)
		sink := &memSink{resume: map[string][]byte{"resume.bin": data[:have]}}
		var log eventLog
		serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, Options{OnEvent: log.add, Resume: true}, Options{})
		if serr != nil || rerr != nil {
			t.Fatalf("send: %v, receive: %v", serr, rerr)
		}
		checkBatch(t, sink, files, true)
		// The sender started at the resume offset and sent only the rest.
		if e := log.events[0]; e.Kind != FileStart || e.Bytes != have {
			t.Errorf("first sender event = %v at %d, want FileStart at %d", e.Kind, e.Bytes, have)
		}
		if sent := a.out.written(); sent > int64(len(data)-have)+4096 {
			t.Errorf("sender put %d bytes on the wire for %d remaining bytes", sent, len(data)-have)
		}
	})

	t.Run("DirSink", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "resume.bin")
		if err := os.WriteFile(path, data[:have], 0o644); err != nil {
			t.Fatal(err)
		}
		a, b := newDuplex(64 << 10)
		serr, rerr := zmodemLoop(t, a, b, sendFiles(files), DirSink{Dir: dir, Resume: true}, Options{}, Options{})
		if serr != nil || rerr != nil {
			t.Fatalf("send: %v, receive: %v", serr, rerr)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("resumed file differs (%d bytes, err %v)", len(got), err)
		}
		if sent := a.out.written(); sent > int64(len(data)-have)+4096 {
			t.Errorf("sender put %d bytes on the wire for %d remaining bytes", sent, len(data)-have)
		}
		st, _ := os.Stat(path)
		if !st.ModTime().Equal(testTime) {
			t.Errorf("mtime = %v, want %v", st.ModTime(), testTime)
		}

		// A second attempt finds the file complete and skips it.
		a, b = newDuplex(64 << 10)
		var log eventLog
		serr, rerr = zmodemLoop(t, a, b, sendFiles(files), DirSink{Dir: dir, Resume: true}, Options{OnEvent: log.add}, Options{})
		if serr != nil || rerr != nil {
			t.Fatalf("send: %v, receive: %v", serr, rerr)
		}
		if got := log.kinds(true); !slices.Equal(got, []EventKind{FileSkipped, Done}) {
			t.Errorf("sender events = %v, want [FileSkipped Done]", got)
		}
	})
}

func TestZmodemSkip(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	files := batch()
	sink := &memSink{skip: map[string]bool{"nasty.bin": true, "random.bin": true}}
	var sendLog, recvLog eventLog
	serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, Options{OnEvent: sendLog.add}, Options{OnEvent: recvLog.add})
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	var want []memFile
	for _, f := range files {
		if !sink.skip[f.info.Name] {
			want = append(want, f)
		}
	}
	checkBatch(t, sink, want, true)
	if n := sendLog.count(FileSkipped); n != 2 {
		t.Errorf("sender reported %d skipped files, want 2", n)
	}
	if n := recvLog.count(FileSkipped); n != 2 {
		t.Errorf("receiver reported %d skipped files, want 2", n)
	}
}

// TestZmodemOpenError: a file that cannot be opened is skipped, the rest of
// the batch is sent and the error is reported at the end.
func TestZmodemOpenError(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	files := batch()
	send := sendFiles(files)
	boom := errors.New("boom")
	send[2].Open = func() (io.ReadSeekCloser, error) { return nil, boom }
	sink := &memSink{}
	serr, rerr := zmodemLoop(t, a, b, send, sink, Options{}, Options{})
	if !errors.Is(serr, boom) || rerr != nil {
		t.Fatalf("send: %v (want boom), receive: %v", serr, rerr)
	}
	checkBatch(t, sink, append(files[:2:2], files[3:]...), true)
}

func TestZmodemCancel(t *testing.T) {
	data := pattern(9, 4<<20)
	files := []memFile{{FileInfo{Name: "big.bin", Size: int64(len(data)), ModTime: testTime, Mode: 0o644}, data}}

	t.Run("receiver-context", func(t *testing.T) {
		leakCheck(t)
		a, b := newDuplex(16 << 10)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w := &hookWriter{at: 200000, fn: cancel}
		sink := sinkFunc(func(FileInfo) (io.WriteCloser, int64, error) { return w, 0, nil })
		var events eventLog
		serr, rerr := runPair(t, a, b,
			func() error { return ZmodemSend(context.Background(), a, sendFiles(files), Options{}) },
			func() error { return ZmodemReceive(ctx, b, sink, Options{OnEvent: events.add}) },
		)
		if !errors.Is(rerr, context.Canceled) {
			t.Errorf("receiver returned %v, want context.Canceled", rerr)
		}
		if !errors.Is(serr, ErrCanceled) {
			t.Errorf("sender returned %v, want ErrCanceled", serr)
		}
		if w.n >= int64(len(data)) {
			t.Errorf("the whole file (%d bytes) was received despite the cancellation", w.n)
		}
		if got := events.kinds(true); !slices.Equal(got, []EventKind{FileStart, Error}) {
			t.Errorf("receiver events = %v, want [FileStart Error]", got)
		}
	})

	t.Run("sender-context", func(t *testing.T) {
		leakCheck(t)
		a, b := newDuplex(16 << 10)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var events eventLog
		send := []SendFile{{
			Info: files[0].info,
			Open: func() (io.ReadSeekCloser, error) {
				return &hookReader{Reader: bytes.NewReader(data), at: 200000, fn: cancel}, nil
			},
		}}
		sink := &memSink{}
		serr, rerr := runPair(t, a, b,
			func() error { return ZmodemSend(ctx, a, send, Options{OnEvent: events.add}) },
			func() error { return ZmodemReceive(context.Background(), b, sink, Options{}) },
		)
		if !errors.Is(serr, context.Canceled) {
			t.Errorf("sender returned %v, want context.Canceled", serr)
		}
		if !errors.Is(rerr, ErrCanceled) {
			t.Errorf("receiver returned %v, want ErrCanceled", rerr)
		}
		if f := sink.get("big.bin"); f == nil || !f.closed {
			t.Error("the partial file was not closed")
		} else if f.buf.Len() >= len(data) {
			t.Errorf("the whole file was received despite the cancellation")
		}
		if got := events.kinds(true); !slices.Equal(got, []EventKind{FileStart, Error}) {
			t.Errorf("sender events = %v, want [FileStart Error]", got)
		}
	})

	t.Run("already-cancelled", func(t *testing.T) {
		leakCheck(t)
		a, b := newDuplex(16 << 10)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// Each side tells the other to stop.
		if err := ZmodemReceive(ctx, b, &memSink{}, Options{}); !errors.Is(err, context.Canceled) {
			t.Errorf("ZmodemReceive = %v, want context.Canceled", err)
		}
		if got := a.readAll(20 * time.Millisecond); !bytes.Equal(got, abortSequence) {
			t.Errorf("receiver sent %q, want the cancel sequence", got)
		}
		if err := ZmodemSend(ctx, a, nil, Options{}); !errors.Is(err, context.Canceled) {
			t.Errorf("ZmodemSend = %v, want context.Canceled", err)
		}
		if got := b.readAll(20 * time.Millisecond); !bytes.Equal(got, abortSequence) {
			t.Errorf("sender sent %q, want the cancel sequence", got)
		}
	})

	t.Run("abort-sequence", func(t *testing.T) {
		// Abort() written by the host cancels a remote receiver and sender.
		leakCheck(t)
		for _, side := range []string{"receive", "send"} {
			a, b := newDuplex(16 << 10)
			done := make(chan error, 1)
			go func() {
				if side == "receive" {
					done <- ZmodemReceive(context.Background(), b, &memSink{}, Options{})
				} else {
					done <- ZmodemSend(context.Background(), b, sendFiles(files), Options{})
				}
			}()
			time.Sleep(20 * time.Millisecond)
			Abort(a)
			select {
			case err := <-done:
				if !errors.Is(err, ErrCanceled) {
					t.Errorf("%s: got %v, want ErrCanceled", side, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: Abort did not cancel the transfer", side)
			}
		}
	})
}

// TestZmodemTimeout: with nobody on the other end both sides give up after
// their retries, send the cancel sequence and return ErrTimeout.
func TestZmodemTimeout(t *testing.T) {
	leakCheck(t)
	o := Options{Timeout: 30 * time.Millisecond, Retries: 2}
	a, b := newDuplex(16 << 10)
	start := time.Now()
	if err := ZmodemReceive(context.Background(), b, &memSink{}, o); !errors.Is(err, ErrTimeout) {
		t.Errorf("ZmodemReceive = %v, want ErrTimeout", err)
	}
	out := a.readAll(20 * time.Millisecond)
	if n := bytes.Count(out, []byte("**\x18B01")); n != 3 {
		t.Errorf("receiver sent %d ZRINIT headers, want 3 (one plus two retries): %q", n, out)
	}
	if !bytes.HasSuffix(out, abortSequence) {
		t.Errorf("receiver did not finish with the cancel sequence: %q", out)
	}

	if err := ZmodemSend(context.Background(), a, sendFiles(batch()), o); !errors.Is(err, ErrTimeout) {
		t.Errorf("ZmodemSend = %v, want ErrTimeout", err)
	}
	out = b.readAll(20 * time.Millisecond)
	if !bytes.HasPrefix(out, []byte("**\x18B00000000000000\r\x8a\x11")) {
		t.Errorf("sender did not start with ZRQINIT: %q", out)
	}
	if !bytes.HasSuffix(out, abortSequence) {
		t.Errorf("sender did not finish with the cancel sequence: %q", out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("giving up took %v", d)
	}
}

// TestZmodemSinkError: a failing sink aborts the transfer on both sides.
func TestZmodemSinkError(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	boom := errors.New("disk on fire")
	serr, rerr := zmodemLoop(t, a, b, sendFiles(batch()), &memSink{fail: boom}, Options{}, Options{})
	if !errors.Is(rerr, boom) {
		t.Errorf("receiver returned %v, want the sink error", rerr)
	}
	if !errors.Is(serr, ErrCanceled) {
		t.Errorf("sender returned %v, want ErrCanceled", serr)
	}
}

// failWriter fails after a number of bytes.
type failWriter struct {
	left int
	err  error
}

func (w *failWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		n := w.left
		w.left = 0
		return n, w.err
	}
	w.left -= len(p)
	return len(p), nil
}
func (w *failWriter) Close() error { return nil }

func TestZmodemWriteError(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	boom := errors.New("no space left")
	sink := sinkFunc(func(FileInfo) (io.WriteCloser, int64, error) {
		return &failWriter{left: 50000, err: boom}, 0, nil
	})
	files := []memFile{{FileInfo{Name: "x.bin", Size: -1}, pattern(3, 300000)}}
	serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, Options{}, Options{})
	if !errors.Is(rerr, boom) {
		t.Errorf("receiver returned %v, want the write error", rerr)
	}
	if !errors.Is(serr, ErrCanceled) {
		t.Errorf("sender returned %v, want ErrCanceled", serr)
	}
}

// TestZmodemLeftover checks that bytes following the last protocol byte are
// handed back untouched, through Unread or the Leftover callback.
func TestZmodemLeftover(t *testing.T) {
	const prompt = "\r\nuser@host:~$ *** OO \x18B"
	files := batch()[:3]

	for _, mode := range []string{"unread", "callback"} {
		t.Run("receiver-"+mode, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			// The remote "shell" prints its prompt right behind the sender's
			// final "OO".
			a.tap = func(p []byte) []byte {
				if string(p) == "OO" {
					return []byte("OO" + prompt)
				}
				return p
			}
			var got []byte
			ro := Options{}
			if mode == "callback" {
				ro.Leftover = func(p []byte) { got = append(got, p...) }
			}
			sink := &memSink{}
			serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, Options{}, ro)
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			checkBatch(t, sink, files, true)
			if mode == "unread" {
				// runPair put the link into discard mode after the transfer;
				// what Unread gave back is still in its buffer.
				got = b.readAll(20 * time.Millisecond)
			}
			if string(got) != prompt {
				t.Errorf("leftover = %q, want %q", got, prompt)
			}
		})

		t.Run("sender-"+mode, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			// The remote prints its prompt right behind the receiver's ZFIN.
			b.tap = func(p []byte) []byte {
				if bytes.HasPrefix(p, []byte("**\x18B08")) {
					return append(append([]byte(nil), p...), prompt...)
				}
				return p
			}
			var got []byte
			so := Options{}
			if mode == "callback" {
				so.Leftover = func(p []byte) { got = append(got, p...) }
			}
			sink := &memSink{}
			serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, so, Options{})
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			if mode == "unread" {
				got = a.readAll(20 * time.Millisecond)
			}
			if string(got) != prompt {
				t.Errorf("leftover = %q, want %q", got, prompt)
			}
		})
	}
}

// TestZmodemAutoStart feeds the sessions the announcement the Detector saw,
// in full and truncated, as happens when the header straddles two reads.
func TestZmodemAutoStart(t *testing.T) {
	leakCheck(t)
	files := batch()[:3]
	for _, pre := range []string{
		"rz\r**\x18B00000000000000\r\x8a\x11",
		"\x18B00000000000000\r\x8a\x11",
		"0000000\r\x8a\x11",
		"rz\r**\x18B00000000000000\r\x8a\x11**\x18B00000000000000\r\x8a\x11**\x18B00000000000000\r\x8a\x11",
	} {
		a, b := newDuplex(64 << 10)
		b.Unread([]byte(pre))
		sink := &memSink{}
		start := time.Now()
		serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, Options{}, Options{})
		if serr != nil || rerr != nil {
			t.Fatalf("%q: send: %v, receive: %v", pre, serr, rerr)
		}
		checkBatch(t, sink, files, true)
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%q: transfer took %v", pre, d)
		}
	}
	// The sender finds the receiver's ZRINIT (or a fragment of it) waiting.
	for _, pre := range []string{
		"**\x18B0100000023be50\r\x8a\x11",
		"100000023be50\r\x8a\x11",
	} {
		a, b := newDuplex(64 << 10)
		a.Unread([]byte(pre))
		sink := &memSink{}
		serr, rerr := zmodemLoop(t, a, b, sendFiles(files), sink, Options{}, Options{})
		if serr != nil || rerr != nil {
			t.Fatalf("%q: send: %v, receive: %v", pre, serr, rerr)
		}
		checkBatch(t, sink, files, true)
	}
}

// scriptPeer is a hand-driven ZMODEM peer for protocol-level tests.
type scriptPeer struct {
	t *testing.T
	*zmodem
	end *endpoint
}

func newScriptPeer(t *testing.T, end *endpoint) *scriptPeer {
	x := newSession(context.Background(), end, Options{Timeout: 5 * time.Second})
	t.Cleanup(func() { x.rd.close() })
	return &scriptPeer{t: t, zmodem: newZmodem(x), end: end}
}

func (p *scriptPeer) expect(typ byte) zhdr {
	p.t.Helper()
	h, err := p.readHeader(5 * time.Second)
	if err != nil {
		p.t.Fatalf("waiting for frame type %d: %v", typ, err)
	}
	if h.typ != typ {
		p.t.Fatalf("got frame type %d, want %d", h.typ, typ)
	}
	return h
}

// TestZmodemReceiverProtocol drives ZmodemReceive with hand-made frames to
// cover the less common corners: ZSINIT with an attention string, ZFREECNT,
// ZCOMMAND, a file sent in ZCRCW/ZCRCQ frames, a stale ZDATA restart, a
// ZEOF at the wrong position, a damaged subpacket and the attention string
// being played back.
func TestZmodemReceiverProtocol(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	sink := &memSink{}
	done := make(chan error, 1)
	go func() { done <- ZmodemReceive(context.Background(), b, sink, Options{Timeout: 2 * time.Second}) }()

	p := newScriptPeer(t, a)
	p.use32 = true
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.sendHex(zhdr{typ: zrqinit}))
	h := p.expect(zrinit)
	if h.b[zf0]&(canFDX|canOVIO|canFC32) != canFDX|canOVIO|canFC32 {
		t.Errorf("ZRINIT flags = %#x", h.b[zf0])
	}

	// ZSINIT with an attention string.
	must(p.sendBin(zhdr{typ: zsinit}))
	must(p.sendData([]byte("!ATTN!\x00"), zcrcw))
	p.expect(zack)

	must(p.sendHex(zhdr{typ: zfreecnt}))
	p.expect(zack)

	must(p.sendBin(zhdr{typ: zcommand}))
	must(p.sendData([]byte("rm -rf /\x00"), zcrcw))
	if h := p.expect(zcompl); h.pos() == 0 {
		t.Error("ZCOMMAND was reported as successful")
	}

	data := pattern(11, 5000)
	fh := zhdr{typ: zfile}
	fh.b[zf0] = zcbin
	must(p.sendBin(fh))
	must(p.sendData([]byte("dir/../../evil\\..\\proto.bin\x005000 12345670000 100644 0 1 5000\x00"), zcrcw))
	if h := p.expect(zrpos); h.pos() != 0 {
		t.Fatalf("ZRPOS at %d, want 0", h.pos())
	}

	// Frame 1: 1000 bytes acknowledged with ZCRCQ, 1000 more with ZCRCW.
	must(p.sendBin(posHdr(zdata, 0)))
	must(p.sendData(data[:1000], zcrcq))
	if h := p.expect(zack); h.pos() != 1000 {
		t.Fatalf("ZACK at %d, want 1000", h.pos())
	}
	must(p.sendData(data[1000:2000], zcrcw))
	if h := p.expect(zack); h.pos() != 2000 {
		t.Fatalf("ZACK at %d, want 2000", h.pos())
	}

	// A restart from an earlier position (duplicate ZRPOS handling on the
	// sender side): the overlap must be dropped, not written twice.
	must(p.sendBin(posHdr(zdata, 500)))
	must(p.sendData(data[500:2500], zcrcw))
	if h := p.expect(zack); h.pos() != 2500 {
		t.Fatalf("ZACK at %d, want 2500", h.pos())
	}

	// A ZEOF for the wrong position is ignored.
	must(p.sendBin(posHdr(zeof, 5000)))

	// Data from beyond the receive position: the receiver plays the
	// attention string and asks for its position.
	must(p.sendBin(posHdr(zdata, 4000)))
	must(p.sendData(data[4000:], zcrce))
	if h := p.expect(zrpos); h.pos() != 2500 {
		t.Fatalf("ZRPOS at %d, want 2500", h.pos())
	}

	// A damaged subpacket: same reaction.
	must(p.sendBin(posHdr(zdata, 2500)))
	p.out = p.out[:0]
	for _, c := range data[2500:3000] {
		p.putEsc(c)
	}
	p.out = append(p.out, zdle, zcrcg, 1, 2, 3, 4)
	must(p.write(p.out))
	if h := p.expect(zrpos); h.pos() != 2500 {
		t.Fatalf("ZRPOS at %d, want 2500", h.pos())
	}

	// The rest, then a proper end.
	must(p.sendBin(posHdr(zdata, 2500)))
	must(p.sendData(data[2500:], zcrce))
	must(p.sendBin(posHdr(zeof, 5000)))
	p.expect(zrinit)
	must(p.sendHex(zhdr{typ: zfin}))
	p.expect(zfin)
	must(p.write([]byte("OO")))

	if err := <-done; err != nil {
		t.Fatalf("ZmodemReceive: %v", err)
	}
	f := sink.get("proto.bin")
	if f == nil {
		t.Fatalf("file not stored under its sanitised name; have %v", sink.files)
	}
	if !bytes.Equal(f.buf.Bytes(), data) {
		t.Errorf("content differs: %d bytes%s", f.buf.Len(), firstDiff(f.buf.Bytes(), data))
	}
	if f.info.Size != 5000 || f.info.Mode != 0o644 || f.info.ModTime.Unix() != 0o12345670000 {
		t.Errorf("file info = %+v", f.info)
	}
}

// TestZmodemAttention checks that the attention string precedes error ZRPOS
// headers on the wire.
func TestZmodemAttention(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	done := make(chan error, 1)
	go func() {
		done <- ZmodemReceive(context.Background(), b, &memSink{}, Options{Timeout: 2 * time.Second})
	}()
	p := newScriptPeer(t, a)
	p.expect(zrinit)
	if err := p.sendBin(zhdr{typ: zsinit}); err != nil {
		t.Fatal(err)
	}
	p.sendData([]byte("!ATTN!\x00"), zcrcw)
	p.expect(zack)
	p.sendBin(zhdr{typ: zfile})
	p.sendData([]byte("a.bin\x00100\x00"), zcrcw)
	p.expect(zrpos)
	p.sendBin(posHdr(zdata, 50)) // ahead of the receiver: an error

	// Read the raw reply instead of parsing it.
	var raw []byte
	deadline := time.Now().Add(3 * time.Second)
	for !bytes.Contains(raw, []byte("**\x18B09")) && time.Now().Before(deadline) {
		c, err := p.rd.get(100 * time.Millisecond)
		if err == nil {
			raw = append(raw, c)
		}
	}
	if !strings.Contains(string(raw), "!ATTN!**\x18B09") {
		t.Errorf("error reply = %q, want the attention string followed by ZRPOS", raw)
	}
	Abort(a)
	if err := <-done; !errors.Is(err, ErrCanceled) {
		t.Errorf("ZmodemReceive = %v, want ErrCanceled", err)
	}
}

// TestZmodemSenderProtocol drives ZmodemSend with hand-made frames:
// ZCHALLENGE, a receiver with a small buffer (segmented ZCRCW frames), a
// ZCRC request, a ZRPOS in mid-stream and a repeated ZEOF.
func TestZmodemSenderProtocol(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(1 << 20)
	data := pattern(13, 10000)
	files := []memFile{{FileInfo{Name: "tx.bin", Size: int64(len(data)), ModTime: testTime, Mode: 0o600}, data}}
	done := make(chan error, 1)
	var acked atomic.Int64
	go func() {
		done <- ZmodemSend(context.Background(), a, sendFiles(files), Options{
			Timeout: 2 * time.Second,
			OnEvent: func(e Event) {
				if e.Kind == FileDone {
					acked.Store(e.Bytes)
				}
			},
		})
	}()
	p := newScriptPeer(t, b)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	p.expect(zrqinit)
	must(p.sendHex(posHdr(zchallenge, 0xdeadbeef)))
	if h := p.expect(zack); h.pos() != 0xdeadbeef {
		t.Fatalf("challenge answered with %#x", h.pos())
	}
	// Receiver with a 2048 byte buffer and CRC-32.
	ri := zhdr{typ: zrinit}
	ri.b[zf0] = canFDX | canOVIO | canFC32
	ri.b[zp0], ri.b[zp1] = 0x00, 0x08
	must(p.sendHex(ri))

	fh := p.expect(zfile)
	if fh.b[zf0] != zcbin {
		t.Errorf("ZFILE conversion option = %d, want ZCBIN", fh.b[zf0])
	}
	meta, fe, err := p.readData(maxSubpacket)
	must(err)
	if fe != zcrcw {
		t.Errorf("ZFILE data ended with %q, want ZCRCW", fe)
	}
	info, err := parseFileHeader(meta)
	must(err)
	if info.Name != "tx.bin" || info.Size != 10000 || !info.ModTime.Equal(testTime) || info.Mode != 0o600 {
		t.Errorf("announced %+v", info)
	}

	// Ask for the file CRC first.
	must(p.sendHex(zhdr{typ: zcrc}))
	if h := p.expect(zcrc); h.pos() != crc32.ChecksumIEEE(data) {
		t.Errorf("file CRC = %#x", h.pos())
	}

	must(p.sendHex(posHdr(zrpos, 0)))
	// recvFrame reads the next frame, which must start at wantPos, and
	// returns its frame end; 0 means that ZEOF arrived instead.
	var got []byte
	recvFrame := func(wantPos int) byte {
		t.Helper()
		h, err := p.readHeader(5 * time.Second)
		must(err)
		if h.typ == zeof {
			if h.pos() != 10000 {
				t.Fatalf("ZEOF at %d", h.pos())
			}
			return 0
		}
		if h.typ != zdata || int(h.pos()) != wantPos {
			t.Fatalf("got frame type %d at %d, want ZDATA at %d", h.typ, h.pos(), wantPos)
		}
		n := 0
		for {
			d, fe, err := p.readData(maxSubpacket)
			must(err)
			got = append(got, d...)
			n += len(d)
			if n > 2048 {
				t.Fatalf("frame of %d bytes exceeds the receiver buffer", n)
			}
			if fe == zcrcw || fe == zcrce {
				return fe
			}
		}
	}
	// Two full buffers.
	for pos := 0; pos < 4096; pos += 2048 {
		if fe := recvFrame(pos); fe != zcrcw {
			t.Fatalf("frame at %d ended with %q, want ZCRCW", pos, fe)
		}
		must(p.sendHex(posHdr(zack, uint32(pos+2048))))
	}
	// Reject the third buffer: back to 3000.
	recvFrame(4096)
	got = got[:3000]
	must(p.sendHex(posHdr(zrpos, 3000)))
	for {
		fe := recvFrame(len(got))
		if fe == 0 {
			break
		}
		if fe == zcrcw {
			must(p.sendHex(posHdr(zack, uint32(len(got)))))
		}
	}
	// Pretend the ZEOF was garbled.
	must(p.sendHex(zhdr{typ: znak}))
	p.expect(zeof)
	must(p.sendHex(ri))
	p.expect(zfin)
	must(p.sendHex(zhdr{typ: zfin}))
	if err := <-done; err != nil {
		t.Fatalf("ZmodemSend: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content differs: %d bytes%s", len(got), firstDiff(got, data))
	}
	if acked.Load() != 10000 {
		t.Errorf("FileDone reported %d bytes", acked.Load())
	}
	if tail := b.readAll(50 * time.Millisecond); string(tail) != "" {
		// The script peer's reader already took the "OO".
		t.Logf("tail: %q", tail)
	}
}
