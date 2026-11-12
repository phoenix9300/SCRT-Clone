package xfer

// Interoperability tests against lrzsz (rz, sz, rb, sb, rx, sx). The tools
// speak the protocol on stdin/stdout, so each test wires a subprocess to a
// PipeStream. The tests are skipped when lrzsz is not installed.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// remote is a running lrzsz process connected to a PipeStream.
type remote struct {
	t     *testing.T
	cmd   *exec.Cmd
	ps    *PipeStream
	stdin io.WriteCloser

	mu     sync.Mutex
	stderr bytes.Buffer
	waited bool
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// startRemote runs an lrzsz tool (or a shell command line, if name is "sh")
// in dir and connects it to a PipeStream.
func startRemote(t *testing.T, dir, name string, args ...string) *remote {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("lrzsz interoperability tests need a Unix system")
	}
	need := name
	if name == "sh" {
		need = strings.Trim(strings.Fields(args[1])[0], ";")
	}
	if _, err := exec.LookPath(need); err != nil {
		t.Skipf("%s is not installed", need)
	}
	r := &remote{t: t}
	r.cmd = exec.Command(name, args...)
	r.cmd.Dir = dir
	r.cmd.Stderr = lockedWriter{&r.mu, &r.stderr}
	var err error
	if r.stdin, err = r.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.ps = NewPipeStream(stdout, r.stdin)
	t.Cleanup(func() {
		if !r.waited {
			r.cmd.Process.Kill()
			r.stdin.Close()
			r.cmd.Wait()
		}
		r.ps.Close()
	})
	return r
}

// finish waits for the remote process to exit. It returns everything the
// process wrote that the transfer did not consume, and its exit error.
func (r *remote) finish() (tail []byte, err error) {
	r.t.Helper()
	buf := make([]byte, 4096)
	deadline := time.Now().Add(20 * time.Second)
	for {
		n, rerr := r.ps.ReadTimeout(buf, 100*time.Millisecond)
		tail = append(tail, buf[:n]...)
		if rerr == nil {
			continue
		}
		if !isTimeout(rerr) {
			break // EOF: the process closed its stdout
		}
		if time.Now().After(deadline) {
			r.cmd.Process.Kill()
			r.t.Errorf("remote %s did not exit; stderr: %q", r.cmd.Path, r.errOutput())
			break
		}
	}
	r.stdin.Close()
	err = r.cmd.Wait()
	r.waited = true
	return tail, err
}

func (r *remote) errOutput() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stderr.String()
}

