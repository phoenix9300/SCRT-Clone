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

// kermitBatch is the standard batch with names that survive the
// conservative file name conversion.
func kermitBatch() []memFile {
	var runs []byte
	for _, c := range []byte{0x00, 0xff, '#', '~', '&', 0x01, ' ', 0x7f, 0x80, 0xa3, 0xfe, 'A'} {
		runs = append(runs, bytes.Repeat([]byte{c}, 300)...)
		runs = append(runs, 'x', c, c, c)
	}
	var all []byte
	for i := range 256 {
		all = append(all, byte(i))
	}
	rev := slices.Clone(all)
	slices.Reverse(rev)
	return []memFile{
		{FileInfo{Name: "empty.dat", Size: 0, ModTime: testTime}, nil},
		{FileInfo{Name: "one.dat", Size: 1, ModTime: testTime.Add(time.Hour)}, []byte{0x01}},
		{FileInfo{Name: "allbytes.bin", Size: -1, ModTime: testTime}, append(slices.Clone(all), rev...)},
		{FileInfo{Name: "runs.bin", Size: -1, ModTime: testTime}, runs},
		{FileInfo{Name: "nasty.bin", Size: -1, ModTime: testTime}, nastyBytes()},
		{FileInfo{Name: "random.bin", Size: 60000, ModTime: testTime}, pattern(2, 60000)},
		{FileInfo{Name: "text_file.txt", Size: 27, ModTime: testTime}, []byte("hello, world\r\nsecond line\r\n")},
	}
}

func kermitLoop(t *testing.T, a, b *endpoint, files []SendFile, sink Sink, so, ro Options, sc, rc kermitCfg) (serr, rerr error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if so.Timeout == 0 {
		so.Timeout = 400 * time.Millisecond
	}
	if ro.Timeout == 0 {
		ro.Timeout = 400 * time.Millisecond
	}
	return runPair(t, a, b,
		func() error { return kermitSend(ctx, a, files, so, sc) },
		func() error { return kermitReceive(ctx, b, sink, ro, rc) },
	)
}

func checkKermit(t *testing.T, sink *memSink, files []memFile) {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.files) != len(files) {
		t.Errorf("received %d files, want %d", len(sink.files), len(files))
	}
	for _, w := range files {
		var got *memSinkFile
		for _, f := range sink.files {
			if f.info.Name == w.info.Name {
				got = f
			}
		}
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
	}
}

func TestKermitCRCVector(t *testing.T) {
	// CRC-16/KERMIT check value.
	var crc uint16
	for _, c := range []byte("123456789") {
		crc = crc>>8 ^ kCRCTable[byte(crc)^c]
	}
	if crc != 0x2189 {
		t.Fatalf("crc = %#x, want 0x2189", crc)
	}
	// Type 1 checksum of "ABC": sum 198 -> 198 + 3 = 201 & 63 = 9.
	if got := kAppendCheck(nil, 1, []byte("ABC")); !bytes.Equal(got, []byte{ktoc(9)}) {
		t.Errorf("type 1 check = %q", got)
	}
}

func TestKermitCodec(t *testing.T) {
	data := kermitBatch()[3].data
	data = append(data, pattern(7, 5000)...)
	data = append(data, nastyBytes()...)
	for _, tc := range []struct{ qbin, rept byte }{{0, 0}, {'&', 0}, {0, '~'}, {'&', '~'}, {'`', '!'}} {
		k := &kermit{qctlIn: '#', qctlOut: '#', qbin: tc.qbin, rept: tc.rept}
		for _, capacity := range []int{9, 40, 91, 4000} {
			var got []byte
			for src := data; len(src) > 0; {
				enc, used := k.encodeBlock(src, capacity, false)
				if used == 0 || len(enc) > capacity {
					t.Fatalf("%+v cap %d: used %d, len %d", tc, capacity, used, len(enc))
				}
				for _, c := range enc {
					if c < 32 && c != 0 || c == 127 {
						t.Fatalf("control character %#x in encoded data", c)
					}
				}
				var err error
				if got, err = k.decode(got, enc); err != nil {
					t.Fatal(err)
				}
				src = src[used:]
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("%+v cap %d: round trip differs%s", tc, capacity, firstDiff(got, data))
			}
		}
	}
}

