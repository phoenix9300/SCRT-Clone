// Package server exposes the Hawser engine to the browser UI over HTTP and
// a WebSocket carrying JSON-RPC style messages plus binary terminal data.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
	"github.com/phoenix9300/SCRT-Clone/internal/webui"
)

// Version is the application version reported to the UI.
var Version = "0.1.0"

// Server is the application backend.
type Server struct {
	Store *store.Store
	Mgr   *conn.Manager
	// Token authenticates the UI. It is generated at start-up.
	Token string
	// AllowRemote accepts clients from other machines (the token is still
	// required). Without it only loopback peers are served, whatever address
	// the listener is bound to.
	AllowRemote bool
	// TLSCert and TLSKey, when set, serve the UI over HTTPS.
	TLSCert, TLSKey string
	// UIDir, when set, serves the UI from a directory instead of the
	// embedded bundle (development).
	UIDir string
	// Startup lists actions the UI should perform once it is ready
	// (sessions to open, scripts to run) – from the command line.
	Startup   []map[string]any
	startupMu sync.Mutex

	handlers map[string]handler
	clients  sync.Map // *client -> struct{}
	ext      extensions
	ln       net.Listener
	quit     chan struct{}
	quitOnce sync.Once
}

type handler func(c *client, params json.RawMessage) (any, error)

// New creates a server around an opened store.
func New(st *store.Store) *Server {
	s := &Server{Store: st, Mgr: conn.NewManager(st), quit: make(chan struct{})}
	tok := make([]byte, 24)
	_, _ = rand.Read(tok)
	s.Token = base64.RawURLEncoding.EncodeToString(tok)
	s.handlers = map[string]handler{}
	s.registerCore()
	s.registerExtensions()
	s.registerScripts()
	return s
}

// Quit asks the process to exit (application menu → Exit).
func (s *Server) Quit() { s.quitOnce.Do(func() { close(s.quit) }) }

// Done is closed when the UI requested shutdown.
func (s *Server) Done() <-chan struct{} { return s.quit }

// Listen binds the HTTP listener and returns the base URL.
func (s *Server) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.ln = ln
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	scheme := "http"
	if s.TLSCert != "" {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/", nil
}

// Serve runs the HTTP server until the listener is closed.
func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/file", s.handleFile)
	mux.HandleFunc("/api/open", s.handleOpen)
	ui, built := webui.FS()
	if s.UIDir != "" {
		ui, built = os.DirFS(s.UIDir), true
	}
	fileServer := http.FileServer(http.FS(ui))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !built {
			b, _ := fs.ReadFile(ui, "placeholder.html")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "index.html" {
			// the page itself must never be cached: it references hashed assets
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy",
				"default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'")
		} else if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	if s.TLSCert != "" {
		return srv.ServeTLS(s.ln, s.TLSCert, s.TLSKey)
	}
	return srv.Serve(s.ln)
}

// Close stops listening and disconnects everything.
func (s *Server) Close() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.ext.close()
	s.Mgr.CloseAll()
}

func (s *Server) checkToken(r *http.Request) bool {
	if !s.AllowRemote {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	tok := r.URL.Query().Get("token")
	if tok == "" {
		tok = r.Header.Get("X-Hawser-Token")
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1
}

// checkOrigin rejects cross-site WebSocket / API use: the Origin host must
// be the host the request was sent to.
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client; the token still applies
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true }, // verified by Server.checkOrigin
}

// ---------------------------------------------------------------- client

const (
	frameOutput = 1
	frameInput  = 2
)

type outMsg struct {
	binary bool
	data   []byte
}

// client is one UI window. It implements conn.Sink.
type client struct {
	srv    *Server
	ws     *websocket.Conn
	out    chan outMsg
	closed chan struct{}
	once   sync.Once

	reqMu   sync.Mutex
	pending map[uint64]chan rpcReply
	nextReq atomic.Uint64
}

type rpcReply struct {
	result json.RawMessage
	err    string
}

type inMsg struct {
	ID     *uint64         `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	RID    *uint64         `json:"rid"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(r) || !s.checkToken(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(64 << 20)
	c := &client{srv: s, ws: ws, out: make(chan outMsg, 1024), closed: make(chan struct{}), pending: map[uint64]chan rpcReply{}}
	s.clients.Store(c, struct{}{})
	s.Mgr.AddSink(c)
	go c.writeLoop()
	c.readLoop()
	c.close()
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.closed)
		c.srv.clients.Delete(c)
		c.srv.Mgr.RemoveSink(c)
		_ = c.ws.Close()
		c.reqMu.Lock()
		for id, ch := range c.pending {
			ch <- rpcReply{err: "window closed"}
			delete(c.pending, id)
		}
		c.reqMu.Unlock()
	})
}

