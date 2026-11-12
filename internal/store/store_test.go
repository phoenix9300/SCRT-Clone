package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionsRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save("Lab/Routers/core1", map[string]any{"host": "10.0.0.1", "username": "admin", "terminal": map[string]any{"emulation": "vt100"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("web", map[string]any{"host": "example.org", "protocol": "telnet", "port": 0}); err != nil {
		t.Fatal(err)
	}
	o, err := s.Session("Lab/Routers/core1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Host != "10.0.0.1" || o.Port != 22 || o.Terminal.Emulation != "vt100" || o.Terminal.Scrollback != 5000 {
		t.Fatalf("unexpected effective options: %+v", o)
	}
	// default session changes flow into existing sessions
	if err := s.SaveDefault(map[string]any{"terminal": map[string]any{"scrollback": 9999, "emulation": "linux"}}); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Session("Lab/Routers/core1")
	if o.Terminal.Scrollback != 9999 || o.Terminal.Emulation != "vt100" {
		t.Fatalf("default overlay wrong: %+v", o.Terminal)
	}
	o, _ = s.Session("web")
	if o.Port != 23 || o.Terminal.Emulation != "linux" {
		t.Fatalf("telnet defaults wrong: port=%d emu=%s", o.Port, o.Terminal.Emulation)
	}
	tree, err := s.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Children) != 2 || !tree.Children[0].Folder || tree.Children[1].Summary.Host != "example.org" {
		t.Fatalf("tree: %+v", tree.Children)
	}
	// rename with secrets
	if err := s.Vault.Set(SecretKey("web", "password"), "hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("web", "Lab/web2", false); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Vault.Get(SecretKey("Lab/web2", "password")); !ok || v != "hunter2" {
		t.Fatalf("secret did not follow rename: %q %v", v, ok)
	}
	if err := s.Rename("Lab", "Site A", true); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Vault.Get(SecretKey("Site A/web2", "password")); !ok || v != "hunter2" {
		t.Fatalf("secret did not follow folder rename: %q %v", v, ok)
	}
	if err := s.Delete("Site A/web2"); err != nil {
		t.Fatal(err)
	}
	if s.Vault.Has(SecretKey("Site A/web2", "password")) {
		t.Fatal("secret survived delete")
	}
	for _, bad := range []string{"", "../x", "a/../../b", "a/.hidden", "con:1"} {
		if _, err := CleanPath(bad); err == nil {
			t.Errorf("CleanPath(%q) accepted", bad)
		}
	}
}

func TestVaultPassphrase(t *testing.T) {
	dir := t.TempDir()
	v, err := OpenVault(filepath.Join(dir, "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set("a", "secret-a"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "vault.json"))
	if string(raw) == "" || contains(raw, "secret-a") {
		t.Fatal("secret stored in clear text")
	}
	if err := v.SetPassphrase("master"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vault.key")); err == nil {
		t.Fatal("machine key should be removed once a passphrase is set")
	}
	// reopen: locked until unlocked
	v2, err := OpenVault(filepath.Join(dir, "vault.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !v2.Locked() {
		t.Fatal("vault should be locked")
	}
	if _, _, err := v2.Get("a"); err != ErrLocked {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	if err := v2.Unlock("wrong"); err != ErrBadPassphrase {
		t.Fatalf("want ErrBadPassphrase, got %v", err)
	}
	if err := v2.Unlock("master"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := v2.Get("a"); got != "secret-a" {
		t.Fatalf("got %q", got)
	}
	if err := v2.SetPassphrase(""); err != nil {
		t.Fatal(err)
	}
	v3, _ := OpenVault(filepath.Join(dir, "vault.json"))
	if got, _, _ := v3.Get("a"); got != "secret-a" || v3.Locked() {
		t.Fatalf("after removing passphrase: %q locked=%v", got, v3.Locked())
	}
}

func contains(b []byte, s string) bool {
	return len(s) > 0 && len(b) >= len(s) && (string(b) == s || indexOf(string(b), s) >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestProtocolDefaultPortAndRemovedKeys(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// a session that only switches the protocol gets that protocol's port,
	// not the default session's SSH port
	o, err := s.Resolve(map[string]any{"protocol": "telnet", "host": "pbx"})
	if err != nil || o.Port != 23 {
		t.Fatalf("telnet without port: %d %v", o.Port, err)
	}
	if o, _ = s.Resolve(map[string]any{"protocol": "telnet", "host": "pbx", "port": 2323}); o.Port != 2323 {
		t.Fatalf("explicit port lost: %d", o.Port)
	}
	// a customised default port applies to sessions of the default protocol only
	_ = s.SaveDefault(map[string]any{"port": 2222, "ssh": map[string]any{"env": map[string]any{"A": "1", "B": "2"}}})
	if o, _ = s.Resolve(map[string]any{"host": "h"}); o.Port != 2222 {
		t.Fatalf("default port not inherited: %d", o.Port)
	}
	if o, _ = s.Resolve(map[string]any{"host": "h", "protocol": "rlogin"}); o.Port != 513 {
		t.Fatalf("rlogin port: %d", o.Port)
	}
	// null removes an inherited key of a free-form map
	o, _ = s.Resolve(map[string]any{"host": "h", "ssh": map[string]any{"env": map[string]any{"A": nil, "C": "3"}}})
	if len(o.SSH.Env) != 2 || o.SSH.Env["B"] != "2" || o.SSH.Env["C"] != "3" {
		t.Fatalf("env after removal: %v", o.SSH.Env)
	}
	_ = s.Save("t", map[string]any{"protocol": "telnet", "host": "pbx"})
	tree, _ := s.Tree()
	if tree.Children[0].Summary.Port != 23 {
		t.Fatalf("tree summary port: %d", tree.Children[0].Summary.Port)
	}
}
