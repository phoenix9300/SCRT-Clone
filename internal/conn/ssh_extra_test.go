package conn

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

func sshOpts(ov map[string]any) map[string]any { return ov["ssh"].(map[string]any) }

// startAgent runs a private ssh-agent holding the given key files and
// returns its socket path.
func startAgent(t *testing.T, keyFiles ...string) string {
	t.Helper()
	for _, tool := range []string{"ssh-agent", "ssh-add"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir, err := os.MkdirTemp("", "hwag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	cmd := exec.Command("ssh-agent", "-D", "-a", sock)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start ssh-agent: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("ssh-agent did not create its socket")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, k := range keyFiles {
		add := exec.Command("ssh-add", k)
		add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
		if out, err := add.CombinedOutput(); err != nil {
			t.Fatalf("ssh-add: %v %s", err, out)
		}
	}
	return sock
}

func fingerprintOf(t *testing.T, pubFile string) string {
	t.Helper()
	out, err := exec.Command("ssh-keygen", "-lf", pubFile).Output()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	if len(f) < 2 {
		t.Fatalf("ssh-keygen -l: %q", out)
	}
	return f[1]
}

func TestSSHRemoteForward(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll
	echoPort := startEcho(t)
	c := openConn(t, m, sink, f.overrides(), 80, 24)
	waitState(t, c, StateConnected)
	st, err := c.AddForward(store.PortForward{Name: "rev", Type: "remote", BindAddr: "127.0.0.1", BindPort: 0, DestHost: "127.0.0.1", DestPort: echoPort, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Address == "" || strings.HasSuffix(st.Address, ":0") {
		t.Fatalf("remote listener address not reported: %q", st.Address)
	}
	conn, err := net.DialTimeout("tcp", st.Address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	msg := make([]byte, 8192)
	_, _ = rand.Read(msg)
	go func() { _, _ = conn.Write(msg) }()
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("remote forward round trip failed: %v", err)
	}
	if fw := c.Forwards(); len(fw) != 1 || fw[0].Total != 1 || !fw[0].Listening {
		t.Fatalf("forward status: %+v", fw)
	}
	if !c.RemoveForward("rev") {
		t.Fatal("RemoveForward failed")
	}
	time.Sleep(200 * time.Millisecond)
	if c2, err := net.DialTimeout("tcp", st.Address, time.Second); err == nil {
		// the server may still be tearing the listener down; it must not reach the echo service any more
		_ = c2.SetDeadline(time.Now().Add(time.Second))
		_, _ = c2.Write([]byte("x"))
		if n, _ := c2.Read(make([]byte, 1)); n != 0 {
			t.Fatal("removed remote forward still relays")
		}
		_ = c2.Close()
	}
	// forwards configured in the session start with the connection
	ov := f.overrides()
	sshOpts(ov)["forwards"] = []any{map[string]any{"name": "auto", "type": "remote", "bindAddr": "127.0.0.1", "bindPort": 0, "destHost": "127.0.0.1", "destPort": echoPort, "enabled": true}}
	c3 := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c3, StateConnected)
	if fw := c3.Forwards(); len(fw) != 1 || !fw[0].Listening || fw[0].Error != "" {
		t.Fatalf("auto-started forward: %+v", fw)
	}
	// an unusable remote bind is reported, not fatal
	if _, err := c3.AddForward(store.PortForward{Name: "bad", Type: "bogus"}); err == nil {
		t.Fatal("unknown forward type accepted")
	}
}

func TestSSHAgentAuthAndForwarding(t *testing.T) {
	f := startSSHD(t)
	sock := startAgent(t, f.clientKey)
	t.Setenv("SSH_AUTH_SOCK", sock)
	m, sink := newTestManager(t)
	sink.answers = acceptAll
	ov := f.overrides()
	sshOpts(ov)["identityFiles"] = []any{"/nonexistent/hawser-key"}
	sshOpts(ov)["useAgent"] = true
	c := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c, StateConnected)
	if am := c.Info(false).Details["authMethod"]; am != "publickey (agent)" {
		t.Fatalf("authMethod = %q", am)
	}
	// agent not used when disabled: the missing identity file means failure
	sshOpts(ov)["useAgent"] = false
	c0 := openConn(t, m, sink, ov, 80, 24)
	if msg := failedMessage(t, c0); !strings.Contains(msg, "authentication failed") {
		t.Fatalf("message: %q", msg)
	}

	// agent forwarding: the remote side can list our key
	sshOpts(ov)["useAgent"] = true
	sshOpts(ov)["agentForwarding"] = true
	c2 := openConn(t, m, sink, ov, 120, 24)
	waitState(t, c2, StateConnected)
	c2.Input([]byte("ssh-add -l; echo add-done\n"))
	expect(t, c2, 0, fingerprintOf(t, f.clientKey+".pub"))
	expect(t, c2, 0, "add-done")
}

func TestSSHBuiltinAgent(t *testing.T) {
	f := startSSHD(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	m, sink := newTestManager(t)
	sink.answers = acceptAll
	ov := f.overrides()
	sshOpts(ov)["identityFiles"] = []any{"/nonexistent/hawser-key"}
	sshOpts(ov)["useAgent"] = true
	sshOpts(ov)["auth"] = []any{"publickey"}
	c := openConn(t, m, sink, ov, 80, 24)
	if msg := failedMessage(t, c); !strings.Contains(msg, "authentication failed") {
		t.Fatalf("empty keyring should fail authentication, got %q", msg)
	}
	data, err := os.ReadFile(f.clientKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Keyring.Add(agent.AddedKey{PrivateKey: raw, Comment: "builtin"}); err != nil {
		t.Fatal(err)
	}
	c2 := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c2, StateConnected)
	if am := c2.Info(false).Details["authMethod"]; am != "publickey (Hawser agent)" {
		t.Fatalf("authMethod = %q", am)
	}
	c2.Input([]byte("echo builtin-$((5*5))\n"))
	expect(t, c2, 0, "builtin-25")
}

func TestSSHEncryptedIdentityPassphrase(t *testing.T) {
	f := startSSHD(t)
	enc := filepath.Join(t.TempDir(), "id_enc")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "correct horse", "-f", enc).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(enc + ".pub")
	ak, _ := os.OpenFile(filepath.Join(f.dir, "authorized_keys"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = ak.Write(pub)
	_ = ak.Close()

	m, sink := newTestManager(t)
	var passPrompts int32
	var save atomic.Bool
	sink.answers = func(req PromptRequest) PromptResponse {
		switch req.Kind {
		case "hostkey":
			return PromptResponse{OK: true, Choice: "save"}
		case "passphrase":
			n := atomic.AddInt32(&passPrompts, 1)
			if !req.AllowSave || !req.Fields[0].Secret || !strings.Contains(req.Message, enc) {
				t.Errorf("passphrase prompt wrong: %+v", req)
			}
			if n == 1 {
				return PromptResponse{OK: true, Values: []string{"wrong"}}
			}
			if n == 2 && !strings.Contains(req.Message, "Incorrect") {
				t.Errorf("retry prompt should say the passphrase was wrong: %q", req.Message)
			}
			return PromptResponse{OK: true, Values: []string{"correct horse"}, Save: save.Load()}
		}
		return PromptResponse{}
	}
	ov := f.overrides()
	sshOpts(ov)["identityFiles"] = []any{enc}
	sshOpts(ov)["auth"] = []any{"publickey"}
	if err := m.Store.Save("enc", ov); err != nil {
		t.Fatal(err)
	}
	connect := func() *Conn {
		c, err := m.Open(OpenRequest{SessionPath: "enc", Cols: 80, Rows: 24}, sink)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// wrong once, then right; without Save nothing is remembered
	c := connect()
	waitState(t, c, StateConnected)
	if n := atomic.LoadInt32(&passPrompts); n != 2 {
		t.Fatalf("passphrase prompts = %d, want 2", n)
	}
	if _, ok, _ := m.Store.Vault.Get("passphrase:" + enc); ok {
		t.Fatal("passphrase stored although Save was not requested")
	}
	c.Input([]byte("echo enc-ok-$((3+3))\n"))
	expect(t, c, 0, "enc-ok-6")
	// asked again next time; this time save it
	atomic.StoreInt32(&passPrompts, 10) // no more "wrong" answers
	save.Store(true)
	c2 := connect()
	waitState(t, c2, StateConnected)
	if n := atomic.LoadInt32(&passPrompts); n != 11 {
		t.Fatalf("second connect should ask exactly once, prompts = %d", n-10)
	}
	if v, ok, _ := m.Store.Vault.Get("passphrase:" + enc); !ok || v != "correct horse" {
		t.Fatalf("passphrase not saved: %q %v", v, ok)
	}
	// saved passphrase is used silently
	c3 := connect()
	waitState(t, c3, StateConnected)
	if n := atomic.LoadInt32(&passPrompts); n != 11 {
		t.Fatalf("saved passphrase was not used (%d extra prompts)", n-11)
	}
	// forgotten passphrase + a dismissed prompt fails the login without hanging
	m.Store.Vault.Delete("passphrase:" + enc)
	sink.answers = func(req PromptRequest) PromptResponse {
		if req.Kind == "hostkey" {
			return PromptResponse{OK: true, Choice: "save"}
		}
		return PromptResponse{OK: false}
	}
	c4 := connect()
	if msg := failedMessage(t, c4); !strings.Contains(strings.ToLower(msg), "cancel") && !strings.Contains(strings.ToLower(msg), "publickey") {
		t.Fatalf("cancelled passphrase message: %q", msg)
	}
	// three wrong answers in a row are given up on
	var tries int32
	sink.answers = func(req PromptRequest) PromptResponse {
		if req.Kind == "hostkey" {
			return PromptResponse{OK: true, Choice: "save"}
		}
		atomic.AddInt32(&tries, 1)
		return PromptResponse{OK: true, Values: []string{"nope"}}
	}
	c5 := connect()
	failedMessage(t, c5)
	if n := atomic.LoadInt32(&tries); n != 3 {
		t.Fatalf("expected 3 passphrase attempts, got %d", n)
	}
}

func TestSSHHostKeyAcceptNew(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	var prompts []PromptRequest
	sink.answers = func(req PromptRequest) PromptResponse {
		prompts = append(prompts, req)
		return PromptResponse{OK: false, Choice: "reject"}
	}
	ov := f.overrides()
	sshOpts(ov)["hostKeyPolicy"] = "accept-new"
	c := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c, StateConnected)
	if len(prompts) != 0 {
		t.Fatalf("accept-new asked the user: %+v", prompts)
	}
	if !strings.Contains(sink.text(c.ID), "Permanently added host key") {
		t.Fatalf("no notice about the new key: %q", sink.text(c.ID))
	}
	kh, _ := os.ReadFile(m.Store.KnownHostsPath())
	if !strings.Contains(string(kh), "[127.0.0.1]:") {
		t.Fatalf("host key not recorded: %q", kh)
	}
	// known now: silent
	c2 := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c2, StateConnected)
	if len(prompts) != 0 {
		t.Fatalf("known host prompted: %+v", prompts)
	}
	// a changed key is never accepted silently
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(other + ".pub")
	fields := strings.Fields(string(pub))
	line := "[127.0.0.1]:" + itoa(f.port) + " " + fields[0] + " " + fields[1] + "\n"
	if err := os.WriteFile(m.Store.KnownHostsPath(), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	c3 := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c3, StateDisconnected)
	if len(prompts) != 1 || prompts[0].Kind != "hostkey" || !prompts[0].HostKey.Changed || prompts[0].HostKey.OldSHA256 == "" {
		t.Fatalf("changed key must prompt: %+v", prompts)
	}
	if !c3.Info(false).Failed {
		t.Fatal("rejected host key should be a failure")
	}
	if kh2, _ := os.ReadFile(m.Store.KnownHostsPath()); string(kh2) != line {
		t.Fatalf("known_hosts was modified by a rejected key: %q", kh2)
	}
}

func TestSSHRemoteCommandAndEnv(t *testing.T) {
	f := startSSHD(t, "AcceptEnv HAWSER_*")
	m, sink := newTestManager(t)
	sink.answers = acceptAll

	ov := f.overrides()
	sshOpts(ov)["remoteCommand"] = "echo cmd-out-$((6*7)); exit 7"
	c := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c, StateDisconnected)
	inf := c.Info(false)
	if inf.Failed || inf.Message != "Connection closed (exit status 7)" {
		t.Fatalf("exit status not reported: failed=%v %q", inf.Failed, inf.Message)
	}
	if !strings.Contains(sink.text(c.ID), "cmd-out-42") {
		t.Fatalf("command output missing: %q", sink.text(c.ID))
	}
	sshOpts(ov)["remoteCommand"] = "echo fine"
	c2 := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c2, StateDisconnected)
	if inf := c2.Info(false); inf.Failed || inf.Message != "Connection closed" || !strings.Contains(sink.text(c2.ID), "fine") {
		t.Fatalf("clean command exit: failed=%v %q", inf.Failed, inf.Message)
	}

	// environment variables: accepted ones arrive, others are silently dropped
	ov = f.overrides()
	sshOpts(ov)["env"] = map[string]any{"HAWSER_GREETING": "bar-baz", "NOT_ACCEPTED_VAR": "zzz"}
	c3 := openConn(t, m, sink, ov, 200, 24)
	waitState(t, c3, StateConnected)
	c3.Input([]byte("echo \"val=[$HAWSER_GREETING] other=[$NOT_ACCEPTED_VAR]\"\n"))
	expect(t, c3, 0, "val=[bar-baz] other=[]")
}

func TestSSHKeepAliveIdleSession(t *testing.T) {
	f := startSSHD(t)
	m, sink := newTestManager(t)
	sink.answers = acceptAll
	ov := f.overrides()
	sshOpts(ov)["keepAliveInterval"] = 1
	c := openConn(t, m, sink, ov, 80, 24)
	waitState(t, c, StateConnected)
	time.Sleep(2500 * time.Millisecond) // two or more keep-alive rounds
	if c.State() != StateConnected {
		t.Fatalf("idle session dropped: %s", c.Info(false).Message)
	}
	c.Input([]byte("echo still-here-$((9*9))\n"))
	expect(t, c, 0, "still-here-81")
	c.mu.Lock()
	tr := c.transport
	c.mu.Unlock()
	if err := tr.(KeepAliver).KeepAlive(); err != nil {
		t.Fatalf("explicit keep-alive: %v", err)
	}
	if c.SSH() == nil || c.SSH().stopKeep == nil {
		t.Fatal("keep-alive loop was not started")
	}
	c.Input([]byte("exit\n"))
	waitState(t, c, StateDisconnected)
}
