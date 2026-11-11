package xfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"
	"time"
)

// padded returns data padded with SUB to a multiple of 128 bytes, the way an
// XMODEM sender delivers it (a short final 1K piece goes out in 128-byte
// blocks, so the granularity is 128 in every mode).
func padded(data []byte) []byte {
	out := append([]byte(nil), data...)
	for len(out)%128 != 0 {
		out = append(out, sub)
	}
	return out
}

func xmodemLoop(t *testing.T, a, b *endpoint, data []byte, so, ro Options) (sent, received int64, out []byte, serr, rerr error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var buf bytes.Buffer
	serr, rerr = runPair(t, a, b,
		func() (err error) { sent, err = XmodemSend(ctx, a, bytes.NewReader(data), so); return err },
		func() (err error) { received, err = XmodemReceive(ctx, b, &buf, ro); return err },
	)
	return sent, received, buf.Bytes(), serr, rerr
}

func TestXmodemLoopback(t *testing.T) {
	modes := []struct {
		name     string
		send, rx Options
	}{
		{"checksum", Options{}, Options{}},
		{"crc", Options{}, Options{UseCRC: true}},
		{"1k-crc", Options{Use1K: true}, Options{UseCRC: true}},
		{"1k-checksum", Options{Use1K: true}, Options{}},
	}
	sizes := []int{0, 1, 127, 128, 129, 1023, 1024, 1025, 5000, 70000}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			leakCheck(t)
			for _, size := range sizes {
				data := pattern(uint64(size), size)
				if size == 5000 {
					data = bytes.Repeat(nastyBytes(), 2)
				}
				a, b := newDuplex(64 << 10)
				sent, received, out, serr, rerr := xmodemLoop(t, a, b, data, m.send, m.rx)
				if serr != nil || rerr != nil {
					t.Fatalf("size %d: send: %v, receive: %v", size, serr, rerr)
				}
				if sent != int64(len(data)) {
					t.Errorf("size %d: XmodemSend reported %d bytes", size, sent)
				}
				want := padded(data)
				if !bytes.Equal(out, want) {
					t.Errorf("size %d: received %d bytes, want %d%s", size, len(out), len(want), firstDiff(out, want))
				}
				if received != int64(len(out)) {
					t.Errorf("size %d: XmodemReceive reported %d bytes, wrote %d", size, received, len(out))
				}
				if left := append(a.readAll(10*time.Millisecond), b.readAll(10*time.Millisecond)...); len(left) != 0 {
					t.Errorf("size %d: unread protocol bytes: %q", size, left)
				}
			}
		})
	}
}

func TestXmodemTrimPadding(t *testing.T) {
	leakCheck(t)
	for _, size := range []int{0, 1, 127, 128, 129, 1500, 4096} {
		data := pattern(uint64(size)+7, size)
		if size > 0 {
			data[size-1] = 'x' // real data ending in SUB cannot be told from padding
		}
		a, b := newDuplex(64 << 10)
		_, received, out, serr, rerr := xmodemLoop(t, a, b, data, Options{Use1K: true}, Options{UseCRC: true, TrimPadding: true})
		if serr != nil || rerr != nil {
			t.Fatalf("size %d: send: %v, receive: %v", size, serr, rerr)
		}
		if !bytes.Equal(out, data) || received != int64(size) {
			t.Errorf("size %d: received %d bytes (reported %d)", size, len(out), received)
		}
	}
}

// TestXmodemCRCFallback: the sender never sees the 'C' requests (as if it
// only understood checksum XMODEM); the receiver falls back to NAK and
// recognises the checksum blocks.
func TestXmodemCRCFallback(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	b.out.fault = func(p []byte) []byte {
		return bytes.ReplaceAll(p, []byte{wantCRC}, nil)
	}
	data := pattern(5, 3000)
	o := Options{Timeout: 100 * time.Millisecond, Retries: 20}
	ro := o
	ro.UseCRC = true
	_, _, out, serr, rerr := xmodemLoop(t, a, b, data, o, ro)
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	if !bytes.Equal(out, padded(data)) {
		t.Errorf("received %d bytes, want %d", len(out), len(padded(data)))
	}
}