func (c *client) writeLoop() {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.closed:
			return
		case m := <-c.out:
			_ = c.ws.SetWriteDeadline(time.Now().Add(60 * time.Second))
			mt := websocket.TextMessage
			if m.binary {
				mt = websocket.BinaryMessage
			}
			if err := c.ws.WriteMessage(mt, m.data); err != nil {
				c.close()
				return
			}
		case <-ping.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(20 * time.Second))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *client) enqueue(m outMsg) {
	select {
	case c.out <- m:
	case <-c.closed:
	}
}

func (c *client) sendJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("hawser: cannot encode message: %v", err)
		return
	}
	c.enqueue(outMsg{data: b})
}

// Data implements conn.Sink.
func (c *client) Data(id uint32, p []byte) {
	frame := make([]byte, 5+len(p))
	frame[0] = frameOutput
	binary.BigEndian.PutUint32(frame[1:], id)
	copy(frame[5:], p)
	c.enqueue(outMsg{binary: true, data: frame})
}

// Event implements conn.Sink.
func (c *client) Event(name string, payload any) {
	c.sendJSON(map[string]any{"event": name, "data": payload})
}

// request performs a server-to-UI call and waits for the answer.
func (c *client) request(ctx context.Context, name string, data any) (json.RawMessage, error) {
	rid := c.nextReq.Add(1)
	ch := make(chan rpcReply, 1)
	c.reqMu.Lock()
	c.pending[rid] = ch
	c.reqMu.Unlock()
	c.sendJSON(map[string]any{"rid": rid, "request": name, "data": data})
	select {
	case r := <-ch:
		if r.err != "" {
			return nil, errors.New(r.err)
		}
		return r.result, nil
	case <-ctx.Done():
		c.reqMu.Lock()
		delete(c.pending, rid)
		c.reqMu.Unlock()
		c.sendJSON(map[string]any{"event": "request.cancel", "data": map[string]any{"rid": rid}})
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errors.New("window closed")
	}
}

// Prompt implements conn.Sink.
func (c *client) Prompt(ctx context.Context, id uint32, req conn.PromptRequest) (conn.PromptResponse, error) {
	raw, err := c.request(ctx, "prompt", map[string]any{"conn": id, "prompt": req})
	if err != nil {
		return conn.PromptResponse{}, err
	}
	var resp conn.PromptResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return conn.PromptResponse{}, err
	}
	if !resp.OK && req.Kind != "hostkey" {
		return resp, conn.ErrCancelled
	}
	return resp, nil
}

func (c *client) readLoop() {
	for {
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.BinaryMessage {
			if len(data) >= 5 && data[0] == frameInput {
				if cn := c.srv.Mgr.Get(binary.BigEndian.Uint32(data[1:5])); cn != nil {
					cn.Input(data[5:])
				}
			}
			continue
		}
		var m inMsg
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		switch {
		case m.RID != nil:
			c.reqMu.Lock()
			ch := c.pending[*m.RID]
			delete(c.pending, *m.RID)
			c.reqMu.Unlock()
			if ch != nil {
				ch <- rpcReply{result: m.Result, err: m.Error}
			}
		case m.Method != "":
			h := c.srv.handlers[m.Method]
			if h == nil {
				if m.ID != nil {
					c.sendJSON(map[string]any{"id": *m.ID, "error": "unknown method " + m.Method})
				}
				continue
			}
			// Fast, order-sensitive calls run inline; everything else gets its
			// own goroutine so prompts and long transfers never block input.
			if inlineMethods[m.Method] {
				c.dispatch(h, m)
			} else {
				go c.dispatch(h, m)
			}
		}
	}
}

var inlineMethods = map[string]bool{"conn.resize": true, "conn.ack": true, "conn.send": true}

func (c *client) dispatch(h handler, m inMsg) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("hawser: panic in %s: %v", m.Method, r)
			if m.ID != nil {
				c.sendJSON(map[string]any{"id": *m.ID, "error": fmt.Sprint("internal error: ", r)})
			}
		}
	}()
	res, err := h(c, m.Params)
	if m.ID == nil {
		return
	}
	if err != nil {
		c.sendJSON(map[string]any{"id": *m.ID, "error": err.Error()})
		return
	}
	c.sendJSON(map[string]any{"id": *m.ID, "result": res})
}

// broadcast sends an event to every window.
func (s *Server) broadcast(name string, payload any) {
	s.clients.Range(func(k, _ any) bool {
		k.(*client).Event(name, payload)
		return true
	})
}

// anyClient returns some attached window (for server-initiated dialogs).
func (s *Server) anyClient() *client {
	var out *client
	s.clients.Range(func(k, _ any) bool {
		out = k.(*client)
		return false
	})
	return out
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 || string(raw) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("bad parameters: %w", err)
	}
	return v, nil
}
