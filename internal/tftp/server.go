package tftp

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event reports server activity to Server.OnEvent.
type Event struct {
	Time   time.Time
	Kind   string // "rrq", "wrq", "done", "error"
	Remote string
	File   string
	Bytes  int64
	Err    error
}

// ErrServerClosed is returned by Serve after Close.
var ErrServerClosed = errors.New("tftp: server closed")

// Server is a TFTP server confined to a root directory.
type Server struct {
	Root           string        // directory served; requests are confined to it (no traversal, no symlink escape)
	AllowRead      bool          // serve read requests
	AllowWrite     bool          // accept write requests
	AllowOverwrite bool          // let write requests replace existing files
	Timeout        time.Duration // retransmission timeout, default 5s
	Retries        int           // retransmissions before giving up, default 5
	OnEvent        func(Event)   // may be nil; called from transfer goroutines

	mu     sync.Mutex
	pc     net.PacketConn
	closed bool
	done   chan struct{}
	wg     sync.WaitGroup

	// listen opens the socket of a transfer; tests replace it.
	listen func(laddr *net.UDPAddr) (net.PacketConn, error)
}

// ListenAndServe binds addr (e.g. ":69" or "127.0.0.1:0") and serves until
// Close is called.
func (s *Server) ListenAndServe(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	return s.Serve(pc)
}

// Addr returns the address the server listens on, or nil before Serve.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pc == nil {
		return nil
	}
	return s.pc.LocalAddr()
}

// Close stops accepting requests and waits for running transfers.
func (s *Server) Close() error {
	s.mu.Lock()
	pc, done := s.pc, s.done
	s.closed = true
	s.mu.Unlock()
	if pc == nil {
		return nil
	}
	err := pc.Close()
	<-done
	return err
}

// Serve answers requests arriving on pc until Close is called. Every
// transfer runs on its own ephemeral UDP port. Serve returns after all
// transfers have finished.
func (s *Server) Serve(pc net.PacketConn) error {
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		pc.Close()
		return err
	}
	s.mu.Lock()
	if s.closed || s.pc != nil {
		s.mu.Unlock()
		root.Close()
		pc.Close()
		return ErrServerClosed
	}
	s.pc, s.done = pc, make(chan struct{})
	s.mu.Unlock()
	defer func() {
		s.wg.Wait()
		root.Close()
		close(s.done)
	}()

	buf := make([]byte, 2048)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return ErrServerClosed
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			time.Sleep(5 * time.Millisecond) // transient (e.g. ICMP) error
			continue
		}
		req, err := parseRequest(buf[:n])
		if err != nil {
			pc.WriteTo(errorPacket(CodeIllegalOp, "illegal TFTP operation"), from)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(root, req, from)
		}()
	}
}

func (s *Server) event(kind string, from net.Addr, file string, n int64, err error) {
	if s.OnEvent != nil {
		s.OnEvent(Event{Time: time.Now(), Kind: kind, Remote: from.String(), File: file, Bytes: n, Err: err})
	}
}

// cleanName turns a requested file name into a path relative to the root.
// Names that would leave the root are rejected.
func cleanName(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false
	}
	name = strings.ReplaceAll(name, "\\", "/")
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	name = path.Clean(strings.TrimLeft(name, "/"))
	if name == "." || name == "" || !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", false
	}
	return filepath.FromSlash(name), true
}

// refused wraps an error that the server itself raises and must report to
// the client, as opposed to an error packet received from the client.
type refused struct{ e *Error }

func (r refused) Error() string { return r.e.Error() }

func (s *Server) handle(root *os.Root, req request, from net.Addr) {
	kind := "rrq"
	if req.op == opWRQ {
		kind = "wrq"
	}
	s.event(kind, from, req.filename, 0, nil)

	laddr := &net.UDPAddr{}
	if u, ok := s.pc.LocalAddr().(*net.UDPAddr); ok && !u.IP.IsUnspecified() {
		laddr.IP, laddr.Zone = u.IP, u.Zone
	}
	listen := s.listen
	if listen == nil {
		listen = func(a *net.UDPAddr) (net.PacketConn, error) { return net.ListenUDP("udp", a) }
	}
	pc, err := listen(laddr)
	if err != nil {
		s.event("error", from, req.filename, 0, err)
		return
	}
	defer pc.Close()
	c := newConn(pc, from, s.Timeout, s.Retries)

	n, err := s.transfer(root, c, req)
	if err != nil {
		var refusal refused
		if errors.As(err, &refusal) {
			c.fail(refusal.e.Code, refusal.e.Message)
			err = refusal.e
		}
		s.event("error", from, req.filename, n, err)
		return
	}
	s.event("done", from, req.filename, n, nil)
	if req.op == opWRQ {
		s.dally(c, uint16(n/int64(c.blksize)+1))
	}
}

