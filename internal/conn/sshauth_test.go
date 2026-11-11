package conn

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// fakeSSHServer is an in-process SSH server with password and
// keyboard-interactive authentication and a trivial echo "shell".
func fakeSSHServer(t *testing.T, password string) int {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("wrong password")
		},
		KeyboardInteractiveCallback: func(c ssh.ConnMetadata, ch ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			ans, err := ch("Two-step login", "Answer both questions", []string{"Password: ", "Token: "}, []bool{false, true})
			if err != nil {
				return nil, err
			}
			if len(ans) == 2 && ans[0] == password && ans[1] == "123456" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
		BannerCallback: func(ssh.ConnMetadata) string { return "Authorised use only\n" },
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					if nch.ChannelType() != "session" {
						_ = nch.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					ch, creqs, err := nch.Accept()
					if err != nil {
						return
					}
					go func() {
						for r := range creqs {
							_ = r.Reply(r.Type == "pty-req" || r.Type == "shell" || r.Type == "window-change", nil)
						}
					}()
					go func() {
						defer ch.Close()
						_, _ = ch.Write([]byte("welcome " + sc.User() + "\r\n> "))
						buf := make([]byte, 1024)
						for {
							n, err := ch.Read(buf)
							if err != nil {
								return
							}
							_, _ = ch.Write([]byte(strings.ReplaceAll(string(buf[:n]), "\r", "\r\n> ")))
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestSSHPasswordAndKeyboardInteractive(t *testing.T) {
	port := fakeSSHServer(t, "s3cret")
	m, sink := newTestManager(t)
	var kinds []string
	pwPrompts := 0
	sink.answers = func(req PromptRequest) PromptResponse {
		kinds = append(kinds, req.Kind)
		switch req.Kind {
		case "hostkey":
			return PromptResponse{OK: true, Choice: "save"}
		case "password":
			pwPrompts++
			if pwPrompts == 1 {
				return PromptResponse{OK: true, Values: []string{"wrong"}}
			}
			return PromptResponse{OK: true, Values: []string{"s3cret"}, Save: true}
		case "keyboard-interactive":
			if len(req.Fields) != 2 || !req.Fields[0].Secret || req.Fields[1].Secret || req.Title != "Two-step login" {
				t.Errorf("keyboard-interactive prompt wrong: %+v", req)
			}
			return PromptResponse{OK: true, Values: []string{"s3cret", "123456"}}
		}
		return PromptResponse{}
	}
	base := func(auth ...any) map[string]any {
		return map[string]any{"protocol": "ssh2", "host": "127.0.0.1", "port": port, "username": "alice",
			"ssh": map[string]any{"auth": auth, "useAgent": false, "useOpenSSHKnownHosts": false, "identityFiles": []any{"/nonexistent"}}}
	}
	if err := m.Store.Save("pw", base("password")); err != nil {
		t.Fatal(err)
	}
	c, err := m.Open(OpenRequest{SessionPath: "pw", Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	expect(t, c, 0, "welcome alice")
	if pwPrompts != 2 {
		t.Fatalf("expected a retry after the wrong password, got %d prompts (%v)", pwPrompts, kinds)
	}
	if !strings.Contains(sink.text(c.ID), "Authorised use only") {
		t.Fatalf("banner not shown: %q", sink.text(c.ID))
	}
	if v, ok, _ := m.Store.Vault.Get(store.SecretKey("pw", "password")); !ok || v != "s3cret" {
		t.Fatal("password was not saved to the vault after a successful login")
	}
	// the saved password is used silently the next time
	pwPrompts = 0
	c2, _ := m.Open(OpenRequest{SessionPath: "pw", Cols: 80, Rows: 24}, sink)
	waitState(t, c2, StateConnected)
	if pwPrompts != 0 {
		t.Fatalf("saved password was not used (%d prompts)", pwPrompts)
	}
	// a one-off password from the command line is never stored
	c3, _ := m.Open(OpenRequest{Overrides: base("password"), Password: "s3cret", Cols: 80, Rows: 24}, sink)
	waitState(t, c3, StateConnected)
	if pwPrompts != 0 {
		t.Fatal("transient password was not used")
	}
	// keyboard-interactive with two questions goes to the user
	kinds = nil
	c4, _ := m.Open(OpenRequest{Overrides: base("keyboard-interactive"), Cols: 80, Rows: 24}, sink)
	waitState(t, c4, StateConnected)
	if len(kinds) != 1 || kinds[0] != "keyboard-interactive" {
		t.Fatalf("prompts: %v", kinds)
	}
	// cancelling the prompt fails the connection without retry loops
	sink.answers = func(req PromptRequest) PromptResponse { return PromptResponse{OK: false} }
	c5, _ := m.Open(OpenRequest{Overrides: base("password"), Cols: 80, Rows: 24}, sink)
	waitState(t, c5, StateDisconnected)
	if !c5.Info(false).Failed {
		t.Fatal("cancelled login should be reported as failed")
	}
}
