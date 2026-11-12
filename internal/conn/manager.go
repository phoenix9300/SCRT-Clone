package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// Sink is the UI side of a connection: it renders output, shows prompts and
// receives notifications.
type Sink interface {
	// Data delivers terminal output. The slice must not be modified.
	Data(id uint32, p []byte)
	// Event delivers an asynchronous notification.
	Event(name string, payload any)
	// Prompt asks the user a question on behalf of a connection.
	Prompt(ctx context.Context, id uint32, req PromptRequest) (PromptResponse, error)
}

// Connection states.
const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
)

// Manager owns every live connection. Connections outlive the UI: a window
// can be closed or reloaded and re-attach to its sessions later.
type Manager struct {
	Store *store.Store
	// Keyring is Hawser's built-in SSH agent (keys added for this run).
	Keyring agent.Agent

	mu     sync.Mutex
	conns  map[uint32]*Conn
	nextID uint32
	sinks  map[Sink]struct{}

	// OnConnected, when set, is called (in its own goroutine) each time a
	// session reaches the connected state; used to start logon scripts.
	OnConnected func(c *Conn)
}

// NewManager creates a connection manager.
func NewManager(st *store.Store) *Manager {
	return &Manager{Store: st, Keyring: agent.NewKeyring(), conns: map[uint32]*Conn{}, sinks: map[Sink]struct{}{}}
}

// AddSink registers a UI.
func (m *Manager) AddSink(s Sink) {
	m.mu.Lock()
	m.sinks[s] = struct{}{}
	m.mu.Unlock()
}