// dally lingers after the final ACK of an upload: if that ACK was lost the
// client repeats its last block and must be acknowledged again, or it would
// report a failure for a file that arrived completely. Close cuts it short.
func (s *Server) dally(c *conn, block uint16) {
	end := time.Now().Add(2 * c.timeout)
	for time.Now().Before(end) {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		op, p, err := c.read(time.Now().Add(min(100*time.Millisecond, time.Until(end))))
		if isTimeout(err) {
			continue
		}
		if err != nil {
			return
		}
		if op == opDATA && uint16(p[2])<<8|uint16(p[3]) == block {
			c.pc.WriteTo(ackPacket(block), c.peer)
		}
	}
}

func (s *Server) transfer(root *os.Root, c *conn, req request) (int64, error) {
	if req.mode != "octet" {
		return 0, refused{&Error{CodeIllegalOp, "only octet mode is supported"}}
	}
	name, ok := cleanName(req.filename)
	if !ok {
		return 0, refused{&Error{CodeAccessViolation, "access violation"}}
	}

	var f *os.File
	var err error
	size := int64(-1)
	created := false
	if req.op == opRRQ {
		if !s.AllowRead {
			return 0, refused{&Error{CodeAccessViolation, "reading is disabled"}}
		}
		if f, err = root.Open(name); err == nil {
			st, serr := f.Stat()
			if serr != nil || !st.Mode().IsRegular() {
				f.Close()
				return 0, refused{&Error{CodeNotFound, "file not found"}}
			}
			size = st.Size()
		}
	} else {
		if !s.AllowWrite {
			return 0, refused{&Error{CodeAccessViolation, "writing is disabled"}}
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if s.AllowOverwrite {
			flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		}
		f, err = root.OpenFile(name, flags, 0o644)
		created = err == nil && !s.AllowOverwrite
	}
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return 0, refused{&Error{CodeNotFound, "file not found"}}
	case errors.Is(err, fs.ErrExist):
		return 0, refused{&Error{CodeFileExists, "file already exists"}}
	default:
		return 0, refused{&Error{CodeAccessViolation, "access violation"}}
	}
	defer f.Close()

	// Option negotiation.
	var accepted []option
	for _, o := range req.opts {
		switch o.name {
		case "blksize":
			if v, ok := atoiRange(o.value, minBlockSize, 65535); ok {
				c.blksize = min(v, maxBlockSize)
				accepted = append(accepted, option{o.name, strconv.Itoa(c.blksize)})
			}
		case "timeout":
			if v, ok := atoiRange(o.value, 1, 255); ok {
				c.timeout = time.Duration(v) * time.Second
				accepted = append(accepted, o)
			}
		case "tsize":
			if req.op == opRRQ {
				accepted = append(accepted, option{o.name, strconv.FormatInt(size, 10)})
			} else if _, err := strconv.ParseInt(o.value, 10, 64); err == nil {
				accepted = append(accepted, o)
			}
		}
	}

	if req.op == opRRQ {
		if len(accepted) > 0 {
			if _, err := c.exchange(oackPacket(accepted), c.peer, 0); err != nil {
				return 0, err
			}
		}
		return c.send(f)
	}
	first := ackPacket(0)
	if len(accepted) > 0 {
		first = oackPacket(accepted)
	}
	if _, err := c.pc.WriteTo(first, c.peer); err != nil {
		return 0, err
	}
	n, err := c.receive(f, first, c.peer, nil)
	if err != nil && created {
		f.Close()
		root.Remove(name) // do not leave a partial upload behind
	}
	return n, err
}