func TestKermitLoopback(t *testing.T) {
	cases := []struct {
		name   string
		sc, rc kermitCfg
	}{
		{"crc-long", kermitCfg{}, kermitCfg{}},
		{"crc-short", kermitCfg{longMax: -1}, kermitCfg{}},
		{"crc-short-recv", kermitCfg{}, kermitCfg{longMax: -1}},
		{"chk1-long", kermitCfg{chkt: '1'}, kermitCfg{}},
		{"chk1-short", kermitCfg{chkt: '1', longMax: -1}, kermitCfg{longMax: -1}},
		{"chk2-long", kermitCfg{chkt: '2'}, kermitCfg{}},
		{"chk2-short", kermitCfg{chkt: '2', longMax: -1}, kermitCfg{longMax: -1}},
		{"chk3-short", kermitCfg{chkt: '3', longMax: -1}, kermitCfg{longMax: -1}},
		{"receiver-limits-check", kermitCfg{chkt: '3'}, kermitCfg{chkt: '2'}},
		{"qbin-both", kermitCfg{qbin: '&'}, kermitCfg{qbin: '&'}},
		{"qbin-asked-by-receiver", kermitCfg{}, kermitCfg{qbin: '&'}},
		{"qbin-asked-by-sender", kermitCfg{qbin: '&', longMax: -1}, kermitCfg{longMax: -1}},
		{"qbin-other-prefix", kermitCfg{qbin: '`'}, kermitCfg{}},
		{"qbin-refused", kermitCfg{qbin: '&'}, kermitCfg{qbin: 'N'}},
		{"no-repeat", kermitCfg{rept: ' '}, kermitCfg{}},
		{"small-packets", kermitCfg{longMax: -1}, kermitCfg{maxl: 30, longMax: -1}},
		{"medium-long", kermitCfg{longMax: 300}, kermitCfg{longMax: 500}},
		{"no-attr", kermitCfg{noAttr: true}, kermitCfg{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			files := kermitBatch()
			sink := &memSink{}
			var sendLog, recvLog eventLog
			so, ro := Options{OnEvent: sendLog.add}, Options{OnEvent: recvLog.add}
			serr, rerr := kermitLoop(t, a, b, sendFiles(files), sink, so, ro, tc.sc, tc.rc)
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			checkKermit(t, sink, files)
			if !tc.sc.noAttr && tc.rc.maxl == 0 {
				for _, w := range files {
					if f := sink.get(w.info.Name); f != nil && (f.info.Size != int64(len(w.data)) || !f.info.ModTime.Equal(w.info.ModTime)) {
						t.Errorf("%s: attributes size %d mtime %v, want %d %v", w.info.Name, f.info.Size, f.info.ModTime, len(w.data), w.info.ModTime)
					}
				}
			}
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
		})
	}
}

func TestKermitEmptyBatch(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	sink := &memSink{}
	serr, rerr := kermitLoop(t, a, b, nil, sink, Options{}, Options{}, kermitCfg{}, kermitCfg{})
	if serr != nil || rerr != nil || len(sink.files) != 0 {
		t.Fatalf("send: %v, receive: %v, files %d", serr, rerr, len(sink.files))
	}
}

func TestKermitLargeFile(t *testing.T) {
	data := append(pattern(11, 2<<20+12345), bytes.Repeat([]byte{0}, 100000)...)
	files := []memFile{{FileInfo{Name: "big.bin", Size: int64(len(data)), ModTime: testTime}, data}}
	for _, tc := range []struct {
		name string
		cfg  kermitCfg
	}{{"long", kermitCfg{}}, {"short", kermitCfg{longMax: -1, chkt: '2'}}} {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			sink := &memSink{}
			serr, rerr := kermitLoop(t, a, b, sendFiles(files), sink, Options{}, Options{}, tc.cfg, tc.cfg)
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			checkKermit(t, sink, files)
		})
	}
}

