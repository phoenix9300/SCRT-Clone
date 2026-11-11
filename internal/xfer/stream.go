package xfer

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// Stream is the 8-bit path to the remote peer for the duration of a transfer.
// The host (connection manager) diverts terminal output into it.
type Stream interface {
	// Write sends bytes to the remote.
	io.Writer
	// ReadTimeout blocks until at least one byte is available, the timeout
	// elapses (returns 0, os.ErrDeadlineExceeded) or the stream fails or is
	// closed. Any error that reports Timeout() == true is treated like
	// os.ErrDeadlineExceeded.
	//
	// The transfer functions call ReadTimeout from a single internal
	// goroutine, never concurrently, and never after they have returned.
	ReadTimeout(p []byte, d time.Duration) (int, error)
}

// Unreader may be implemented by a Stream to take back bytes that a transfer
// read but that turned out not to be part of the protocol (typically terminal
// output that directly followed the final protocol frame). The bytes must be
// returned by the next ReadTimeout call, ahead of any newer data.
type Unreader interface {
	Unread(p []byte)
}

// pipeStreamHighWater bounds the read-ahead of a PipeStream. When that many
// bytes are waiting to be consumed the pump stops reading, which applies
// back-pressure to the remote.
const pipeStreamHighWater = 1 << 20

// PipeStream adapts an io.Reader and an io.Writer to the Stream interface
// with an internal pump goroutine. It also implements Unreader.
type PipeStream struct {
	r io.Reader
	w io.Writer

	mu     sync.Mutex
	buf    []byte
	off    int
	err    error
	closed bool

	notify chan struct{} // data or error available
	space  chan struct{} // consumer made room
	quit   chan struct{}
	done   chan struct{}
	once   sync.Once
}

// NewPipeStream adapts an io.Reader + io.Writer (e.g. a subprocess's
// stdout/stdin or a net.Conn) to Stream using an internal pump goroutine.
// Close stops the pump.
//
// The pump reads ahead from r, so r must not be read by anyone else while
// the PipeStream is in use.
func NewPipeStream(r io.Reader, w io.Writer) *PipeStream {
	p := &PipeStream{
		r:      r,
		w:      w,
		notify: make(chan struct{}, 1),
		space:  make(chan struct{}, 1),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go p.pump()
	return p
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (p *PipeStream) pump() {
	defer close(p.done)
	tmp := make([]byte, 32<<10)
	for {
		p.mu.Lock()
		full := len(p.buf)-p.off >= pipeStreamHighWater
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return
		}
		if full {
			select {
			case <-p.space:
			case <-p.quit:
				return
			}
			continue
		}
		n, err := p.r.Read(tmp)
		p.mu.Lock()
		if n > 0 {
			if p.off >= 64<<10 {
				m := copy(p.buf, p.buf[p.off:])
				p.buf, p.off = p.buf[:m], 0
			}
			p.buf = append(p.buf, tmp[:n]...)
		}
		if err != nil && p.err == nil {
			if p.closed {
				p.err = io.ErrClosedPipe
			} else {
				p.err = err
			}
		}
		p.mu.Unlock()
		signal(p.notify)
		if err != nil {
			return
		}
	}
}

// Write sends b to the remote. It is a direct call to the underlying writer.
func (p *PipeStream) Write(b []byte) (int, error) { return p.w.Write(b) }

// ReadTimeout implements Stream. A zero or negative d polls: buffered data
// is returned, otherwise the call fails immediately with
// os.ErrDeadlineExceeded. Once the underlying reader has failed (io.EOF
// included) and the buffer is drained, that error is returned.
func (p *PipeStream) ReadTimeout(b []byte, d time.Duration) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	var timer *time.Timer
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return 0, io.ErrClosedPipe
		}
		if n := copy(b, p.buf[p.off:]); n > 0 {
			p.off += n
			if p.off == len(p.buf) {
				p.buf, p.off = p.buf[:0], 0
			}
			p.mu.Unlock()
			signal(p.space)
			return n, nil
		}
		err := p.err
		p.mu.Unlock()
		if err != nil {
			return 0, err
		}
		if d <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		if timer == nil {
			timer = time.NewTimer(d)
			defer timer.Stop()
		}
		select {
		case <-p.notify:
		case <-timer.C:
			return 0, os.ErrDeadlineExceeded
		case <-p.quit:
			return 0, io.ErrClosedPipe
		}
	}
}

// Unread implements Unreader: b is returned by the next ReadTimeout, ahead of
// data that is already buffered.
func (p *PipeStream) Unread(b []byte) {
	if len(b) == 0 {
		return
	}
	p.mu.Lock()
	rest := p.buf[p.off:]
	nb := make([]byte, 0, len(b)+len(rest))
	nb = append(append(nb, b...), rest...)
	p.buf, p.off = nb, 0
	p.mu.Unlock()
	signal(p.notify)
}

// Buffered returns a copy of the bytes that have been read from the
// underlying reader but not yet consumed. It remains usable after Close, so
// a host can recover read-ahead data when it takes the reader back.
func (p *PipeStream) Buffered() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.buf[p.off:]...)
}

// Close stops the pump goroutine. Subsequent ReadTimeout calls fail with
// io.ErrClosedPipe. Close does not close the underlying reader or writer.
//
// If the reader supports read deadlines (net.Conn, os pipes on most
// platforms) the pump is interrupted and has exited when Close returns, and
// the reader is left usable. Otherwise the pump exits as soon as its pending
// Read returns, so the owner should close the reader (or let it reach EOF)
// to release the goroutine.
func (p *PipeStream) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		close(p.quit)
		type deadliner interface{ SetReadDeadline(time.Time) error }
		if d, ok := p.r.(deadliner); ok && d.SetReadDeadline(time.Now()) == nil {
			t := time.NewTimer(time.Second)
			select {
			case <-p.done:
			case <-t.C:
			}
			t.Stop()
			_ = d.SetReadDeadline(time.Time{})
		}
	})
	return nil
}

// isTimeout reports whether err is a read timeout as produced by
// Stream.ReadTimeout.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