// diskFiles writes the standard batch to a directory and returns the paths.
func diskFiles(t *testing.T, dir string, files []memFile) []string {
	t.Helper()
	var paths []string
	for _, f := range files {
		p := filepath.Join(dir, f.info.Name)
		if err := os.WriteFile(p, f.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, os.FileMode(f.info.Mode)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, f.info.ModTime, f.info.ModTime); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// checkDir verifies that dir holds exactly the given files, with content
// and (optionally) mode and modification time.
func checkDir(t *testing.T, dir string, want []memFile, meta bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names, wantNames []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for _, w := range want {
		wantNames = append(wantNames, w.info.Name)
	}
	slices.Sort(names)
	slices.Sort(wantNames)
	if !slices.Equal(names, wantNames) {
		t.Errorf("directory holds %q, want %q", names, wantNames)
	}
	for _, w := range want {
		p := filepath.Join(dir, w.info.Name)
		got, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", w.info.Name, err)
			continue
		}
		if !bytes.Equal(got, w.data) {
			t.Errorf("%s: content differs (got %d bytes, want %d)%s", w.info.Name, len(got), len(w.data), firstDiff(got, w.data))
		}
		if !meta {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			t.Error(err)
			continue
		}
		if !st.ModTime().Equal(w.info.ModTime) {
			t.Errorf("%s: mtime %v, want %v", w.info.Name, st.ModTime(), w.info.ModTime)
		}
		// DirSink (our receiving side) deliberately drops execute bits; rz keeps them
		if got := st.Mode().Perm(); got != os.FileMode(w.info.Mode) && got != os.FileMode(w.info.Mode&0o666) {
			t.Errorf("%s: mode %o, want %o", w.info.Name, st.Mode().Perm(), w.info.Mode)
		}
	}
}

func interopCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// lrzszBatch is the standard batch plus a larger file.
func lrzszBatch() []memFile {
	return append(batch(), memFile{FileInfo{Name: "big.bin", ModTime: testTime.Add(-24 * time.Hour), Mode: 0o644}, pattern(99, 1<<20+17)})
}

// TestInteropSzToZmodemReceive: lrzsz "sz" sends, we receive.
func TestInteropSzToZmodemReceive(t *testing.T) {
	cases := []struct {
		name string
		args []string
		o    Options
	}{
		{"default", nil, Options{}},
		{"sz-binary", []string{"-b"}, Options{}},
		{"sz-escape", []string{"-e"}, Options{}},
		{"sz-crc16", []string{"-o"}, Options{}},
		{"sz-8k", []string{"-8"}, Options{}},
		{"sz-start-8k", []string{"--start-8k"}, Options{}},
		{"sz-4k", []string{"--start-4k"}, Options{}},
		{"sz-window", []string{"-w", "4096"}, Options{}},
		{"sz-packetlen", []string{"-L", "200", "-l", "1000"}, Options{}},
		{"sz-escape-crc16-window", []string{"-e", "-o", "-w", "8192"}, Options{}},
		{"we-escape", nil, Options{EscapeControl: true}},
		{"we-crc16", nil, Options{CRC16: true}},
		{"we-escape-crc16", []string{"-8"}, Options{EscapeControl: true, CRC16: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			src, dst := t.TempDir(), t.TempDir()
			files := lrzszBatch()
			paths := diskFiles(t, src, files)
			r := startRemote(t, src, "sz", append(tc.args, paths...)...)
			var log eventLog
			tc.o.OnEvent = log.add
			err := ZmodemReceive(interopCtx(t), r.ps, DirSink{Dir: dst}, tc.o)
			tail, exit := r.finish()
			if err != nil || exit != nil {
				t.Fatalf("ZmodemReceive: %v; sz exit: %v; stderr: %q", err, exit, r.errOutput())
			}
			checkDir(t, dst, files, true)
			if len(tail) != 0 {
				t.Errorf("unconsumed protocol bytes: %q", tail)
			}
			if n := log.count(FileDone); n != len(files) {
				t.Errorf("%d FileDone events, want %d", n, len(files))
			}
		})
	}
}

// TestInteropZmodemSendToRz: we send, lrzsz "rz" receives.
func TestInteropZmodemSendToRz(t *testing.T) {
	cases := []struct {
		name string
		args []string
		o    Options
	}{
		{"default", nil, Options{}},
		{"rz-binary", []string{"-b"}, Options{}},
		{"rz-escape", []string{"-e"}, Options{}},
		{"we-escape", nil, Options{EscapeControl: true}},
		{"we-crc16", nil, Options{CRC16: true}},
		{"we-window", nil, Options{WindowSize: 4096}},
		{"we-window-small-blocks", nil, Options{WindowSize: 1024, BlockSize: 256}},
		{"we-8k", nil, Options{BlockSize: 8192}},
		{"we-8k-escape-crc16-window", nil, Options{BlockSize: 8192, EscapeControl: true, CRC16: true, WindowSize: 32768}},
		{"rz-window", []string{"-w", "2048"}, Options{}},
		{"rz-errors", []string{"--errors", "50000"}, Options{}},
		{"rz-errors-8k-window", []string{"--errors", "70000"}, Options{BlockSize: 8192, WindowSize: 65536}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leakCheck(t)
			src, dst := t.TempDir(), t.TempDir()
			files := lrzszBatch()
			var send []SendFile
			for _, p := range diskFiles(t, src, files) {
				f, err := SendFileFromPath(p)
				if err != nil {
					t.Fatal(err)
				}
				send = append(send, f)
			}
			r := startRemote(t, dst, "rz", tc.args...)
			var log eventLog
			tc.o.OnEvent = log.add
			err := ZmodemSend(interopCtx(t), r.ps, send, tc.o)
			tail, exit := r.finish()
			if err != nil || exit != nil {
				t.Fatalf("ZmodemSend: %v; rz exit: %v; stderr: %q", err, exit, r.errOutput())
			}
			checkDir(t, dst, files, true)
			if len(tail) != 0 {
				t.Errorf("unconsumed protocol bytes: %q", tail)
			}
			if n := log.count(FileDone); n != len(files) {
				t.Errorf("%d FileDone events, want %d", n, len(files))
			}
		})
	}
}