// TestKermitPublicAPI exercises the exported entry points and checks that
// the S packet uses block check type 1 whatever was proposed.
func TestKermitPublicAPI(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	var first []byte
	a.tap = func(p []byte) []byte {
		if first == nil {
			first = append([]byte(nil), p...)
		}
		return p
	}
	files := kermitBatch()[:4]
	sink := &memSink{}
	ctx := context.Background()
	serr, rerr := runPair(t, a, b,
		func() error { return KermitSend(ctx, a, sendFiles(files), Options{Timeout: time.Second}) },
		func() error { return KermitReceive(ctx, b, sink, Options{Timeout: time.Second}) },
	)
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	checkKermit(t, sink, files)
	// MARK, LEN = SEQ+TYPE+13 parameter bytes+1 check byte, SEQ 0, TYPE S.
	if len(first) < 5 || first[0] != 1 || first[1] != ktoc(16) || first[2] != ktoc(0) || first[3] != 'S' || first[len(first)-1] != '\r' {
		t.Errorf("unexpected first packet %q", first)
	}
}

// TestKermitDuplicates: every packet of the sender arrives twice, as after
// needless retransmissions. The receiver must not store anything twice.
func TestKermitDuplicates(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	a.tap = func(p []byte) []byte { return append(append([]byte(nil), p...), p...) }
	files := kermitBatch()
	sink := &memSink{}
	serr, rerr := kermitLoop(t, a, b, sendFiles(files), sink, Options{}, Options{}, kermitCfg{longMax: -1, chkt: '2'}, kermitCfg{})
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	checkKermit(t, sink, files)
}

func TestKermitLossy(t *testing.T) {
	data := func(seed uint64) []byte { return append(pattern(seed, 30000), nastyBytes()...) }
	files := []memFile{
		{FileInfo{Name: "a.bin", Size: -1, ModTime: testTime}, data(1)},
		{FileInfo{Name: "b.bin", Size: -1, ModTime: testTime}, bytes.Repeat(nastyBytes(), 4)},
		{FileInfo{Name: "c.bin", Size: -1, ModTime: testTime}, nil},
		{FileInfo{Name: "d.bin", Size: -1, ModTime: testTime}, bytes.Repeat([]byte{0xe5}, 20000)},
	}
	cases := []struct {
		name         string
		cfg          kermitCfg
		fwdF, fwdD   float64
		revF, revD   float64
		seedA, seedB uint64
	}{
		{"crc-short", kermitCfg{longMax: -1}, 1.0 / 4000, 1.0 / 9000, 1.0 / 120, 1.0 / 200, 601, 602},
		{"crc-long", kermitCfg{}, 1.0 / 40000, 1.0 / 80000, 1.0 / 120, 1.0 / 200, 603, 604},
		{"chk2-short", kermitCfg{chkt: '2', longMax: -1}, 1.0 / 4000, 1.0 / 9000, 1.0 / 150, 1.0 / 250, 605, 606},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			fwd := newNoise(tc.seedA, tc.fwdF, tc.fwdD)
			rev := newNoise(tc.seedB, tc.revF, tc.revD)
			a.out.fault = fwd.apply
			b.out.fault = rev.apply
			sink := &memSink{}
			o := Options{Timeout: 250 * time.Millisecond, Retries: 40}
			serr, rerr := kermitLoop(t, a, b, sendFiles(files), sink, o, o, tc.cfg, tc.cfg)
			t.Logf("forward: %d flips, %d drops; reverse: %d flips, %d drops", fwd.flips, fwd.drops, rev.flips, rev.drops)
			if fwd.flips+fwd.drops+rev.flips+rev.drops == 0 {
				t.Error("no faults were injected")
			}
			if serr != nil || rerr != nil {
				t.Fatalf("send: %v, receive: %v", serr, rerr)
			}
			checkKermit(t, sink, files)
		})
	}
}

func TestKermitSkip(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	files := kermitBatch()
	sink := &memSink{skip: map[string]bool{"runs.bin": true, "empty.dat": true}}
	var recvLog, sendLog eventLog
	serr, rerr := kermitLoop(t, a, b, sendFiles(files), sink, Options{OnEvent: sendLog.add}, Options{OnEvent: recvLog.add}, kermitCfg{}, kermitCfg{})
	if serr != nil || rerr != nil {
		t.Fatalf("send: %v, receive: %v", serr, rerr)
	}
	var want []memFile
	for _, f := range files {
		if !sink.skip[f.info.Name] {
			want = append(want, f)
		}
	}
	checkKermit(t, sink, want)
	if n := recvLog.count(FileSkipped); n != 2 {
		t.Errorf("receiver reported %d skipped files, want 2", n)
	}
	// A zero-length file is only offered to the sink at its end, when the
	// sender can no longer be told: it counts that file as sent.
	if n := sendLog.count(FileSkipped); n != 1 {
		t.Errorf("sender reported %d skipped files, want 1", n)
	}
	if n := sendLog.count(FileDone); n != len(want)+1 {
		t.Errorf("sender reported %d finished files, want %d", n, len(want)+1)
	}
}

