package xfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCRC(t *testing.T) {
	const check = "123456789"
	if got := crc16([]byte(check)); got != 0x31c3 {
		t.Errorf("crc16(%q) = %#04x, want 0x31c3", check, got)
	}
	if got := crc16Update(crc16([]byte(check[:4])), []byte(check[4:])); got != 0x31c3 {
		t.Errorf("incremental crc16 = %#04x, want 0x31c3", got)
	}
	if got := crc32Frame([]byte(check[:8]), check[8]); got != 0xcbf43926 {
		t.Errorf("crc32Frame = %#08x, want 0xcbf43926", got)
	}
	if got := crc16Frame([]byte(check[:8]), check[8]); got != 0x31c3 {
		t.Errorf("crc16Frame = %#04x, want 0x31c3", got)
	}
	if got := checksum8([]byte{0xff, 0x02, 0x80, 0x80}); got != 0x01 {
		t.Errorf("checksum8 = %#02x, want 0x01", got)
	}
}

// TestZmodemHexHeaderWire compares our hex headers with the exact bytes
// lrzsz puts on the wire.
func TestZmodemHexHeaderWire(t *testing.T) {
	a, b := newDuplex(4096)
	x := newSession(context.Background(), a, Options{})
	defer x.rd.close()
	z := newZmodem(x)

	z.sendHex(zhdr{typ: zrqinit})
	if got := string(b.readAll(5 * time.Millisecond)); got != "**\x18B00000000000000\r\x8a\x11" {
		t.Errorf("ZRQINIT = %q", got)
	}
	ri := zhdr{typ: zrinit}
	ri.b[zf0] = canFDX | canOVIO | canFC32
	z.sendHex(ri)
	if got := string(b.readAll(5 * time.Millisecond)); got != "**\x18B0100000023be50\r\x8a\x11" {
		t.Errorf("ZRINIT = %q", got)
	}
	z.sendHex(zhdr{typ: zfin})
	if got := string(b.readAll(5 * time.Millisecond)); got != "**\x18B0800000000022d\r\x8a" {
		t.Errorf("ZFIN = %q", got)
	}
	z.sendPos(zrpos, 0x0102d800)
	if got := string(b.readAll(5 * time.Millisecond)); !strings.HasPrefix(got, "**\x18B0900d80201") {
		t.Errorf("ZRPOS = %q, want the position least significant byte first", got)
	}
}

// TestZmodemFrameRoundTrip encodes and decodes every header format and data
// subpackets with every escaping mode.
func TestZmodemFrameRoundTrip(t *testing.T) {
	payloads := [][]byte{nil, {0}, nastyBytes(), pattern(3, 8192)}
	for _, crc32bit := range []bool{false, true} {
		for _, ctl := range []bool{false, true} {
			a, b := newDuplex(1 << 20)
			tx := newZmodem(newSession(context.Background(), a, Options{EscapeControl: ctl}))
			rx := newZmodem(newSession(context.Background(), b, Options{}))
			rx.rxCtl = ctl
			tx.use32 = crc32bit
			for typ := byte(0); typ < 20; typ++ {
				h := zhdr{typ: typ, b: [4]byte{0x18, 0x11, typ, 0xff}}
				for _, send := range []func(zhdr) error{tx.sendHex, tx.sendBin} {
					if err := send(h); err != nil {
						t.Fatal(err)
					}
					for _, p := range payloads {
						for _, fe := range []byte{zcrce, zcrcg, zcrcq, zcrcw} {
							if err := tx.sendData(p, fe); err != nil {
								t.Fatal(err)
							}
						}
					}
					got, err := rx.readHeader(time.Second)
					if err != nil || got != h {
						t.Fatalf("crc32=%v ctl=%v: header %+v read back as %+v, %v", crc32bit, ctl, h, got, err)
					}
					for _, p := range payloads {
						for _, fe := range []byte{zcrce, zcrcg, zcrcq, zcrcw} {
							data, gotFe, err := rx.readData(maxSubpacket)
							if err != nil || gotFe != fe || !bytes.Equal(data, p) {
								t.Fatalf("crc32=%v ctl=%v: subpacket of %d bytes end %q read back as %d bytes end %q, %v",
									crc32bit, ctl, len(p), fe, len(data), gotFe, err)
							}
						}
					}
				}
			}
			if ctl {
				// With control escaping nothing on the wire is a control
				// character except ZDLE itself and the hex header trailer.
				tx.sendBin(zhdr{typ: zdata})
				b.readAll(time.Millisecond)
				tx.sendData(nastyBytes(), zcrcg)
				rx.rd.close()
				rx = nil
				for _, c := range b.readAll(5 * time.Millisecond) {
					if (c&0x60 == 0 || c&0x7f == 0x7f) && c != zdle {
						t.Fatalf("control character %#02x on the wire despite EscapeControl", c)
					}
				}
			}
			tx.rd.close()
			if rx != nil {
				rx.rd.close()
			}
		}
	}
}