// RemoveSink detaches a UI from the manager and from every connection.
func (m *Manager) RemoveSink(s Sink) {
	m.mu.Lock()
	delete(m.sinks, s)
	conns := make([]*Conn, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.mu.Unlock()
	for _, c := range conns {
		c.detach(s)
	}
}

// Broadcast sends an event to every attached UI.
func (m *Manager) Broadcast(name string, payload any) {
	m.mu.Lock()
	sinks := make([]Sink, 0, len(m.sinks))
	for s := range m.sinks {
		sinks = append(sinks, s)
	}
	m.mu.Unlock()
	for _, s := range sinks {
		s.Event(name, payload)
	}
}

// OpenRequest describes a connection to create.
type OpenRequest struct {
	SessionPath string         `json:"session"`
	Overrides   map[string]any `json:"overrides"`
	Cols        int            `json:"cols"`
	Rows        int            `json:"rows"`
	// CloneOf opens a new channel on an existing SSH connection.
	CloneOf  uint32 `json:"cloneOf"`
	Password string `json:"password"`
	Title    string `json:"title"`
}

// Open creates a connection and starts connecting in the background.
func (m *Manager) Open(req OpenRequest, sink Sink) (*Conn, error) {
	var opts store.SessionOptions
	var err error
	var parent *Conn
	if req.CloneOf != 0 {
		parent = m.Get(req.CloneOf)
		if parent == nil {
			return nil, errors.New("the session to clone no longer exists")
		}
		opts = parent.Options()
		if req.SessionPath == "" {
			req.SessionPath = parent.SessionPath
		}
		if req.Overrides == nil {
			req.Overrides = parent.overrides
		}
	} else if req.SessionPath != "" {
		ov, oerr := m.Store.Overrides(req.SessionPath)
		if oerr != nil {
			return nil, fmt.Errorf("session %q: %w", req.SessionPath, oerr)
		}
		if req.Overrides != nil {
			deepMergeAny(ov, req.Overrides)
		}
		opts, err = m.Store.Resolve(ov)
	} else {
		opts, err = m.Store.Resolve(req.Overrides)
	}
	if err != nil {
		return nil, err
	}
	if parent == nil {
		pw, cerr := applyCredential(m.Store, &opts)
		if cerr != nil {
			return nil, cerr
		}
		if req.Password == "" {
			req.Password = pw
		}
	}
	title := req.Title
	if title == "" {
		if req.SessionPath != "" {
			title = req.SessionPath[strings.LastIndexByte(req.SessionPath, '/')+1:]
		} else {
			title = Target(opts)
		}
	}
	m.mu.Lock()
	m.nextID++
	c := &Conn{
		ID: m.nextID, mgr: m, SessionPath: req.SessionPath, Title: title,
		opts: opts, overrides: req.Overrides, password: req.Password,
		cols: req.Cols, rows: req.Rows, sink: sink, state: StateDisconnected,
		text: newTextBuffer(), created: time.Now(),
	}
	c.ackCond = sync.NewCond(&c.mu)
	c.inputCh = make(chan inputItem, 256)
	m.conns[c.ID] = c
	m.mu.Unlock()
	go c.inputLoop()
	c.connect(parent)
	return c, nil
}

func deepMergeAny(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMergeAny(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// Get returns a connection by id.
func (m *Manager) Get(id uint32) *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conns[id]
}

// List returns every connection ordered by id.
func (m *Manager) List() []*Conn {
	m.mu.Lock()
	out := make([]*Conn, 0, len(m.conns))
	for _, c := range m.conns {
		out = append(out, c)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Close disconnects and forgets a connection.
func (m *Manager) Close(id uint32) {
	m.mu.Lock()
	c := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	if c != nil {
		c.shutdown()
		m.Broadcast("conn.closed", map[string]any{"id": id})
	}
}

// CloseAll disconnects everything (application exit).
func (m *Manager) CloseAll() {
	for _, c := range m.List() {
		m.Close(c.ID)
	}
}

// ------------------------------------------------------------------ Conn

type inputItem struct {
	data      []byte
	charDelay time.Duration
	lineDelay time.Duration
	done      chan struct{}
}

// Conn is one terminal session.
type Conn struct {
	ID          uint32
	SessionPath string
	Title       string

	mgr       *Manager
	overrides map[string]any
	password  string
	created   time.Time

	mu          sync.Mutex
	opts        store.SessionOptions
	state       string
	message     string
	failed      bool
	transport   Transport
	cancelDial  context.CancelFunc
	cols, rows  int
	gen         int
	userClosed  bool
	dead        bool
	connectedAt time.Time
	stopGen     chan struct{} // closed when the current generation ends

	sink    Sink
	replay  ring
	outPos  int64
	unacked int64
	ackCond *sync.Cond

	strip   stripper
	text    *textBuffer
	log     *sessionLog
	divert  *diverter
	rec     *Recorder
	inputCh chan inputItem

	lastInput atomic.Int64 // unix nanos
	bytesIn   atomic.Int64
	bytesOut  atomic.Int64

	// OutputTap, when non-nil, sees all terminal output (used by transfers).
	zdetect zmodemDetector
	zqueue  []string
}

// Info is the externally visible state of a connection.
type Info struct {
	ID          uint32                `json:"id"`
	Session     string                `json:"session"`
	Title       string                `json:"title"`
	State       string                `json:"state"`
	Message     string                `json:"message"`
	Failed      bool                  `json:"failed"`
	Protocol    string                `json:"protocol"`
	Target      string                `json:"target"`
	Cols        int                   `json:"cols"`
	Rows        int                   `json:"rows"`
	ConnectedAt int64                 `json:"connectedAt,omitempty"`
	Logging     bool                  `json:"logging"`
	LogFile     string                `json:"logFile,omitempty"`
	Recording   bool                  `json:"recording"`
	BytesIn     int64                 `json:"bytesIn"`
	BytesOut    int64                 `json:"bytesOut"`
	Details     map[string]string     `json:"details,omitempty"`
	Options     *store.SessionOptions `json:"options,omitempty"`
}

// Options returns the effective session options.
func (c *Conn) Options() store.SessionOptions {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opts
}

// Info snapshots the connection. withOptions adds the full option set.
func (c *Conn) Info(withOptions bool) Info {
	c.mu.Lock()
	t := c.transport
	inf := Info{
		ID: c.ID, Session: c.SessionPath, Title: c.Title, State: c.state, Message: c.message, Failed: c.failed,
		Protocol: c.opts.Protocol, Target: Target(c.opts), Cols: c.cols, Rows: c.rows,
		Recording: c.rec != nil,
		BytesIn:   c.bytesIn.Load(), BytesOut: c.bytesOut.Load(),
	}
	if !c.connectedAt.IsZero() && c.state == StateConnected {
		inf.ConnectedAt = c.connectedAt.UnixMilli()
	}
	if c.log != nil {
		inf.Logging = true
		inf.LogFile = c.log.Path()
	}
	if withOptions {
		o := c.opts
		inf.Options = &o
	}
	c.mu.Unlock()
	if in, ok := t.(Informer); ok && t != nil {
		inf.Details = in.Info()
	}
	return inf
}

func (c *Conn) setState(state, message string, failed bool) {
	c.mu.Lock()
	c.state = state
	c.message = message
	c.failed = failed
	c.mu.Unlock()
	c.mgr.Broadcast("conn.state", c.Info(false))
}

// State returns the current connection state.
func (c *Conn) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Connected reports whether the transport is up.
func (c *Conn) Connected() bool { return c.State() == StateConnected }

type connPrompter struct{ c *Conn }

func (p connPrompter) Prompt(ctx context.Context, req PromptRequest) (PromptResponse, error) {
	p.c.mu.Lock()
	sink := p.c.sink
	p.c.mu.Unlock()
	if sink == nil {
		return PromptResponse{}, errors.New("no window is attached to answer the prompt")
	}
	return sink.Prompt(ctx, p.c.ID, req)
}

func (p connPrompter) Notice(text string) {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	p.c.emit([]byte(text), false)
}

// connect starts (re)connecting. parent, when set, shares its SSH transport.
func (c *Conn) connect(parent *Conn) {
	c.mu.Lock()
	if c.dead || c.state == StateConnecting || c.state == StateConnected {
		c.mu.Unlock()
		return
	}
	c.userClosed = false
	c.gen++
	gen := c.gen
	ctx, cancel := context.WithCancel(context.Background())
	c.cancelDial = cancel
	opts := c.opts
	cols, rows := c.cols, c.rows
	c.state = StateConnecting
	c.mu.Unlock()
	c.text.reopen()
	c.setState(StateConnecting, "Connecting to "+Target(opts)+" ...", false)

	go func() {
		defer cancel()
		req := &DialRequest{
			Opts: opts, SessionPath: c.SessionPath, Store: c.mgr.Store, Prompter: connPrompter{c},
			Cols: cols, Rows: rows, Manager: c.mgr, Password: c.password,
		}
		var t Transport
		var err error
		if parent != nil {
			t, err = cloneTransport(ctx, parent, req)
		} else {
			t, err = Dial(ctx, req)
		}
		c.mu.Lock()
		if err == nil && (c.dead || c.userClosed || c.gen != gen) {
			c.mu.Unlock()
			_ = t.Close()
			return
		}
		if err != nil {
			c.cancelDial = nil
			userClosed := c.userClosed
			c.mu.Unlock()
			msg := err.Error()
			if errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled) || userClosed {
				msg = "Connection cancelled"
			}
			c.text.close()
			c.setState(StateDisconnected, msg, true)
			c.scheduleReconnect(gen)
			return
		}
		c.transport = t
		c.cancelDial = nil
		c.connectedAt = time.Now()
		c.stopGen = make(chan struct{})
		stop := c.stopGen
		c.strip = stripper{}
		// the remote side may have been given a stale size while we connected
		ncols, nrows := c.cols, c.rows
		c.mu.Unlock()
		if ncols != cols || nrows != rows {
			_ = t.Resize(ncols, nrows)
		}
		if tt, ok := t.(*telnetTransport); ok {
			tt.setNotify(func(ev string, v any) {
				c.mgr.Broadcast("conn.notify", map[string]any{"id": c.ID, "event": ev, "value": v})
			})
		}
		c.lastInput.Store(time.Now().UnixNano())
		c.setState(StateConnected, "", false)
		c.startAutoLog()
		go c.antiIdleLoop(t, stop)
		go c.runLogonActions(stop)
		if hook := c.mgr.OnConnected; hook != nil {
			go hook(c)
		}
		c.readLoop(t, gen)
	}()
}

// cloneTransport opens another shell channel on parent's SSH connection.
func cloneTransport(ctx context.Context, parent *Conn, req *DialRequest) (Transport, error) {
	parent.mu.Lock()
	pt := parent.transport
	parent.mu.Unlock()
	st, ok := pt.(*sshTransport)
	if !ok || pt == nil {
		// not an SSH session (or not connected): open an independent connection
		return Dial(ctx, req)
	}
	sc := st.shared()
	sc.retain()
	t, err := sc.openTerminal(req)
	if err != nil {
		sc.release()
		return nil, err
	}
	return t, nil
}

func (c *Conn) readLoop(t Transport, gen int) {
	buf := make([]byte, 64*1024)
	for {
		n, err := t.Read(buf)
		if n > 0 {
			c.bytesIn.Add(int64(n))
			c.handleOutput(buf[:n])
		}
		if err != nil {
			c.onDisconnected(t, gen, err)
			return
		}
	}
}

func (c *Conn) onDisconnected(t Transport, gen int, err error) {
	_ = t.Close()
	c.mu.Lock()
	if c.gen != gen {
		c.mu.Unlock()
		return
	}
	c.transport = nil
	userClosed := c.userClosed
	if c.stopGen != nil {
		close(c.stopGen)
		c.stopGen = nil
	}
	d := c.divert
	lg := c.log
	c.mu.Unlock()
	if d != nil {
		d.fail(io.EOF)
	}
	c.text.close()
	if lg != nil {
		lg.event("disconnect")
		if !c.Options().Log.StartOnConnect {
			lg.Flush()
		} else {
			c.StopLog()
		}
	}
	msg := "Disconnected"
	failed := false
	var ee *ExitStatusError
	switch {
	case userClosed:
	case err == nil || errors.Is(err, io.EOF) || errors.Is(err, errProcessExited):
		msg = "Connection closed"
	case errors.As(err, &ee):
		msg = fmt.Sprintf("Connection closed (exit status %d)", ee.Code)
	default:
		msg = "Connection lost: " + err.Error()
		failed = true
	}
	c.setState(StateDisconnected, msg, failed)
	if !userClosed {
		c.scheduleReconnect(gen)
	}
}

func (c *Conn) scheduleReconnect(gen int) {
	o := c.Options().Connection
	if !o.AutoReconnect {
		return
	}
	delay := time.Duration(o.ReconnectDelay) * time.Second
	if delay < time.Second {
		delay = time.Second
	}
	time.AfterFunc(delay, func() {
		c.mu.Lock()
		ok := !c.dead && !c.userClosed && c.gen == gen && c.state == StateDisconnected
		c.mu.Unlock()
		if ok {
			c.connect(nil)
		}
	})
}

// Reconnect re-establishes a disconnected session.
func (c *Conn) Reconnect() {
	if c.State() == StateDisconnected {
		// pick up edits made to the stored session since the last connect
		if c.SessionPath != "" {
			if ov, err := c.mgr.Store.Overrides(c.SessionPath); err == nil {
				if c.overrides != nil {
					deepMergeAny(ov, c.overrides)
				}
				if o, err := c.mgr.Store.Resolve(ov); err == nil {
					pw, _ := applyCredential(c.mgr.Store, &o)
					c.mu.Lock()
					if pw != "" {
						c.password = pw
					}
					c.opts = o
					c.mu.Unlock()
				}
			}
		}
		c.connect(nil)
	}
}

// Retarget points a disconnected connection at another session or ad-hoc
// target (used by scripts: crt.Session.Connect). Call Reconnect afterwards.
func (m *Manager) Retarget(c *Conn, req OpenRequest) error {
	if c.State() != StateDisconnected {
		return errors.New("the session is still connected")
	}
	var ov map[string]any
	if req.SessionPath != "" {
		var err error
		if ov, err = m.Store.Overrides(req.SessionPath); err != nil {
			return err
		}
		if req.Overrides != nil {
			deepMergeAny(ov, req.Overrides)
		}
	} else {
		ov = req.Overrides
	}
	opts, err := m.Store.Resolve(ov)
	if err != nil {
		return err
	}
	if pw, cerr := applyCredential(m.Store, &opts); cerr != nil {
		return cerr
	} else if req.Password == "" {
		req.Password = pw
	}
	c.mu.Lock()
	c.opts = opts
	c.SessionPath = req.SessionPath
	c.overrides = req.Overrides
	c.password = req.Password
	if req.Title != "" {
		c.Title = req.Title
	} else if req.SessionPath != "" {
		c.Title = req.SessionPath[strings.LastIndexByte(req.SessionPath, '/')+1:]
	} else {
		c.Title = Target(opts)
	}
	c.mu.Unlock()
	c.connect(nil)
	return nil
}

// Disconnect closes the transport but keeps the tab.
func (c *Conn) Disconnect() {
	c.mu.Lock()
	c.userClosed = true
	t := c.transport
	cancel := c.cancelDial
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if t != nil {
		_ = t.Close()
	}
}

func (c *Conn) shutdown() {
	c.mu.Lock()
	c.dead = true
	c.sink = nil
	c.ackCond.Broadcast()
	c.mu.Unlock()
	c.Disconnect()
	c.StopLog()
	close(c.inputCh)
}

// ------------------------------------------------------------------ output

func (c *Conn) handleOutput(p []byte) {
	c.mu.Lock()
	d := c.divert
	c.mu.Unlock()
	if d != nil {
		d.push(p)
		return
	}
	if rest, handled := c.checkZmodem(p); handled {
		if len(rest) > 0 {
			c.emit(rest, true)
		}
		return
	}
	c.emit(p, true)
}

// emit sends bytes to the terminal. remote marks data that really came from
// the host (and therefore feeds logging and expect matching).
func (c *Conn) emit(p []byte, remote bool) {
	if len(p) == 0 {
		return
	}
	data := make([]byte, len(p))
	copy(data, p)
	c.mu.Lock()
	var stripped []byte
	if remote {
		stripped = c.strip.Feed(data, nil)
	}
	lg := c.log
	c.replay.write(data)
	c.outPos += int64(len(data))
	sink := c.sink
	c.mu.Unlock()
	if remote {
		c.text.append(stripped)
		if lg != nil {
			lg.write(data, stripped)
		}
	}
	if sink == nil {
		return
	}
	sink.Data(c.ID, data)
	// flow control: do not outrun the terminal renderer
	c.mu.Lock()
	c.unacked += int64(len(data))
	for c.unacked > flowHighWater && c.sink == sink && !c.dead {
		c.ackCond.Wait()
	}
	c.mu.Unlock()
}

const flowHighWater = 2 << 20

// Ack is called by the UI after it has processed n bytes of output.
func (c *Conn) Ack(n int64) {
	c.mu.Lock()
	c.unacked -= n
	if c.unacked < 0 {
		c.unacked = 0
	}
	c.ackCond.Broadcast()
	c.mu.Unlock()
}

// Attach makes sink the viewer of this connection and returns the buffered
// output so the terminal can be rebuilt.
func (c *Conn) Attach(sink Sink) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink = sink
	c.unacked = 0
	c.ackCond.Broadcast()
	return c.replay.bytes()
}

func (c *Conn) detach(sink Sink) {
	c.mu.Lock()
	if c.sink == sink {
		c.sink = nil
		c.unacked = 0
		c.ackCond.Broadcast()
	}
	c.mu.Unlock()
}

// OutputOffset is the number of bytes sent to the terminal so far.
func (c *Conn) OutputOffset() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outPos
}

// ------------------------------------------------------------------- input

func (c *Conn) inputLoop() {
	for it := range c.inputCh {
		c.writeItem(it)
		if it.done != nil {
			close(it.done)
		}
	}
}

func (c *Conn) writeItem(it inputItem) {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t == nil || len(it.data) == 0 {
		return
	}
	c.lastInput.Store(time.Now().UnixNano())
	if it.charDelay <= 0 && it.lineDelay <= 0 {
		if n, err := t.Write(it.data); err == nil {
			c.bytesOut.Add(int64(n))
		}
		return
	}
	// throttled send (pasted text into slow devices)
	data := it.data
	for len(data) > 0 {
		c.mu.Lock()
		alive := c.transport == t
		c.mu.Unlock()
		if !alive {
			return
		}
		n := len(data)
		if it.charDelay > 0 {
			n = 1
			// keep UTF-8 sequences intact
			for n < len(data) && data[n]&0xc0 == 0x80 {
				n++
			}
		} else {
			for i, b := range data {
				if b == '\r' || b == '\n' {
					n = i + 1
					if b == '\r' && n < len(data) && data[n] == '\n' {
						n++
					}
					break
				}
			}
		}
		chunk := data[:n]
		data = data[n:]
		if w, err := t.Write(chunk); err != nil {
			return
		} else {
			c.bytesOut.Add(int64(w))
		}
		last := chunk[len(chunk)-1]
		if it.lineDelay > 0 && (last == '\r' || last == '\n') {
			time.Sleep(it.lineDelay)
		} else if it.charDelay > 0 {
			time.Sleep(it.charDelay)
		}
	}
}

// Input queues keyboard data for the host.
func (c *Conn) Input(p []byte) {
	if len(p) == 0 {
		return
	}
	data := make([]byte, len(p))
	copy(data, p)
	c.mu.Lock()
	dead := c.dead
	rec := c.rec
	d := c.divert
	c.mu.Unlock()
	if dead || d != nil {
		return // keyboard is ignored during in-band file transfers
	}
	if rec != nil {
		rec.input(c, data)
	}
	defer func() { _ = recover() }() // inputCh closed during shutdown
	c.inputCh <- inputItem{data: data}
}

// Send writes data honouring optional pacing and waits until it is written.
func (c *Conn) Send(p []byte, charDelay, lineDelay time.Duration) {
	if len(p) == 0 {
		return
	}
	c.mu.Lock()
	dead := c.dead
	c.mu.Unlock()
	if dead {
		return
	}
	defer func() { _ = recover() }()
	done := make(chan struct{})
	c.inputCh <- inputItem{data: append([]byte(nil), p...), charDelay: charDelay, lineDelay: lineDelay, done: done}
	<-done
}

// SendString sends text containing \p pause markers and escapes already expanded.
func (c *Conn) SendString(s string) {
	data := Unescape(s)
	for len(data) > 0 {
		i := indexBytes(data, pauseMarker)
		if i < 0 {
			c.Send(data, 0, 0)
			return
		}
		c.Send(data[:i], 0, 0)
		time.Sleep(time.Second)
		data = data[i+len(pauseMarker):]
	}
}

func indexBytes(h, n []byte) int {
	for i := 0; i+len(n) <= len(h); i++ {
		j := 0
		for j < len(n) && h[i+j] == n[j] {
			j++
		}
		if j == len(n) {
			return i
		}
	}
	return -1
}

// Resize records the terminal size and informs the host.
func (c *Conn) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	c.mu.Lock()
	changed := c.cols != cols || c.rows != rows
	c.cols, c.rows = cols, rows
	t := c.transport
	c.mu.Unlock()
	if t != nil && changed {
		_ = t.Resize(cols, rows)
	}
}