func TestKermitSinkError(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	boom := errors.New("disk full")
	sink := &memSink{fail: boom}
	serr, rerr := kermitLoop(t, a, b, sendFiles(kermitBatch()), sink, Options{}, Options{}, kermitCfg{}, kermitCfg{})
	if !errors.Is(rerr, boom) {
		t.Errorf("receiver returned %v, want %v", rerr, boom)
	}
	if !errors.Is(serr, ErrCanceled) {
		t.Errorf("sender returned %v, want ErrCanceled", serr)
	}
}

func TestKermitCancel(t *testing.T) {
	data := pattern(1, 2<<20)
	files := []memFile{{FileInfo{Name: "big.bin", Size: int64(len(data))}, data}}
	for _, cfg := range []kermitCfg{{}, {longMax: -1}} {
		t.Run(fmt.Sprintf("receiver-context/long=%v", cfg.longMax == 0), func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(16 << 10)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w := &hookWriter{at: 100000, fn: cancel}
			sink := sinkFunc(func(FileInfo) (io.WriteCloser, int64, error) { return w, 0, nil })
			var log eventLog
			serr, rerr := runPair(t, a, b,
				func() error {
					return kermitSend(context.Background(), a, sendFiles(files), Options{Timeout: time.Second}, cfg)
				},
				func() error {
					return kermitReceive(ctx, b, sink, Options{Timeout: time.Second, OnEvent: log.add}, cfg)
				},
			)
			if !errors.Is(rerr, context.Canceled) {
				t.Errorf("receiver returned %v, want context.Canceled", rerr)
			}
			if !errors.Is(serr, ErrCanceled) {
				t.Errorf("sender returned %v, want ErrCanceled", serr)
			}
			if k := log.kinds(true); len(k) == 0 || k[len(k)-1] != Error {
				t.Errorf("receiver events %v do not end with Error", k)
			}
		})
		t.Run(fmt.Sprintf("sender-context/long=%v", cfg.longMax == 0), func(t *testing.T) {
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
				func() error { return kermitSend(ctx, a, send, Options{Timeout: time.Second}, cfg) },
				func() error {
					return kermitReceive(context.Background(), b, &memSink{}, Options{Timeout: time.Second}, cfg)
				},
			)
			if !errors.Is(serr, context.Canceled) {
				t.Errorf("sender returned %v, want context.Canceled", serr)
			}
			if !errors.Is(rerr, ErrCanceled) {
				t.Errorf("receiver returned %v, want ErrCanceled", rerr)
			}
		})
	}
}

