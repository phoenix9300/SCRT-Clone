package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

type testClient struct {
	t      *testing.T
	ws     *websocket.Conn
	nextID uint64
	output map[uint32][]byte
	events []map[string]any
}

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(st)
	base, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(s.Close)
	return s, base
}

func dial(t *testing.T, base, token string, hdr http.Header) (*testClient, *http.Response, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(base, "http") + "ws?token=" + token
	ws, resp, err := websocket.DefaultDialer.Dial(u, hdr)
	if err != nil {
		return nil, resp, err
	}
	t.Cleanup(func() { _ = ws.Close() })
	return &testClient{t: t, ws: ws, output: map[uint32][]byte{}}, resp, nil
}

// call performs an RPC and returns its result, collecting output frames and
// events that arrive in the meantime.
func (c *testClient) call(method string, params any) json.RawMessage {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	if err := c.ws.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.t.Fatal(err)
	}
	for {
		m := c.read(10 * time.Second)
		if m == nil {
			c.t.Fatalf("%s: no response", method)
		}
		if rid, ok := m["id"].(float64); ok && uint64(rid) == id {
			if e, ok := m["error"].(string); ok {
				c.t.Fatalf("%s: %s", method, e)
			}
			b, _ := json.Marshal(m["result"])
			return b
		}
	}
}

func (c *testClient) callErr(method string, params any) string {
	c.nextID++
	id := c.nextID
	_ = c.ws.WriteJSON(map[string]any{"id": id, "method": method, "params": params})
	for {
		m := c.read(10 * time.Second)
		if m == nil {
			return "no response"
		}
		if rid, ok := m["id"].(float64); ok && uint64(rid) == id {
			e, _ := m["error"].(string)
			return e
		}
	}
}

// read returns the next JSON message; binary output frames are stored.
func (c *testClient) read(timeout time.Duration) map[string]any {
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(timeout))
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			return nil
		}
		if mt == websocket.BinaryMessage {
			if len(data) >= 5 && data[0] == frameOutput {
				id := binary.BigEndian.Uint32(data[1:5])
				c.output[id] = append(c.output[id], data[5:]...)
			}
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if _, ok := m["event"]; ok {
			c.events = append(c.events, m)
			continue
		}
		return m
	}
}

func (c *testClient) waitOutput(id uint32, want string) {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(c.output[id], []byte(want)) {
			return
		}
		c.read(200 * time.Millisecond)
	}
	c.t.Fatalf("output %q never arrived; got %q", want, c.output[id])
}

func TestAccessControl(t *testing.T) {
	s, base := startServer(t)
	if _, resp, err := dial(t, base, "wrong", nil); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong token must be refused: %v %v", err, resp)
	}
	if _, resp, err := dial(t, base, "", nil); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("missing token must be refused")
	}
	// a page from another site must not be able to use the socket even with the token
	if _, resp, err := dial(t, base, s.Token, http.Header{"Origin": {"https://evil.example"}}); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("cross-origin WebSocket must be refused")
	}
	origin := strings.TrimSuffix(base, "/")
	if _, _, err := dial(t, base, s.Token, http.Header{"Origin": {origin}}); err != nil {
		t.Fatalf("same-origin WebSocket refused: %v", err)
	}

	get := func(path string, hdr map[string]string, method string, body io.Reader) *http.Response {
		req, _ := http.NewRequest(method, base+path, body)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(secret, []byte("top secret"), 0o600)
	if r := get("api/file?path="+secret, nil, "GET", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("file download without token: %d", r.StatusCode)
	}
	if r := get("api/file?path="+secret+"&token="+s.Token, map[string]string{"Origin": "https://evil.example"}, "GET", nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin file download: %d", r.StatusCode)
	}
	r := get("api/file?path="+secret, map[string]string{"X-Hawser-Token": s.Token}, "GET", nil)
	if b, _ := io.ReadAll(r.Body); r.StatusCode != 200 || string(b) != "top secret" {
		t.Fatalf("file download with token: %d %q", r.StatusCode, b)
	}
	if r := get("api/upload?name=x.txt", nil, "POST", strings.NewReader("data")); r.StatusCode != http.StatusForbidden {
		t.Fatalf("upload without token: %d", r.StatusCode)
	}
	r = get("api/upload?name=../../evil.txt", map[string]string{"X-Hawser-Token": s.Token}, "POST", strings.NewReader("data"))
	var up struct{ Path string }
	_ = json.NewDecoder(r.Body).Decode(&up)
	if r.StatusCode != 200 || filepath.Base(up.Path) != "evil.txt" || strings.Contains(up.Path, "..") {
		t.Fatalf("upload must keep only the base name: %d %q", r.StatusCode, up.Path)
	}
	if b, _ := os.ReadFile(up.Path); string(b) != "data" {
		t.Fatalf("uploaded content: %q", b)
	}
	// handing targets to a running instance is for local processes, not web pages
	if r := get("api/open", map[string]string{"X-Hawser-Token": s.Token, "Origin": origin}, "POST", strings.NewReader(`{"startup":[]}`)); r.StatusCode != http.StatusForbidden {
		t.Fatalf("api/open from a browser origin: %d", r.StatusCode)
	}
	// without --allow-remote only loopback peers are served, even with the token
	far := &http.Request{RemoteAddr: "192.0.2.7:4000", Header: http.Header{"X-Hawser-Token": {s.Token}}, URL: mustURL("/ws")}
	if s.checkToken(far) {
		t.Fatal("non-loopback peer accepted without AllowRemote")
	}
	s.AllowRemote = true
	if !s.checkToken(far) {
		t.Fatal("AllowRemote should admit a remote peer that has the token")
	}
	s.AllowRemote = false
	// the UI page itself is public but never cached and carries a CSP
	r = get("", nil, "GET", nil)
	if r.StatusCode != 200 || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("index: %d %v", r.StatusCode, r.Header)
	}
}