// SendBreak sends a BREAK when the transport supports it.
func (c *Conn) SendBreak() error {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if b, ok := t.(Breaker); ok && t != nil {
		return b.SendBreak()
	}
	return errors.New("this connection type cannot send a break")
}

// SSH returns the shared SSH connection of this session, if any.
func (c *Conn) SSH() *sshConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok := c.transport.(*sshTransport); ok {
		return st.shared()
	}
	return nil
}

// Forwards lists the session's active port forwards.
func (c *Conn) Forwards() []ForwardStatus {
	if sc := c.SSH(); sc != nil {
		return sc.Forwards()
	}
	return nil
}

// AddForward starts a port forward on a connected SSH session.
func (c *Conn) AddForward(spec store.PortForward) (*ForwardStatus, error) {
	sc := c.SSH()
	if sc == nil {
		return nil, errors.New("port forwarding needs a connected SSH2 session")
	}
	st := sc.StartForward(spec)
	if st.Error != "" {
		return st, errors.New(st.Error)
	}
	return st, nil
}

// RemoveForward stops a port forward by name.
func (c *Conn) RemoveForward(name string) bool {
	if sc := c.SSH(); sc != nil {
		return sc.StopForward(name)
	}
	return false
}

// ---------------------------------------------------------------- expect

// TextEnd returns the current end offset of the stripped output stream.
func (c *Conn) TextEnd() int64 { return c.text.End() }

// WaitFor waits for one of the patterns in output received at or after from.
func (c *Conn) WaitFor(ctx context.Context, from int64, m *Matcher) (MatchResult, error) {
	return c.text.Wait(ctx, from, m)
}

// TextSlice returns stripped output in [from, to).
func (c *Conn) TextSlice(from, to int64) string { return c.text.Slice(from, to) }

// ------------------------------------------------------------------- ring

// ring keeps the most recent output so a re-attached window can rebuild
// its terminal.
type ring struct {
	buf []byte
}

const replayMax = 4 << 20

func (r *ring) write(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > replayMax {
		drop := len(r.buf) - replayMax*3/4
		// cut at a line boundary so the replay does not start mid-sequence
		for drop < len(r.buf) && r.buf[drop] != '\n' {
			drop++
		}
		r.buf = append(r.buf[:0], r.buf[drop:]...)
	}
}

func (r *ring) bytes() []byte {
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}