// TestInteropZmodemExistingFile: what rz does with a file it already has,
// depending on our options and its flags.
func TestInteropZmodemExistingFile(t *testing.T) {
	data := pattern(5, 100000)
	file := memFile{FileInfo{Name: "exists.bin", ModTime: testTime, Mode: 0o644}, data}
	run := func(t *testing.T, existing []byte, o Options, rzArgs ...string) (events []EventKind, result []byte) {
		t.Helper()
		src, dst := t.TempDir(), t.TempDir()
		f, err := SendFileFromPath(diskFiles(t, src, []memFile{file})[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, file.info.Name), existing, 0o644); err != nil {
			t.Fatal(err)
		}
		r := startRemote(t, dst, "rz", rzArgs...)
		var log eventLog
		o.OnEvent = log.add
		err = ZmodemSend(interopCtx(t), r.ps, []SendFile{f}, o)
		_, exit := r.finish()
		if err != nil {
			t.Fatalf("ZmodemSend: %v; rz exit: %v; stderr: %q", err, exit, r.errOutput())
		}
		result, _ = os.ReadFile(filepath.Join(dst, file.info.Name))
		return log.kinds(true), result
	}

	t.Run("protected-by-default", func(t *testing.T) {
		leakCheck(t)
		events, result := run(t, []byte("old"), Options{})
		if !slices.Equal(events, []EventKind{FileSkipped, Done}) || string(result) != "old" {
			t.Errorf("events %v, file %d bytes; want the file skipped and untouched", events, len(result))
		}
	})
	t.Run("rz-overwrite", func(t *testing.T) {
		leakCheck(t)
		events, result := run(t, []byte("old"), Options{}, "-y")
		if !slices.Equal(events, []EventKind{FileStart, FileDone, Done}) || !bytes.Equal(result, data) {
			t.Errorf("events %v, file %d bytes; want the file replaced", events, len(result))
		}
	})
	t.Run("we-overwrite", func(t *testing.T) {
		leakCheck(t)
		events, result := run(t, []byte("old"), Options{Overwrite: true})
		if !slices.Equal(events, []EventKind{FileStart, FileDone, Done}) || !bytes.Equal(result, data) {
			t.Errorf("events %v, file %d bytes; want the file replaced", events, len(result))
		}
	})
	t.Run("we-resume", func(t *testing.T) {
		leakCheck(t)
		var start int64 = -1
		src, dst := t.TempDir(), t.TempDir()
		f, _ := SendFileFromPath(diskFiles(t, src, []memFile{file})[0])
		os.WriteFile(filepath.Join(dst, file.info.Name), data[:65536], 0o644)
		r := startRemote(t, dst, "rz")
		err := ZmodemSend(interopCtx(t), r.ps, []SendFile{f}, Options{Resume: true, OnEvent: func(e Event) {
			if e.Kind == FileStart {
				start = e.Bytes
			}
		}})
		_, exit := r.finish()
		if err != nil || exit != nil {
			t.Fatalf("ZmodemSend: %v; rz exit: %v; stderr: %q", err, exit, r.errOutput())
		}
		result, _ := os.ReadFile(filepath.Join(dst, file.info.Name))
		if !bytes.Equal(result, data) {
			t.Errorf("resumed file differs: %d bytes", len(result))
		}
		if start != 65536 {
			t.Errorf("transfer started at %d, want 65536", start)
		}
	})
}