// TestKermitReceiverCancelsBatch: a scripted receiver acknowledges the first
// data packet with Z; the sender closes the file and the batch and reports
// the cancellation.
func TestKermitReceiverCancelsBatch(t *testing.T) {
	for _, ackData := range []string{"Z", "X"} {
		t.Run(ackData, func(t *testing.T) {
			leakCheck(t)
			a, b := newDuplex(64 << 10)
			files := []memFile{
				{FileInfo{Name: "one.bin", Size: -1}, pattern(1, 5000)},
				{FileInfo{Name: "two.bin", Size: -1}, pattern(2, 5000)},
			}
			var types []byte
			scripted := func() error {
				r := newKermit(context.Background(), b, Options{Timeout: time.Second}, kermitCfg{})
				defer r.rd.close()
				var perr error
				for {
					p, err := r.readPacket(time.Now().Add(3 * time.Second))
					if err != nil {
						return err
					}
					types = append(types, p.typ)
					data := []byte(nil)
					chk := r.chk
					switch p.typ {
					case 'S':
						r.peer = kparseParams(p.data)
						r.negotiate()
						r.own.chkt = r.peer.chkt
						data, chk = r.own.bytes(), 1
						r.chk = int(r.peer.chkt - '0')
					case 'D':
						data = []byte(ackData)
					}
					if err := r.write(r.build(p.seq, 'Y', data, chk, false)); err != nil {
						return err
					}
					if p.typ == 'B' {
						return perr
					}
				}
			}
			serr, rerr := runPair(t, a, b,
				func() error {
					return kermitSend(context.Background(), a, sendFiles(files), Options{Timeout: time.Second}, kermitCfg{})
				},
				scripted,
			)
			if rerr != nil {
				t.Fatalf("scripted receiver: %v", rerr)
			}
			got := string(types)
			if ackData == "Z" {
				if !errors.Is(serr, ErrCanceled) {
					t.Errorf("sender returned %v, want ErrCanceled", serr)
				}
				if got != "SFADZB" {
					t.Errorf("packet types %q, want SFADZB", got)
				}
			} else {
				if serr != nil {
					t.Errorf("sender returned %v", serr)
				}
				if got != "SFADZFADZB" {
					t.Errorf("packet types %q, want SFADZFADZB", got)
				}
			}
		})
	}
}

func TestKermitRemoteError(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	go func() {
		r := newKermit(context.Background(), b, Options{Timeout: time.Second}, kermitCfg{})
		defer r.rd.close()
		_, _ = r.readPacket(time.Now().Add(3 * time.Second))
		_, _ = b.Write(r.build(0, 'E', []byte("No space"), 1, false))
	}()
	err := kermitSend(context.Background(), a, sendFiles(kermitBatch()[:1]), Options{Timeout: time.Second}, kermitCfg{})
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("returned %v, want ErrCanceled", err)
	}
}

func TestKermitTimeout(t *testing.T) {
	leakCheck(t)
	a, _ := newDuplex(64 << 10)
	err := KermitSend(context.Background(), a, sendFiles(kermitBatch()[:1]), Options{Timeout: 50 * time.Millisecond, Retries: 2})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("returned %v, want ErrTimeout", err)
	}
	a, _ = newDuplex(64 << 10)
	err = KermitReceive(context.Background(), a, &memSink{}, Options{Timeout: 50 * time.Millisecond, Retries: 2})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("receive returned %v, want ErrTimeout", err)
	}
}

func TestKermitNames(t *testing.T) {
	for in, want := range map[string]string{
		"plain.txt":        "plain.txt",
		"dir/sub/file.c":   "file.c",
		`C:\x\y z.txt`:     "y_z.txt",
		"na#me~&.bin":      "na_me__.bin",
		"caf\u00e9.txt":    "caf__.txt",
		"":                 "unnamed",
		"../../etc/passwd": "passwd",
	} {
		if got := kermitWireName(in, 64); got != want {
			t.Errorf("kermitWireName(%q) = %q, want %q", in, got, want)
		}
	}
	long := kermitWireName("a-very-long-file-name-that-keeps-going-and-going.ext", 20)
	if len(long) != 20 || long[len(long)-4:] != ".ext" {
		t.Errorf("long name = %q", long)
	}
}

// TestKermitHostileName: a sender that announces a path-traversing name gets
// the base name only.
func TestKermitHostileName(t *testing.T) {
	leakCheck(t)
	a, b := newDuplex(64 << 10)
	sink := &memSink{}
	done := make(chan error, 1)
	go func() {
		done <- kermitReceive(context.Background(), b, sink, Options{Timeout: time.Second}, kermitCfg{})
	}()
	s := newKermit(context.Background(), a, Options{Timeout: time.Second}, kermitCfg{})
	defer s.rd.close()
	_, err := s.exchange('S', s.own.bytes())
	if err != nil {
		t.Fatal(err)
	}
	s.peer = s.own
	s.negotiate()
	s.chk = 3
	for _, step := range []struct {
		typ  byte
		data string
	}{{'F', "../../evil.txt"}, {'D', "abc"}, {'Z', ""}, {'B', ""}} {
		if _, err := s.exchange(step.typ, []byte(step.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f := sink.get("evil.txt"); f == nil || f.buf.String() != "abc" {
		t.Errorf("files: %+v", sink.files)
	}
}