func TestXmodemLossy(t *testing.T) {
	for i, m := range []struct {
		name     string
		send, rx Options
	}{
		{"checksum", Options{}, Options{}},
		{"crc", Options{}, Options{UseCRC: true}},
		{"1k", Options{Use1K: true}, Options{UseCRC: true}},
	} {
		t.Run(m.name, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			fwd := newNoise(uint64(300+i), 1.0/4000, 1.0/9000)
			rev := newNoise(uint64(400+i), 1.0/60, 1.0/80)
			a.out.fault = fwd.apply
			b.out.fault = rev.apply
			data := pattern(uint64(i), 60000)
			m.send.Timeout, m.rx.Timeout = 80*time.Millisecond, 80*time.Millisecond
			m.send.Retries, m.rx.Retries = 30, 30
			_, _, out, serr, rerr := xmodemLoop(t, a, b, data, m.send, m.rx)
			t.Logf("forward: %d flips, %d drops; reverse: %d flips, %d drops", fwd.flips, fwd.drops, rev.flips, rev.drops)
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			if fwd.flips+fwd.drops == 0 || rev.flips+rev.drops == 0 {
				t.Fatal("no faults were injected; the test proves nothing")
			}
			if m.name == "checksum" {
				// An 8-bit checksum cannot catch every double bit flip; what
				// it must guarantee is framing: no lost or duplicated block.
				if len(out) != len(padded(data)) {
					t.Errorf("received %d bytes, want %d", len(out), len(padded(data)))
				}
				return
			}
			if !bytes.Equal(out, padded(data)) {
				t.Errorf("received %d bytes, want %d%s", len(out), len(padded(data)), firstDiff(out, padded(data)))
			}
		})
	}
}