// TestInteropSzResume: crash recovery when receiving from sz.
func TestInteropSzResume(t *testing.T) {
	for _, args := range [][]string{nil, {"-r"}} {
		t.Run(strings.Join(append([]string{"sz"}, args...), ""), func(t *testing.T) {
			leakCheck(t)
			src, dst := t.TempDir(), t.TempDir()
			data := pattern(8, 500000)
			file := memFile{FileInfo{Name: "partial.bin", ModTime: testTime, Mode: 0o644}, data}
			paths := diskFiles(t, src, []memFile{file})
			const have = 300000
			if err := os.WriteFile(filepath.Join(dst, "partial.bin"), data[:have], 0o644); err != nil {
				t.Fatal(err)
			}
			r := startRemote(t, src, "sz", append(args, paths...)...)
			var start int64 = -1
			var wire int64
			counting := &countingStream{Stream: r.ps, n: &wire}
			err := ZmodemReceive(interopCtx(t), counting, DirSink{Dir: dst, Resume: true}, Options{OnEvent: func(e Event) {
				if e.Kind == FileStart {
					start = e.Bytes
				}
			}})
			_, exit := r.finish()
			if err != nil || exit != nil {
				t.Fatalf("ZmodemReceive: %v; sz exit: %v; stderr: %q", err, exit, r.errOutput())
			}
			checkDir(t, dst, []memFile{file}, true)
			if start != have {
				t.Errorf("transfer started at %d, want %d", start, have)
			}
			if wire > int64(len(data)-have)+20000 {
				t.Errorf("%d bytes received for %d missing bytes", wire, len(data)-have)
			}
		})
	}
}

// countingStream counts the bytes read from a Stream.
type countingStream struct {
	Stream
	n *int64
}

func (c *countingStream) ReadTimeout(p []byte, d time.Duration) (int, error) {
	n, err := c.Stream.ReadTimeout(p, d)
	*c.n += int64(n)
	return n, err
}

// TestInteropZmodemTerminalResumes: the transfer consumes exactly the
// protocol bytes; what the remote shell prints next is left for the terminal.
func TestInteropZmodemTerminalResumes(t *testing.T) {
	const prompt = "*** done ***\r\nuser@host:~$ "
	files := batch()

	t.Run("sz", func(t *testing.T) {
		leakCheck(t)
		src, dst := t.TempDir(), t.TempDir()
		diskFiles(t, src, files)
		r := startRemote(t, src, "sh", "-c", `sz *; printf '*** done ***\r\nuser@host:~$ '`)
		err := ZmodemReceive(interopCtx(t), r.ps, DirSink{Dir: dst}, Options{})
		tail, exit := r.finish()
		if err != nil || exit != nil {
			t.Fatalf("ZmodemReceive: %v; exit: %v; stderr: %q", err, exit, r.errOutput())
		}
		checkDir(t, dst, files, true)
		if string(tail) != prompt {
			t.Errorf("terminal gets %q, want %q", tail, prompt)
		}
	})

	t.Run("rz", func(t *testing.T) {
		leakCheck(t)
		src, dst := t.TempDir(), t.TempDir()
		var send []SendFile
		for _, p := range diskFiles(t, src, files) {
			f, _ := SendFileFromPath(p)
			send = append(send, f)
		}
		r := startRemote(t, dst, "sh", "-c", `rz; printf '*** done ***\r\nuser@host:~$ '`)
		err := ZmodemSend(interopCtx(t), r.ps, send, Options{})
		tail, exit := r.finish()
		if err != nil || exit != nil {
			t.Fatalf("ZmodemSend: %v; exit: %v; stderr: %q", err, exit, r.errOutput())
		}
		checkDir(t, dst, files, true)
		if string(tail) != prompt {
			t.Errorf("terminal gets %q, want %q", tail, prompt)
		}
	})
}

