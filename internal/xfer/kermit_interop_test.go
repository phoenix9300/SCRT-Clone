package xfer

// Interoperability tests against real Kermit programs: G-Kermit (gkermit)
// and C-Kermit (kermit), both speaking the protocol on stdin/stdout. The
// tests are skipped when the programs are not installed and on Windows.
//
// Both programs insist on talking to a terminal, so they are started on a
// pseudo terminal (raw mode) whose master side is wired to a PipeStream.
// C-Kermit is driven with "kermit -Y -q -i -s files" and
// "kermit -Y -q -i -r"; -Y skips the initialisation file, -q is quiet and -i
// selects binary mode. G-Kermit takes "gkermit -i -s files" and "gkermit -i -r".

import (
	"bytes"
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

func kermitInteropBatch() []memFile {
	var runs []byte
	for _, c := range []byte{0x00, 0xff, '#', '~', '&', 0x01, 0x7f, 0x80} {
		runs = append(runs, bytes.Repeat([]byte{c}, 500)...)
	}
	return []memFile{
		{FileInfo{Name: "one.dat", Size: 1, ModTime: testTime, Mode: 0o644}, []byte{0x01}},
		{FileInfo{Name: "nasty.bin", Size: -1, ModTime: testTime, Mode: 0o644}, append(nastyBytes(), runs...)},
		{FileInfo{Name: "random.bin", Size: 300000, ModTime: testTime.Add(-time.Hour), Mode: 0o644}, pattern(5, 300000)},
		{FileInfo{Name: "text.txt", Size: 27, ModTime: testTime, Mode: 0o644}, []byte("hello, world\r\nsecond line\r\n")},
	}
}

// kermitProgram describes one installed Kermit implementation.
type kermitProgram struct {
	name string
	bin  string
	send func(files []string) []string // arguments that make it send files
	recv []string                      // arguments that make it receive
}

var kermitPrograms = []kermitProgram{
	{
		name: "gkermit", bin: "gkermit",
		send: func(files []string) []string { return append([]string{"-i", "-q", "-s"}, files...) },
		recv: []string{"-i", "-q", "-r"},
	},
	{
		name: "ckermit", bin: "kermit",
		send: func(files []string) []string { return append([]string{"-Y", "-q", "-i", "-s"}, files...) },
		recv: []string{"-Y", "-q", "-i", "-r"},
	},
}

// kermitProc is a Kermit program running on a pseudo terminal.
type kermitProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File
	ps     *PipeStream
	mu     sync.Mutex
	stderr bytes.Buffer
	waited bool
}

func startKermitProc(t *testing.T, dir string, p kermitProgram, args []string) *kermitProc {
	t.Helper()
	skipUnlessKermit(t, p)
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("cannot open a pseudo terminal: %v", err)
	}
	kp := &kermitProc{t: t, master: master}
	kp.cmd = exec.Command(p.bin, args...)
	kp.cmd.Dir = dir
	kp.cmd.Stderr = lockedWriter{&kp.mu, &kp.stderr}
	ptyChild(kp.cmd, slave)
	if err := kp.cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		t.Fatal(err)
	}
	slave.Close()
	kp.ps = NewPipeStream(master, master)
	t.Cleanup(func() {
		if !kp.waited {
			kp.cmd.Process.Kill()
			kp.cmd.Wait()
		}
		kp.ps.Close()
		kp.master.Close()
	})
	return kp
}

// finish waits for the program to exit and returns what it wrote after the
// transfer.
func (kp *kermitProc) finish() (tail []byte, err error) {
	kp.t.Helper()
	buf := make([]byte, 4096)
	deadline := time.Now().Add(20 * time.Second)
	for {
		n, rerr := kp.ps.ReadTimeout(buf, 100*time.Millisecond)
		tail = append(tail, buf[:n]...)
		if rerr == nil {
			continue
		}
		if !isTimeout(rerr) {
			break // the program exited: the terminal reports EIO or EOF
		}
		if time.Now().After(deadline) {
			kp.cmd.Process.Kill()
			kp.t.Errorf("%s did not exit; stderr: %q", kp.cmd.Path, kp.errOutput())
			break
		}
	}
	err = kp.cmd.Wait()
	kp.waited = true
	return tail, err
}

func (kp *kermitProc) errOutput() string {
	kp.mu.Lock()
	defer kp.mu.Unlock()
	return kp.stderr.String()
}