func TestXmodemCancel(t *testing.T) {
	data := pattern(1, 2<<20)

	t.Run("receiver-context", func(t *testing.T) {
		leakCheck(t)
		a, b := newDuplex(16 << 10)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w := &hookWriter{at: 50000, fn: cancel}
		serr, rerr := runPair(t, a, b,
			func() error {
				_, err := XmodemSend(context.Background(), a, bytes.NewReader(data), Options{})
				return err
			},
			func() error { _, err := XmodemReceive(ctx, b, w, Options{UseCRC: true}); return err },
		)
		if !errors.Is(rerr, context.Canceled) {
			t.Errorf("receiver returned %v, want context.Canceled", rerr)
		}
		if !errors.Is(serr, ErrCanceled) {
			t.Errorf("sender returned %v, want ErrCanceled", serr)
		}
	})

	t.Run("sender-context", func(t *testing.T) {
		leakCheck(t)
		a, b := newDuplex(16 << 10)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := &hookReader{Reader: bytes.NewReader(data), at: 50000, fn: cancel}
		serr, rerr := runPair(t, a, b,
			func() error { _, err := XmodemSend(ctx, a, r, Options{Use1K: true}); return err },
			func() error {
				_, err := XmodemReceive(context.Background(), b, io.Discard, Options{UseCRC: true})
				return err
			},
		)
		if !errors.Is(serr, context.Canceled) {
			t.Errorf("sender returned %v, want context.Canceled", serr)
		}
		if !errors.Is(rerr, ErrCanceled) {
			t.Errorf("receiver returned %v, want ErrCanceled", rerr)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		leakCheck(t)
		a, b := newDuplex(16 << 10)
		o := Options{Timeout: 20 * time.Millisecond, Retries: 3}
		if _, err := XmodemReceive(context.Background(), b, io.Discard, o); !errors.Is(err, ErrTimeout) {
			t.Errorf("XmodemReceive = %v, want ErrTimeout", err)
		}
		out := a.readAll(10 * time.Millisecond)
		if !bytes.HasPrefix(out, []byte{nak, nak}) || !bytes.HasSuffix(out, abortSequence) {
			t.Errorf("receiver sent %q, want NAKs followed by the cancel sequence", out)
		}
		if _, err := XmodemSend(context.Background(), a, bytes.NewReader(data), o); !errors.Is(err, ErrTimeout) {
			t.Errorf("XmodemSend = %v, want ErrTimeout", err)
		}
	})
}

// TestXmodemSyncError: a block number that is neither the expected one nor
// a repeat of the previous one is fatal.
func TestXmodemSyncError(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(16 << 10)
	done := make(chan error, 1)
	go func() {
		_, err := XmodemReceive(context.Background(), b, io.Discard, Options{Timeout: time.Second})
		done <- err
	}()
	payload := bytes.Repeat([]byte{'x'}, 128)
	a.Write(xyBlock(nil, 1, payload, false))
	a.Write(xyBlock(nil, 1, payload, false)) // duplicate: acknowledged, dropped
	a.Write(xyBlock(nil, 3, payload, false)) // block 2 is missing
	if err := <-done; !errors.Is(err, ErrProtocol) {
		t.Fatalf("XmodemReceive = %v, want ErrProtocol", err)
	}
	out := a.readAll(20 * time.Millisecond)
	if !bytes.HasPrefix(out, []byte{nak, ack, ack}) || !bytes.HasSuffix(out, abortSequence) {
		t.Errorf("receiver sent %q", out)
	}
}

func ymodemLoop(t *testing.T, a, b *endpoint, files []SendFile, sink Sink, so, ro Options) (serr, rerr error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return runPair(t, a, b,
		func() error { return YmodemSend(ctx, a, files, so) },
		func() error { return YmodemReceive(ctx, b, sink, ro) },
	)
}

func TestYmodemLoopback(t *testing.T) {
	cases := []struct {
		name     string
		send, rx Options
	}{
		{"128", Options{}, Options{}},
		{"1k", Options{Use1K: true}, Options{}},
		{"g", Options{Use1K: true}, Options{YmodemG: true}},
		{"g-128", Options{}, Options{YmodemG: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			files := batch()
			sink := &memSink{}
			var sendLog, recvLog eventLog
			tc.send.OnEvent, tc.rx.OnEvent = sendLog.add, recvLog.add
			serr, rerr := ymodemLoop(t, a, b, sendFiles(files), sink, tc.send, tc.rx)
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			checkBatch(t, sink, files, true)
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
			if left := append(a.readAll(10*time.Millisecond), b.readAll(10*time.Millisecond)...); len(left) != 0 {
				t.Errorf("unread protocol bytes: %q", left)
			}
		})
	}
}

func TestYmodemEmptyBatch(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	sink := &memSink{}
	serr, rerr := ymodemLoop(t, a, b, nil, sink, Options{}, Options{})
	if serr != nil || rerr != nil || len(sink.files) != 0 {
		t.Fatalf("send: %v, receive: %v, %d files", serr, rerr, len(sink.files))
	}
}

func TestYmodemLargeFile(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(256 << 10)
	data := pattern(77, 5<<20+999)
	files := []memFile{{FileInfo{Name: "large.bin", Size: int64(len(data)), ModTime: testTime, Mode: 0o644}, data}}
	sink := &memSink{}
	serr, rerr := ymodemLoop(t, a, b, sendFiles(files), sink, Options{Use1K: true}, Options{})
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	checkBatch(t, sink, files, true)
}

func TestYmodemLossy(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	fwd := newNoise(501, 1.0/5000, 1.0/12000)
	rev := newNoise(502, 1.0/80, 1.0/100)
	a.out.fault = fwd.apply
	b.out.fault = rev.apply
	files := []memFile{
		{FileInfo{Name: "a.bin", Size: -1, ModTime: testTime, Mode: 0o644}, pattern(1, 50000)},
		{FileInfo{Name: "b.bin", Size: -1, ModTime: testTime, Mode: 0o644}, bytes.Repeat(nastyBytes(), 10)},
		{FileInfo{Name: "c.bin", Size: -1, ModTime: testTime, Mode: 0o644}, nil},
	}
	sink := &memSink{}
	o := Options{Timeout: 80 * time.Millisecond, Retries: 30, Use1K: true}
	serr, rerr := ymodemLoop(t, a, b, sendFiles(files), sink, o, o)
	t.Logf("forward: %d flips, %d drops; reverse: %d flips, %d drops", fwd.flips, fwd.drops, rev.flips, rev.drops)
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	// A retransmitted header block may create the sink entry twice; the
	// last one holds the file.
	for _, w := range files {
		var got *memSinkFile
		for _, f := range sink.files {
			if f.info.Name == w.info.Name {
				got = f
			}
		}
		if got == nil || !bytes.Equal(got.buf.Bytes(), w.data) {
			t.Errorf("%s: content differs", w.info.Name)
		}
	}
}

// TestYmodemSkip: YMODEM cannot decline a file, so a skipped file is
// received and discarded while the rest of the batch is stored.
func TestYmodemSkip(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	files := batch()
	sink := &memSink{skip: map[string]bool{"random.bin": true}}
	var log eventLog
	serr, rerr := ymodemLoop(t, a, b, sendFiles(files), sink, Options{Use1K: true}, Options{OnEvent: log.add})
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	var want []memFile
	for _, f := range files {
		if f.info.Name != "random.bin" {
			want = append(want, f)
		}
	}
	checkBatch(t, sink, want, true)
	if n := log.count(FileSkipped); n != 1 {
		t.Errorf("%d FileSkipped events, want 1", n)
	}
}

func TestYmodemCancel(t *testing.T) {
	data := pattern(1, 2<<20)
	files := []memFile{{FileInfo{Name: "big.bin", Size: int64(len(data))}, data}}
	for _, g := range []bool{false, true} {
		t.Run(fmt.Sprintf("receiver-context/g=%v", g), func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(16 << 10)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &hookWriter{at: 100000, fn: cancel}
			sink := sinkFunc(func(FileInfo) (io.WriteCloser, int64, error) { return w, 0, nil })
			serr, rerr := runPair(t, a, b,
				func() error { return YmodemSend(context.Background(), a, sendFiles(files), Options{Use1K: true}) },
				func() error { return YmodemReceive(ctx, b, sink, Options{YmodemG: g}) },
			)
			if !errors.Is(rerr, context.Canceled) {
				t.Errorf("receiver returned %v, want context.Canceled", rerr)
			}
			if !errors.Is(serr, ErrCanceled) {
				t.Errorf("sender returned %v, want ErrCanceled", serr)
			}
		})
	}
	t.Run("sender-context", func(t *testing.T) {
		leakCheck(t)
		a, b := newDuplex(16 << 10)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		send := []SendFile{{
			Info: files[0].info,
			Open: func() (io.ReadSeekCloser, error) {
				return &hookReader{Reader: bytes.NewReader(data), at: 100000, fn: cancel}, nil
			},
		}}
		serr, rerr := runPair(t, a, b,
			func() error { return YmodemSend(ctx, a, send, Options{}) },
			func() error { return YmodemReceive(context.Background(), b, &memSink{}, Options{}) },
		)
		if !errors.Is(serr, context.Canceled) {
			t.Errorf("sender returned %v, want context.Canceled", serr)
		}
		if !errors.Is(rerr, ErrCanceled) {
			t.Errorf("receiver returned %v, want ErrCanceled", rerr)
		}
	})
}