// TestInteropDetector runs the Detector over what rz and sz really print and
// starts the transfer from the detection, the way the terminal does.
func TestInteropDetector(t *testing.T) {
	files := batch()[:4]

	// firstOutput returns what the remote printed first, preceded by some
	// ordinary terminal output.
	firstOutput := func(t *testing.T, r *remote) []byte {
		t.Helper()
		buf := make([]byte, 4096)
		out := []byte("$ some command\r\n")
		deadline := time.Now().Add(10 * time.Second)
		for !bytes.Contains(out, []byte("\x11")) {
			n, err := r.ps.ReadTimeout(buf, time.Second)
			out = append(out, buf[:n]...)
			if err != nil && (!isTimeout(err) || time.Now().After(deadline)) {
				t.Fatalf("no announcement from the remote: %v (got %q)", err, out)
			}
		}
		return out
	}

	t.Run("sz", func(t *testing.T) {
		leakCheck(t)
		src, dst := t.TempDir(), t.TempDir()
		paths := diskFiles(t, src, files)
		r := startRemote(t, src, "sz", paths...)
		out := firstOutput(t, r)
		var d Detector
		kind, idx := d.Feed(out)
		if kind != DetectReceive {
			t.Fatalf("Feed(%q) = %v, want DetectReceive", out, kind)
		}
		if got := string(out[:idx]); got != "$ some command\r\n" {
			t.Errorf("terminal would print %q", got)
		}
		if got := string(out[idx:]); got != "rz\r**\x18B00000000000000\r\x8a\x11" {
			t.Errorf("sz announcement = %q", got)
		}
		r.ps.Unread(out[idx:])
		err := ZmodemReceive(interopCtx(t), r.ps, DirSink{Dir: dst}, Options{})
		_, exit := r.finish()
		if err != nil || exit != nil {
			t.Fatalf("ZmodemReceive: %v; exit: %v", err, exit)
		}
		checkDir(t, dst, files, true)
	})

	t.Run("rz", func(t *testing.T) {
		leakCheck(t)
		src, dst := t.TempDir(), t.TempDir()
		var send []SendFile
		for _, p := range diskFiles(t, src, files) {
			f, _ := SendFileFromPath(p)
			send = append(send, f)
		}
		r := startRemote(t, dst, "rz")
		out := firstOutput(t, r)
		var d Detector
		kind, idx := d.Feed(out)
		if kind != DetectSend {
			t.Fatalf("Feed(%q) = %v, want DetectSend", out, kind)
		}
		if got := string(out[:idx]); got != "$ some command\r\n" {
			t.Errorf("terminal would print %q", got)
		}
		if !strings.HasPrefix(string(out[idx:]), "**\x18B01") {
			t.Errorf("rz announcement = %q", out[idx:])
		}
		r.ps.Unread(out[idx:])
		err := ZmodemSend(interopCtx(t), r.ps, send, Options{})
		_, exit := r.finish()
		if err != nil || exit != nil {
			t.Fatalf("ZmodemSend: %v; exit: %v", err, exit)
		}
		checkDir(t, dst, files, true)
	})

	t.Run("rz-announcement-lost", func(t *testing.T) {
		// The terminal printed the announcement instead of detecting it
		// (auto-start disabled) and the user starts the upload by hand.
		leakCheck(t)
		src, dst := t.TempDir(), t.TempDir()
		var send []SendFile
		for _, p := range diskFiles(t, src, files) {
			f, _ := SendFileFromPath(p)
			send = append(send, f)
		}
		r := startRemote(t, dst, "rz")
		firstOutput(t, r)
		err := ZmodemSend(interopCtx(t), r.ps, send, Options{})
		_, exit := r.finish()
		if err != nil || exit != nil {
			t.Fatalf("ZmodemSend: %v; exit: %v", err, exit)
		}
		checkDir(t, dst, files, true)
	})
}

