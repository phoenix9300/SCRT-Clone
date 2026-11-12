package xfer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A download must never become executable (or setuid) because the sender
// said so: in-band transfers can be started by the remote host.
func TestDirSinkDropsExecuteBits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits")
	}
	dir := t.TempDir()
	w, _, err := DirSink{Dir: dir}.Create(FileInfo{Name: "tool.sh", Size: 3, Mode: 0o4755})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "tool.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&0o7111 != 0 {
		t.Fatalf("download kept special/execute bits: %v", st.Mode())
	}
}