// TestYmodemUnknownSize: a header block without a length field. The data is
// stored with its padding, or trimmed on request.
func TestYmodemUnknownSize(t *testing.T) {
	leakCheck(t)
	for _, trim := range []bool{false, true} {
		a, b := newDuplex(64 << 10)
		sink := &memSink{}
		done := make(chan error, 1)
		go func() {
			done <- YmodemReceive(context.Background(), b, sink, Options{Timeout: time.Second, TrimPadding: trim})
		}()
		x := newSession(context.Background(), a, Options{Timeout: time.Second})
		step := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err := x.xyWaitStart(time.Second, true)
		step(err)
		hdr := make([]byte, 128)
		copy(hdr, "/tmp/../nosize.txt")
		step(x.xySendBlock(xyBlock(nil, 0, hdr, true), true))
		_, err = x.xyWaitStart(time.Second, true)
		step(err)
		_, err = x.xySendData(bytes.NewReader([]byte("hello")), true, false)
		step(err)
		step(x.xySendEOT(true))
		_, err = x.xyWaitStart(time.Second, true)
		step(err)
		step(x.xySendBlock(xyBlock(nil, 0, make([]byte, 128), true), true))
		x.rd.close()
		if err := <-done; err != nil {
			t.Fatalf("YmodemReceive: %v", err)
		}
		f := sink.get("nosize.txt")
		if f == nil {
			t.Fatalf("file not stored under its sanitised name")
		}
		want := padded([]byte("hello"))
		if trim {
			want = []byte("hello")
		}
		if !bytes.Equal(f.buf.Bytes(), want) || f.info.Size != -1 {
			t.Errorf("trim=%v: got %q (size %d)", trim, f.buf.Bytes(), f.info.Size)
		}
	}
}