// TestZmodemEscapeRules checks the transmit escaping table against the
// specification.
func TestZmodemEscapeRules(t *testing.T) {
	a, _ := newDuplex(4096)
	x := newSession(context.Background(), a, Options{})
	defer x.rd.close()
	z := newZmodem(x)
	enc := func(in string) string {
		z.out, z.lastTx = z.out[:0], 0
		for i := range len(in) {
			z.putEsc(in[i])
		}
		return string(z.out)
	}
	for in, want := range map[string]string{
		"abc":          "abc",
		"\x18":         "\x18X",
		"\x10\x11\x13": "\x18P\x18Q\x18S",
		"\x90\x91\x93": "\x18\xd0\x18\xd1\x18\xd3",
		"\r":           "\r",           // CR alone passes
		"@\r":          "@\x18M",       // CR after @ is escaped
		"\xc0\x8d":     "\xc0\x18\xcd", // also with the high bits set
		"\x00\x7f\xff": "\x00\x7f\xff",
	} {
		if got := enc(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
	z.setTxEscape(true)
	for in, want := range map[string]string{
		"\x00\x01\x1f": "\x18@\x18A\x18_",
		"\r\n":         "\x18M\x18J",
		"\x7f\xff":     "\x18l\x18m",
		"\x80\x9f":     "\x18\xc0\x18\xdf",
		" ~\xa0":       " ~\xa0",
	} {
		if got := enc(in); got != want {
			t.Errorf("escape-all(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGarbageInput feeds line noise to every entry point: no panic, no hang,
// a clean error.
func TestGarbageInput(t *testing.T) {
	leakCheck(t)
	o := Options{Timeout: 30 * time.Millisecond, Retries: 2}
	noisy := [][]byte{
		pattern(1, 100000),
		bytes.Repeat([]byte("**\x18B"), 5000),
		bytes.Repeat([]byte("*\x18A\x18"), 5000),
		bytes.Repeat([]byte("*\x18C0123456789"), 3000),
		bytes.Repeat([]byte{soh, 0, 0xff}, 10000),
		bytes.Repeat([]byte{stx, 1, 0xfe, eot, 'C', nak}, 5000),
		bytes.Repeat([]byte("**\x18B0100000023be50\r\x8a\x11"), 300), // endless ZRINITs
		bytes.Repeat([]byte("**\x18B00000000000000\r\x8a\x11"), 300), // endless ZRQINITs
		bytes.Repeat([]byte("**\x18B0900000000a87c\r\x8a\x11"), 300), // endless ZRPOS
	}
	files := sendFiles(batch())
	entry := map[string]func(s Stream) error{
		"ZmodemReceive": func(s Stream) error { return ZmodemReceive(context.Background(), s, &memSink{}, o) },
		"ZmodemSend":    func(s Stream) error { return ZmodemSend(context.Background(), s, files, o) },
		"YmodemReceive": func(s Stream) error { return YmodemReceive(context.Background(), s, &memSink{}, o) },
		"YmodemSend":    func(s Stream) error { return YmodemSend(context.Background(), s, files, o) },
		"XmodemReceive": func(s Stream) error {
			_, err := XmodemReceive(context.Background(), s, io.Discard, o)
			return err
		},
		"XmodemSend": func(s Stream) error {
			_, err := XmodemSend(context.Background(), s, bytes.NewReader(pattern(1, 5000)), o)
			return err
		},
	}
	for name, fn := range entry {
		for i, junk := range noisy {
			a, b := newDuplex(1 << 20)
			b.in.unbound()
			a.in.unbound()
			a.Write(junk)
			done := make(chan error, 1)
			go func() { done <- fn(b) }()
			select {
			case err := <-done:
				if err == nil {
					t.Errorf("%s with noise %d succeeded", name, i)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%s with noise %d hangs", name, i)
			}
		}
	}
}

func TestDetector(t *testing.T) {
	const (
		rq = "**\x18B00000000000000\r\x8a\x11"
		ri = "**\x18B0100000023be50\r\x8a\x11"
	)
	cases := []struct {
		name string
		in   string
		kind DetectKind
		idx  int
	}{
		{"empty", "", DetectNone, 0},
		{"text", "hello world\r\n$ ", DetectNone, 15},
		{"sz", "rz\r" + rq, DetectReceive, 0},
		{"sz-after-text", "$ sz file\r\nrz\r" + rq, DetectReceive, 11},
		{"sz-no-rz", "$ " + rq, DetectReceive, 2},
		{"rz", "rz waiting to receive." + ri, DetectSend, 22},
		{"rz-only", ri, DetectSend, 0},
		{"three-pads", "x***\x18B00", DetectReceive, 2},
		{"rz-not-adjacent", "rz\rx" + rq, DetectReceive, 4},
		{"other-frame", "**\x18B0800000000022d\r\x8a", DetectNone, 20},
		{"binary-header", "*\x18A\x00\x00\x00\x00\x00", DetectNone, 8},
		{"restart", "**\x18B**\x18B01", DetectSend, 4},
		{"stars", "*****", DetectNone, 5},
		{"almost", "**\x18B0x**\x18B0", DetectNone, 11},
		{"second-wins", "**\x18B0x" + ri, DetectSend, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d Detector
			kind, idx := d.Feed([]byte(tc.in))
			if kind != tc.kind || idx != tc.idx {
				t.Errorf("Feed(%q) = %v, %d; want %v, %d", tc.in, kind, idx, tc.kind, tc.idx)
			}

			// The result must not depend on how the input is chunked: at
			// every split point the detection happens in the chunk holding
			// the deciding byte, at the same position where possible.
			for split := 0; split <= len(tc.in); split++ {
				var d Detector
				k1, i1 := d.Feed([]byte(tc.in[:split]))
				if k1 != DetectNone {
					// Detected in the first part already: same index.
					if k1 != tc.kind || i1 != tc.idx {
						t.Errorf("split %d: first chunk gives %v, %d", split, k1, i1)
					}
					continue
				}
				if i1 != split {
					t.Errorf("split %d: DetectNone with index %d, want %d", split, i1, split)
				}
				k2, i2 := d.Feed([]byte(tc.in[split:]))
				want := max(tc.idx-split, 0)
				if tc.kind == DetectNone {
					want = len(tc.in) - split
				}
				if k2 != tc.kind || i2 != want {
					t.Errorf("split %d: second chunk gives %v, %d; want %v, %d", split, k2, i2, tc.kind, want)
				}
			}

			// Byte-at-a-time feeding finds it too.
			var d1 Detector
			got := DetectNone
			for i := range len(tc.in) {
				if k, _ := d1.Feed([]byte{tc.in[i]}); k != DetectNone {
					got = k
					break
				}
			}
			if got != tc.kind {
				t.Errorf("byte-wise feeding gives %v, want %v", got, tc.kind)
			}
		})
	}

	t.Run("reset-after-hit", func(t *testing.T) {
		var d Detector
		if k, _ := d.Feed([]byte(rq)); k != DetectReceive {
			t.Fatal("no detection")
		}
		// The rest of the same header must not trigger again.
		if k, _ := d.Feed([]byte("0000000000000\r\x8a\x11")); k != DetectNone {
			t.Error("detected twice")
		}
		if k, _ := d.Feed([]byte(ri)); k != DetectSend {
			t.Error("second session not detected")
		}
	})
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"file.txt":                  "file.txt",
		"dir/file.txt":              "file.txt",
		"/etc/passwd":               "passwd",
		"../../etc/passwd":          "passwd",
		`..\..\windows\system.ini`:  "system.ini",
		`C:\Users\x\file.bin`:       "file.bin",
		"a/b\\c/d.e":                "d.e",
		"  spaced.txt ":             "spaced.txt",
		"tab\tname\x1b[31m.txt":     "tab_name_[31m.txt",
		"new\nline\r.txt\x7f":       "new_line_.txt_",
		"name\x00hidden/../../evil": "name",
		"...":                       "...",
		"..foo":                     "..foo",
		".profile":                  ".profile",
		"üñí.txt":                   "üñí.txt",
	} {
		got, err := sanitizeName(in)
		if err != nil || got != want {
			t.Errorf("sanitizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".", "..", "/", "a/", "a/..", `x\..`, "../", "\x00x", "   ", "dir/."} {
		if got, err := sanitizeName(in); err == nil {
			t.Errorf("sanitizeName(%q) = %q, want an error", in, got)
		}
	}
	for in, want := range map[string]string{
		"a:b*c?.txt": "a_b_c_.txt",
		"con":        "_con",
		"NUL.txt":    "_NUL.txt",
		"com1.log":   "_com1.log",
		"console":    "console",
		"trailing. ": "trailing",
		"<|>\"":      "____",
		"...":        "_",
	} {
		if got := windowsSafeName(in); got != want {
			t.Errorf("windowsSafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFileHeader(t *testing.T) {
	cases := []struct {
		in   string
		want FileInfo
	}{
		{"a.txt\x00", FileInfo{Name: "a.txt", Size: -1}},
		{"a.txt", FileInfo{Name: "a.txt", Size: -1}},
		{"a.txt\x00123\x00", FileInfo{Name: "a.txt", Size: 123}},
		{"a.txt\x00123 14524517704 100644 0 3 9999\x00\x00\x00", FileInfo{Name: "a.txt", Size: 123, ModTime: time.Unix(0o14524517704, 0), Mode: 0o644}},
		{"a.txt\x000 0 0", FileInfo{Name: "a.txt", Size: 0}},
		{"a.txt\x005000000000 0 104755", FileInfo{Name: "a.txt", Size: 5000000000, Mode: 0o4755}},
		{"a.txt\x00junk more junk", FileInfo{Name: "a.txt", Size: -1}},
		{"sub/dir/a.txt\x0010 7 7", FileInfo{Name: "a.txt", Size: 10, ModTime: time.Unix(7, 0), Mode: 7}},
	}
	for _, tc := range cases {
		got, err := parseFileHeader([]byte(tc.in))
		if err != nil || got != tc.want {
			t.Errorf("parseFileHeader(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	if _, err := parseFileHeader([]byte("../..\x0010")); err == nil {
		t.Error("parseFileHeader accepted the name ..")
	}
	// What we build is what we parse.
	info := FileInfo{Name: "dir/round trip.bin", Size: 77, ModTime: testTime, Mode: 0o640}
	got, err := parseFileHeader(buildFileHeader(info, 77, 2, 1000))
	info.Name = "round trip.bin"
	if err != nil || got != info {
		t.Errorf("round trip = %+v, %v; want %+v", got, err, info)
	}
	if hdr := string(buildFileHeader(info, 77, 2, 1000)); hdr != "round trip.bin\x0077 14524770400 100640 0 2 1000\x00" {
		t.Errorf("header = %q", hdr)
	}
}

func TestDirSink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads", "new")
	write := func(s Sink, info FileInfo, data string) (int64, error) {
		t.Helper()
		w, off, err := s.Create(info)
		if err != nil {
			return off, err
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return off, nil
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	info := FileInfo{Name: "report.tar.gz", Size: 5, ModTime: testTime, Mode: 0o640}

	// Creates the directory and the file, applies mode and mtime.
	if _, err := write(DirSink{Dir: dir}, info, "first"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "report.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(testTime) {
		t.Errorf("mtime = %v, want %v", st.ModTime(), testTime)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 640", st.Mode().Perm())
	}

	// Collisions are renamed, never overwritten.
	write(DirSink{Dir: dir}, info, "second")
	write(DirSink{Dir: dir}, info, "third")
	if read("report.tar.gz") != "first" || read("report.tar (1).gz") != "second" || read("report.tar (2).gz") != "third" {
		t.Errorf("auto-rename went wrong")
	}
	write(DirSink{Dir: dir}, FileInfo{Name: ".profile", Size: -1}, "a")
	write(DirSink{Dir: dir}, FileInfo{Name: ".profile", Size: -1}, "b")
	if read(".profile") != "a" || read(".profile (1)") != "b" {
		t.Errorf("auto-rename of a dot file went wrong")
	}

	// Overwrite replaces.
	write(DirSink{Dir: dir, Overwrite: true}, info, "new")
	if read("report.tar.gz") != "new" {
		t.Errorf("overwrite: file holds %q", read("report.tar.gz"))
	}

	// Resume: shorter file is continued, complete file is skipped, longer
	// file is a collision, unknown size is a collision.
	big := FileInfo{Name: "report.tar.gz", Size: 8}
	off, err := write(DirSink{Dir: dir, Resume: true}, big, "+tail")
	if err != nil || off != 3 || read("report.tar.gz") != "new+tail" {
		t.Errorf("resume: offset %d, err %v, file %q", off, err, read("report.tar.gz"))
	}
	if _, err := write(DirSink{Dir: dir, Resume: true}, big, ""); !errors.Is(err, ErrSkip) {
		t.Errorf("complete file: err = %v, want ErrSkip", err)
	}
	if off, err := write(DirSink{Dir: dir, Resume: true}, FileInfo{Name: "report.tar.gz", Size: 2}, "xx"); err != nil || off != 0 || read("report.tar (3).gz") != "xx" {
		t.Errorf("longer existing file: offset %d, err %v", off, err)
	}
	if off, err := write(DirSink{Dir: dir, Resume: true}, FileInfo{Name: "report.tar.gz", Size: -1}, "yy"); err != nil || off != 0 || read("report.tar (4).gz") != "yy" {
		t.Errorf("unknown size: offset %d, err %v", off, err)
	}

	// Traversal attempts end up inside the directory.
	if _, err := write(DirSink{Dir: dir}, FileInfo{Name: "../../escape.txt", Size: 1}, "x"); err != nil {
		t.Fatal(err)
	}
	if read("escape.txt") != "x" {
		t.Error("traversal name not confined")
	}
	if _, _, err := (DirSink{Dir: dir}).Create(FileInfo{Name: ".."}); err == nil {
		t.Error("the name .. was accepted")
	}

	// A directory or symlink in the way is not followed or replaced.
	os.Mkdir(filepath.Join(dir, "taken"), 0o755)
	write(DirSink{Dir: dir, Overwrite: true}, FileInfo{Name: "taken", Size: 1}, "z")
	if read("taken (1)") != "z" {
		t.Error("directory collision not renamed")
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.txt")
		os.WriteFile(outside, []byte("precious"), 0o644)
		if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
			t.Fatal(err)
		}
		write(DirSink{Dir: dir, Overwrite: true, Resume: true}, FileInfo{Name: "link.txt", Size: 100}, "attack")
		if b, _ := os.ReadFile(outside); string(b) != "precious" {
			t.Errorf("wrote through a symlink: %q", b)
		}
		if read("link (1).txt") != "attack" {
			t.Error("symlink collision not renamed")
		}
	}
}

func TestSendFileFromPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "src.bin")
	os.WriteFile(path, []byte("content"), 0o640)
	os.Chtimes(path, testTime, testTime)
	f, err := SendFileFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Info.Name != "src.bin" || f.Info.Size != 7 || !f.Info.ModTime.Equal(testTime) {
		t.Errorf("info = %+v", f.Info)
	}
	if runtime.GOOS != "windows" && f.Info.Mode != 0o640 {
		t.Errorf("mode = %o", f.Info.Mode)
	}
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "content" {
		t.Errorf("content = %q", b)
	}
	if _, err := SendFileFromPath(dir); err == nil {
		t.Error("a directory was accepted")
	}
	if _, err := SendFileFromPath(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestPipeStream(t *testing.T) {
	leakCheck(t)

	t.Run("read-write-timeout", func(t *testing.T) {
		pr, pw := io.Pipe()
		var out bytes.Buffer
		ps := NewPipeStream(pr, &out)
		defer pr.Close()
		defer ps.Close()

		buf := make([]byte, 16)
		start := time.Now()
		if n, err := ps.ReadTimeout(buf, 30*time.Millisecond); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("ReadTimeout on an idle stream = %d, %v", n, err)
		}
		if d := time.Since(start); d < 25*time.Millisecond || d > 2*time.Second {
			t.Errorf("timeout after %v, want about 30ms", d)
		}
		if n, err := ps.ReadTimeout(buf, 0); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("poll on an idle stream = %d, %v", n, err)
		}
		go pw.Write([]byte("hello world"))
		n, err := ps.ReadTimeout(buf[:5], time.Second)
		if err != nil || string(buf[:n]) != "hello" {
			t.Errorf("ReadTimeout = %q, %v", buf[:n], err)
		}
		ps.Unread([]byte(">>"))
		if got := string(ps.Buffered()); got != ">> world" {
			t.Errorf("Buffered = %q", got)
		}
		n, _ = ps.ReadTimeout(buf, time.Second)
		if string(buf[:n]) != ">> world" {
			t.Errorf("after Unread: %q", buf[:n])
		}
		ps.Write([]byte("out"))
		if out.String() != "out" {
			t.Errorf("Write did not reach the writer")
		}
		pw.Close()
		if _, err := ps.ReadTimeout(buf, time.Second); err != io.EOF {
			t.Errorf("after the reader ended: %v, want io.EOF", err)
		}
	})

	t.Run("close-interrupts-pump", func(t *testing.T) {
		// With a deadline-capable reader Close stops the pump without
		// closing the reader, which stays usable.
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		ps := NewPipeStream(c1, c1)
		go c2.Write([]byte("abc"))
		buf := make([]byte, 8)
		if n, err := ps.ReadTimeout(buf, time.Second); err != nil || string(buf[:n]) != "abc" {
			t.Fatalf("ReadTimeout = %q, %v", buf[:n], err)
		}
		ps.Close()
		ps.Close() // idempotent
		select {
		case <-ps.done:
		default:
			t.Error("pump still running after Close")
		}
		if _, err := ps.ReadTimeout(buf, time.Second); !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("ReadTimeout after Close = %v", err)
		}
		go c2.Write([]byte("xyz"))
		c1.SetReadDeadline(time.Now().Add(time.Second))
		if n, err := c1.Read(buf); err != nil || string(buf[:n]) != "xyz" {
			t.Errorf("reader unusable after Close: %q, %v", buf[:n], err)
		}
	})

	t.Run("backpressure", func(t *testing.T) {
		pr, pw := io.Pipe()
		ps := NewPipeStream(pr, io.Discard)
		defer ps.Close()
		defer pr.Close()
		total := 3 * pipeStreamHighWater
		wrote := make(chan struct{})
		go func() {
			pw.Write(make([]byte, total))
			close(wrote)
		}()
		time.Sleep(50 * time.Millisecond)
		select {
		case <-wrote:
			t.Fatal("the pump buffered without limit")
		default:
		}
		if n := len(ps.Buffered()); n > pipeStreamHighWater+64<<10 {
			t.Errorf("pump holds %d bytes, limit is %d", n, pipeStreamHighWater)
		}
		buf := make([]byte, 64<<10)
		for got := 0; got < total; {
			n, err := ps.ReadTimeout(buf, 5*time.Second)
			if err != nil {
				t.Fatalf("after %d bytes: %v", got, err)
			}
			got += n
		}
		<-wrote
	})

	t.Run("transfer", func(t *testing.T) {
		// A whole ZMODEM session over two PipeStreams back to back.
		ar, bw := io.Pipe()
		br, aw := io.Pipe()
		a, b := NewPipeStream(ar, aw), NewPipeStream(br, bw)
		files := batch()
		sink := &memSink{}
		errs := make(chan error, 2)
		go func() { errs <- ZmodemSend(context.Background(), a, sendFiles(files), Options{}) }()
		go func() { errs <- ZmodemReceive(context.Background(), b, sink, Options{}) }()
		for range 2 {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		checkBatch(t, sink, files, true)
		a.Close()
		b.Close()
		ar.Close()
		br.Close()
	})
}

// TestBrokenStream: a stream that fails in mid-transfer ends the transfer
// with the stream's error; a stream that violates the contract by returning
// (0, nil) does not make the pump spin.
func TestBrokenStream(t *testing.T) {
	leakCheck(t)
	pr, pw := io.Pipe()
	ps := NewPipeStream(pr, io.Discard)
	go func() {
		pw.Write([]byte("rz\r**\x18B00000000000000\r\x8a\x11"))
		time.Sleep(20 * time.Millisecond)
		pw.CloseWithError(io.ErrUnexpectedEOF)
	}()
	err := ZmodemReceive(context.Background(), ps, &memSink{}, Options{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("ZmodemReceive = %v, want the stream error", err)
	}
	ps.Close()

	calls := 0
	lazy := streamFunc(func(p []byte, d time.Duration) (int, error) { calls++; return 0, nil })
	start := time.Now()
	_, err = XmodemReceive(context.Background(), lazy, io.Discard, Options{Timeout: 20 * time.Millisecond, Retries: 2})
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("XmodemReceive = %v, want ErrTimeout", err)
	}
	if rate := float64(calls) / time.Since(start).Seconds(); rate > 2000 {
		t.Errorf("pump polled a misbehaving stream %.0f times per second", rate)
	}
}

type streamFunc func(p []byte, d time.Duration) (int, error)

func (f streamFunc) ReadTimeout(p []byte, d time.Duration) (int, error) { return f(p, d) }
func (f streamFunc) Write(p []byte) (int, error)                        { return len(p), nil }

func TestEventKindString(t *testing.T) {
	for k, want := range map[EventKind]string{FileStart: "FileStart", Progress: "Progress", FileDone: "FileDone", FileSkipped: "FileSkipped", Error: "Error", Done: "Done"} {
		if k.String() != want {
			t.Errorf("%d.String() = %q", k, k.String())
		}
	}
	for k, want := range map[DetectKind]string{DetectNone: "DetectNone", DetectReceive: "DetectReceive", DetectSend: "DetectSend"} {
		if k.String() != want {
			t.Errorf("%d.String() = %q", k, k.String())
		}
	}
}
