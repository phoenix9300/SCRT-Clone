package conn

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSSHX11ForwardingEndToEnd runs a real X client on the server side of an
// SSH session and lets it reach a real X server (Xvfb) through the tunnel,
// with cookie authentication enabled on that server.
func TestSSHX11ForwardingEndToEnd(t *testing.T) {
	for _, tool := range []string{"Xvfb", "xauth", "xdpyinfo"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	// a private display with MIT-MAGIC-COOKIE authentication
	display := ""
	xauthFile := filepath.Join(dir, "Xauthority")
	var xvfb *exec.Cmd
	for n := 97; n < 110; n++ {
		if _, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n)); err == nil {
			continue
		}
		display = fmt.Sprintf(":%d", n)
		cookie := randomHex(16)
		if out, err := exec.Command("xauth", "-f", xauthFile, "add", display, "MIT-MAGIC-COOKIE-1", cookie).CombinedOutput(); err != nil {
			t.Skipf("xauth: %v %s", err, out)
		}
		xvfb = exec.Command("Xvfb", display, "-auth", xauthFile, "-screen", "0", "640x480x24", "-nolisten", "tcp")
		if err := xvfb.Start(); err != nil {
			t.Skipf("Xvfb: %v", err)
		}
		break
	}
	if xvfb == nil {
		t.Skip("no free display number")
	}
	t.Cleanup(func() { _ = xvfb.Process.Kill(); _, _ = xvfb.Process.Wait() })
	sock := "/tmp/.X11-unix/X" + display[1:]
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// our side reads the real cookie through xauth
	t.Setenv("XAUTHORITY", xauthFile)
	// without the cookie the server refuses clients: proves authentication is on
	bare := exec.Command("xdpyinfo", "-display", display)
	bare.Env = append(os.Environ(), "XAUTHORITY=/nonexistent")
	if err := bare.Run(); err == nil {
		t.Skip("X server accepts unauthenticated clients; the test would prove nothing")
	}

	f := startSSHD(t, "X11Forwarding yes", "X11UseLocalhost yes", "XAuthLocation "+mustLook(t, "xauth"))
	m, sink := newTestManager(t)
	sink.answers = acceptAll
	ov := f.overrides()
	ssh := ov["ssh"].(map[string]any)
	ssh["x11Forwarding"] = true
	ssh["x11Display"] = display
	c, err := m.Open(OpenRequest{Overrides: ov, Cols: 100, Rows: 30}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	c.Input([]byte("echo display=$DISPLAY; xdpyinfo | grep -E 'dimensions|vendor string' ; echo x11-fin$((1+1))\n"))
	expect(t, c, 0, "x11-fin2")
	out := c.TextSlice(0, c.TextEnd())
	if !strings.Contains(out, "display=localhost:") {
		t.Fatalf("the server did not set up a forwarded display:\n%s", out)
	}
	if !strings.Contains(out, "640x480 pixels") {
		t.Fatalf("the X client did not reach our X server through the tunnel:\n%s", out)
	}
}

func mustLook(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skip(err)
	}
	return p
}