func skipUnlessKermit(t *testing.T, p kermitProgram) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Kermit interoperability tests need a Unix system")
	}
	if _, err := exec.LookPath(p.bin); err != nil {
		t.Skipf("%s is not installed", p.bin)
	}
}

// kermitOpts varies the transfer parameters of the interop tests.
var kermitInteropCfgs = []struct {
	name string
	cfg  kermitCfg
}{
	{"default", kermitCfg{}},
	{"chk1-short", kermitCfg{chkt: '1', longMax: -1}},
	{"chk2-short", kermitCfg{chkt: '2', longMax: -1}},
	{"chk3-short", kermitCfg{chkt: '3', longMax: -1}},
	{"chk1-long", kermitCfg{chkt: '1'}},
	{"chk2-long", kermitCfg{chkt: '2'}},
	{"qbin", kermitCfg{qbin: '&'}},
	{"no-repeat", kermitCfg{rept: ' '}},
}

// readDirFiles returns the names found in dir.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// compareDir checks the files in dir. C-Kermit may convert names to
// upper case; names are compared case-insensitively unless exact is set.
func compareDir(t *testing.T, dir string, want []memFile, ignore ...string) {
	t.Helper()
	got := map[string]string{}
	for _, n := range dirNames(t, dir) {
		if slices.Contains(ignore, n) {
			continue
		}
		got[strings.ToLower(n)] = n
	}
	if len(got) != len(want) {
		t.Errorf("directory holds %v, want %d files", dirNames(t, dir), len(want))
	}
	for _, w := range want {
		n, ok := got[strings.ToLower(w.info.Name)]
		if !ok {
			t.Errorf("%s: missing", w.info.Name)
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Error(err)
			continue
		}
		if !bytes.Equal(data, w.data) {
			t.Errorf("%s: content differs (got %d bytes, want %d)%s", w.info.Name, len(data), len(w.data), firstDiff(data, w.data))
		}
	}
}

// TestInteropKermitReceive: the remote Kermit program sends, we receive.
func TestInteropKermitReceive(t *testing.T) {
	for _, p := range kermitPrograms {
		for _, c := range kermitInteropCfgs {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				skipUnlessKermit(t, p)
				leakCheck(t)
				src, dst := t.TempDir(), t.TempDir()
				files := kermitInteropBatch()
				paths := diskFiles(t, src, files)
				r := startKermitProc(t, src, p, p.send(paths))
				var left []byte
				err := kermitReceive(interopCtx(t), r.ps, DirSink{Dir: dst}, Options{Timeout: 5 * time.Second, Leftover: func(b []byte) { left = b }}, c.cfg)
				tail, exit := r.finish()
				if err != nil || exit != nil {
					t.Fatalf("receive: %v; %s exit: %v; stderr: %q", err, p.bin, exit, r.errOutput())
				}
				compareDir(t, dst, files)
				if len(bytes.TrimSpace(tail))+len(bytes.TrimSpace(left)) != 0 {
					t.Errorf("unconsumed bytes: %q %q", left, tail)
				}
			})
		}
	}
}

// TestInteropKermitSend: we send, the remote Kermit program receives.
func TestInteropKermitSend(t *testing.T) {
	for _, p := range kermitPrograms {
		for _, c := range kermitInteropCfgs {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				skipUnlessKermit(t, p)
				leakCheck(t)
				src, dst := t.TempDir(), t.TempDir()
				files := kermitInteropBatch()
				var send []SendFile
				for _, path := range diskFiles(t, src, files) {
					f, err := SendFileFromPath(path)
					if err != nil {
						t.Fatal(err)
					}
					send = append(send, f)
				}
				r := startKermitProc(t, dst, p, p.recv)
				err := kermitSend(interopCtx(t), r.ps, send, Options{Timeout: 5 * time.Second}, c.cfg)
				tail, exit := r.finish()
				if err != nil || exit != nil {
					t.Fatalf("send: %v; %s exit: %v; stderr: %q", err, p.bin, exit, r.errOutput())
				}
				compareDir(t, dst, files)
				// G-Kermit prints a newline when it exits.
				if len(bytes.TrimSpace(tail)) != 0 {
					t.Errorf("unconsumed bytes: %q", tail)
				}
			})
		}
	}
}