func TestRPCSessionsAndTerminal(t *testing.T) {
	s, base := startServer(t)
	c, _, err := dial(t, base, s.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]any
	_ = json.Unmarshal(c.call("app.info", nil), &info)
	if info["version"] != Version || info["configDir"] != s.Store.Dir {
		t.Fatalf("app.info: %v", info)
	}
	if e := c.callErr("no.such.method", nil); !strings.Contains(e, "unknown method") {
		t.Fatalf("unknown method: %q", e)
	}

	pw := "hunter2"
	c.call("sessions.save", map[string]any{"path": "Lab/sw1", "overrides": map[string]any{"host": "10.0.0.1", "terminal": map[string]any{"emulation": "vt220"}},
		"secrets": map[string]any{"password": pw}})
	if e := c.callErr("sessions.save", map[string]any{"path": "Lab/sw1", "overrides": map[string]any{}, "mustBeNew": true}); !strings.Contains(e, "already exists") {
		t.Fatalf("mustBeNew: %q", e)
	}
	if e := c.callErr("sessions.save", map[string]any{"path": "../escape", "overrides": map[string]any{}}); e == "" {
		t.Fatal("path traversal accepted")
	}
	var got struct {
		Overrides map[string]any
		Defaults  map[string]any
		Secrets   map[string]bool
	}
	_ = json.Unmarshal(c.call("sessions.get", map[string]any{"path": "Lab/sw1"}), &got)
	if got.Overrides["host"] != "10.0.0.1" || !got.Secrets["password"] || got.Defaults["protocol"] != "ssh2" {
		t.Fatalf("sessions.get: %+v", got)
	}
	if strings.Contains(string(c.call("sessions.get", map[string]any{"path": "Lab/sw1"})), pw) {
		t.Fatal("a stored password must never be sent to the UI")
	}
	var tree struct{ Children []struct{ Name string } }
	_ = json.Unmarshal(c.call("sessions.tree", nil), &tree)
	if len(tree.Children) != 1 || tree.Children[0].Name != "Lab" {
		t.Fatalf("tree: %+v", tree)
	}
	c.call("sessions.rename", map[string]any{"from": "Lab/sw1", "to": "Lab/sw2"})
	if v, ok, _ := s.Store.Vault.Get(store.SecretKey("Lab/sw2", "password")); !ok || v != pw {
		t.Fatal("secret did not follow the rename")
	}

	// shared credentials: the password goes in but never comes back out
	c.call("credentials.save", map[string]any{"name": "ops", "username": "ops", "password": "pw1"})
	list := string(c.call("credentials.list", nil))
	if !strings.Contains(list, `"ops"`) || strings.Contains(list, "pw1") {
		t.Fatalf("credentials.list: %s", list)
	}
	c.call("credentials.save", map[string]any{"name": "ops2", "rename": "ops", "username": "ops"})
	if cr, ok, _ := s.Store.Vault.Credential("ops2"); !ok || cr.Password != "pw1" {
		t.Fatalf("rename lost the password: %+v", cr)
	}

	// a terminal session over the socket
	var ci struct {
		ID    uint32
		State string
	}
	_ = json.Unmarshal(c.call("conn.open", map[string]any{"overrides": map[string]any{"protocol": "local", "local": map[string]any{"shell": "/bin/sh"}}, "cols": 80, "rows": 24}), &ci)
	if ci.ID == 0 {
		t.Fatal("conn.open returned no id")
	}
	frame := append([]byte{frameInput, 0, 0, 0, 0}, []byte("echo rpc-$((20+1))\n")...)
	binary.BigEndian.PutUint32(frame[1:], ci.ID)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(string(c.call("conn.info", map[string]any{"id": ci.ID})), `"state":"connected"`) {
		if time.Now().After(deadline) {
			t.Fatal("never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := c.ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	c.waitOutput(ci.ID, "rpc-21")
	// a second window attaches and receives the replay
	c2, _, err := dial(t, base, s.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2.call("conn.attach", map[string]any{"id": ci.ID})
	c2.waitOutput(ci.ID, "rpc-21")
	c2.call("conn.close", map[string]any{"id": ci.ID})
	if !strings.Contains(string(c2.call("conn.list", nil)), "[]") {
		t.Fatal("connection still listed after close")
	}
}

func mustURL(p string) *url.URL {
	u, err := url.Parse(p)
	if err != nil {
		panic(err)
	}
	return u
}