// TestInteropZmodemCancel: cancelling our side stops the remote program and
// leaves the terminal usable.
func TestInteropZmodemCancel(t *testing.T) {
	t.Run("receive-from-sz", func(t *testing.T) {
		leakCheck(t)
		src := t.TempDir()
		big := memFile{FileInfo{Name: "huge.bin", ModTime: testTime, Mode: 0o644}, pattern(1, 24<<20)}
		diskFiles(t, src, []memFile{big})
		r := startRemote(t, src, "sh", "-c", `sz huge.bin; printf 'exit=%d\r\n$ ' $?`)
		ctx, cancel := context.WithCancel(interopCtx(t))
		w := &hookWriter{at: 1 << 20, fn: cancel}
		sink := sinkFunc(func(FileInfo) (io.WriteCloser, int64, error) { return w, 0, nil })
		err := ZmodemReceive(ctx, r.ps, sink, Options{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ZmodemReceive = %v, want context.Canceled", err)
		}
		tail, _ := r.finish()
		if w.n >= int64(len(big.data)) {
			t.Errorf("the whole file arrived despite the cancellation")
		}
		// sz gave up (non-zero exit status) and the shell prompt reaches the
		// terminal without megabytes of file data in front of it.
		if !strings.HasSuffix(string(tail), "\r\n$ ") || strings.Contains(string(tail), "exit=0") {
			t.Errorf("tail = %q", lastBytes(tail, 80))
		}
		if len(tail) > 200 {
			t.Errorf("%d bytes of leftover data reach the terminal: %q ...", len(tail), lastBytes(tail, 80))
		}
	})

	t.Run("send-to-rz", func(t *testing.T) {
		leakCheck(t)
		dst := t.TempDir()
		data := pattern(2, 24<<20)
		r := startRemote(t, dst, "sh", "-c", `rz; printf 'exit=%d\r\n$ ' $?`)
		ctx, cancel := context.WithCancel(interopCtx(t))
		send := []SendFile{{
			Info: FileInfo{Name: "huge.bin", Size: int64(len(data))},
			Open: func() (io.ReadSeekCloser, error) {
				return &hookReader{Reader: bytes.NewReader(data), at: 1 << 20, fn: cancel}, nil
			},
		}}
		err := ZmodemSend(ctx, r.ps, send, Options{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ZmodemSend = %v, want context.Canceled", err)
		}
		tail, _ := r.finish()
		if !strings.HasSuffix(string(tail), "\r\n$ ") || strings.Contains(string(tail), "exit=0") {
			t.Errorf("tail = %q", lastBytes(tail, 80))
		}
		if len(tail) > 200 {
			t.Errorf("%d bytes of leftover data reach the terminal", len(tail))
		}
	})
}

func lastBytes(p []byte, n int) []byte {
	if len(p) > n {
		return p[len(p)-n:]
	}
	return p
}

// TestInteropYmodem: lrzsz "sb" and "rb".
func TestInteropYmodem(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		o    Options
	}{
		{"sb", nil, Options{}},
		{"sb-1k", []string{"-k"}, Options{}},
		{"sb-g", nil, Options{YmodemG: true}},
		{"sb-1k-g", []string{"-k"}, Options{YmodemG: true}},
	} {
		t.Run(tc.name+"-to-YmodemReceive", func(t *testing.T) {
			leakCheck(t)
			src, dst := t.TempDir(), t.TempDir()
			files := lrzszBatch()
			paths := diskFiles(t, src, files)
			r := startRemote(t, src, "sb", append(tc.args, paths...)...)
			err := YmodemReceive(interopCtx(t), r.ps, DirSink{Dir: dst}, tc.o)
			tail, exit := r.finish()
			if err != nil || exit != nil {
				t.Fatalf("YmodemReceive: %v; sb exit: %v; stderr: %q", err, exit, r.errOutput())
			}
			checkDir(t, dst, files, true)
			if len(tail) != 0 {
				t.Errorf("unconsumed protocol bytes: %q", tail)
			}
		})
	}
	// lrzsz's rb pauses for a second at its start and around every EOT, so
	// these batches are kept short.
	all := lrzszBatch()
	for _, tc := range []struct {
		name  string
		o     Options
		files []memFile
	}{
		{"128", Options{}, []memFile{all[2], all[4]}},
		{"1k", Options{Use1K: true}, []memFile{all[0], all[5], all[6]}},
	} {
		t.Run("YmodemSend-"+tc.name+"-to-rb", func(t *testing.T) {
			leakCheck(t)
			src, dst := t.TempDir(), t.TempDir()
			files := tc.files
			var send []SendFile
			for _, p := range diskFiles(t, src, files) {
				f, _ := SendFileFromPath(p)
				send = append(send, f)
			}
			r := startRemote(t, dst, "rb")
			err := YmodemSend(interopCtx(t), r.ps, send, tc.o)
			tail, exit := r.finish()
			if err != nil || exit != nil {
				t.Fatalf("YmodemSend: %v; rb exit: %v; stderr: %q", err, exit, r.errOutput())
			}
			checkDir(t, dst, files, true)
			if len(tail) != 0 {
				t.Errorf("unconsumed protocol bytes: %q", tail)
			}
		})
	}
}

// TestInteropXmodem: lrzsz "sx" and "rx" in plain, CRC and 1K flavours.
func TestInteropXmodem(t *testing.T) {
	data := append(bytes.Repeat(nastyBytes(), 3), pattern(4, 100001)...)
	data[len(data)-1] = 'x' // so that TrimPadding restores the exact file

	for _, tc := range []struct {
		name string
		args []string
		o    Options
	}{
		{"sx", nil, Options{}},
		{"sx-crc", nil, Options{UseCRC: true}},
		{"sx-1k", []string{"-k"}, Options{}},
		{"sx-1k-crc", []string{"-k"}, Options{UseCRC: true}},
		{"sx-1k-crc-trim", []string{"-k"}, Options{UseCRC: true, TrimPadding: true}},
	} {
		t.Run(tc.name+"-to-XmodemReceive", func(t *testing.T) {
			leakCheck(t)
			src := t.TempDir()
			path := diskFiles(t, src, []memFile{{FileInfo{Name: "x.bin", ModTime: testTime, Mode: 0o644}, data}})[0]
			r := startRemote(t, src, "sx", append(tc.args, path)...)
			var out bytes.Buffer
			n, err := XmodemReceive(interopCtx(t), r.ps, &out, tc.o)
			tail, exit := r.finish()
			if err != nil || exit != nil {
				t.Fatalf("XmodemReceive: %v; sx exit: %v; stderr: %q", err, exit, r.errOutput())
			}
			got := out.Bytes()
			if n != int64(len(got)) {
				t.Errorf("returned %d, wrote %d bytes", n, len(got))
			}
			if tc.o.TrimPadding {
				if !bytes.Equal(got, data) {
					t.Errorf("content differs: %d bytes, want %d", len(got), len(data))
				}
			} else {
				// lrzsz pads to its block size, which may be 128 or 1024.
				if !bytes.HasPrefix(got, data) || len(got)-len(data) >= 1024 ||
					len(bytes.TrimRight(got[len(data):], "\x1a")) != 0 {
					t.Errorf("content differs: %d bytes, want %d plus padding%s", len(got), len(data), firstDiff(got, data))
				}
			}
			if len(tail) != 0 {
				t.Errorf("unconsumed protocol bytes: %q", tail)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
		o    Options
	}{
		{"rx", nil, Options{}},
		{"rx-crc", []string{"-c"}, Options{}},
		{"1k-rx", nil, Options{Use1K: true}},
		{"1k-rx-crc", []string{"-c"}, Options{Use1K: true}},
	} {
		t.Run("XmodemSend-to-"+tc.name, func(t *testing.T) {
			leakCheck(t)
			dst := t.TempDir()
			r := startRemote(t, dst, "rx", append(tc.args, "x.bin")...)
			n, err := XmodemSend(interopCtx(t), r.ps, bytes.NewReader(data), tc.o)
			tail, exit := r.finish()
			if err != nil || exit != nil {
				t.Fatalf("XmodemSend: %v; rx exit: %v; stderr: %q", err, exit, r.errOutput())
			}
			if n != int64(len(data)) {
				t.Errorf("returned %d, want %d", n, len(data))
			}
			got, err := os.ReadFile(filepath.Join(dst, "x.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, padded(data)) {
				t.Errorf("rx stored %d bytes, want %d%s", len(got), len(padded(data)), firstDiff(got, padded(data)))
			}
			if len(tail) != 0 {
				t.Errorf("unconsumed protocol bytes: %q", tail)
			}
		})
	}
}
