package xfer

import (
	"context"
	"sync"
	"time"
)

const (
	// pumpPoll is the read timeout the session pump passes to the stream. It
	// bounds how long a transfer function lingers after it has finished
	// before the pump notices and exits.
	pumpPoll = 20 * time.Millisecond
	// pumpHighWater bounds the amount of unprocessed input held in memory.
	pumpHighWater = 256 << 10
)

// lineReader is the receive side of a transfer session. A pump goroutine
// owns all reads from the Stream and queues the bytes; the protocol code
// consumes them with timeouts, can test for pending input without blocking
// (needed to watch the reverse channel while streaming) and is woken
// immediately when the context is cancelled.
type lineReader struct {
	s   Stream
	ctx context.Context // consumer side only

	mu   sync.Mutex
	q    []byte // bytes queued by the pump
	qerr error  // sticky stream error, delivered after q is drained

	wake  chan struct{} // pump -> consumer: q or qerr changed
	space chan struct{} // consumer -> pump: q was taken
	stop  chan struct{}
	done  chan struct{}

	// Consumer-owned buffer; the pump never touches it.
	cur []byte
	pos int
}

func newLineReader(ctx context.Context, s Stream) *lineReader {
	r := &lineReader{
		s:     s,
		ctx:   ctx,
		wake:  make(chan struct{}, 1),
		space: make(chan struct{}, 1),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go r.pump()
	return r
}

func (r *lineReader) pump() {
	defer close(r.done)
	buf := make([]byte, 16<<10)
	for {
		r.mu.Lock()
		full := len(r.q) >= pumpHighWater
		r.mu.Unlock()
		if full {
			select {
			case <-r.space:
			case <-r.stop:
				return
			}
			continue
		}
		select {
		case <-r.stop:
			return
		default:
		}
		n, err := r.s.ReadTimeout(buf, pumpPoll)
		fatal := err != nil && !isTimeout(err)
		if n > 0 || fatal {
			r.mu.Lock()
			r.q = append(r.q, buf[:n]...)
			if fatal && r.qerr == nil {
				r.qerr = err
			}
			r.mu.Unlock()
			signal(r.wake)
		}
		if fatal {
			return
		}
		if n == 0 && err == nil {
			// A stream that returns (0, nil) violates the contract; do not
			// spin on it.
			t := time.NewTimer(time.Millisecond)
			select {
			case <-t.C:
			case <-r.stop:
				t.Stop()
				return
			}
		}
	}
}

// close stops the pump and returns every byte that was read from the stream
// but not consumed by the protocol.
func (r *lineReader) close() []byte {
	close(r.stop)
	<-r.done
	left := append([]byte(nil), r.cur[r.pos:]...)
	left = append(left, r.q...)
	r.cur, r.pos, r.q = nil, 0, nil
	return left
}

// get returns the next input byte, waiting up to d for it. It returns
// ErrTimeout when no byte arrives in time, the context's error when the
// context is cancelled and the stream's error when the stream fails.
func (r *lineReader) get(d time.Duration) (byte, error) {
	if r.pos < len(r.cur) {
		c := r.cur[r.pos]
		r.pos++
		return c, nil
	}
	if err := r.fill(d); err != nil {
		return 0, err
	}
	c := r.cur[r.pos]
	r.pos++
	return c, nil
}

// unget pushes back the byte most recently returned by get.
func (r *lineReader) unget() {
	if r.pos > 0 {
		r.pos--
	}
}

// unread pushes bytes back; they are returned again, in order, by the next
// calls to get.
func (r *lineReader) unread(p []byte) {
	rest := r.cur[r.pos:]
	nb := make([]byte, 0, len(p)+len(rest))
	nb = append(append(nb, p...), rest...)
	r.cur, r.pos = nb, 0
}

// buffered reports whether get would return a byte without waiting.
func (r *lineReader) buffered() bool {
	if r.pos < len(r.cur) {
		return true
	}
	r.mu.Lock()
	n := len(r.q)
	r.mu.Unlock()
	return n > 0
}

// fill waits up to d for the pump to deliver data and makes it the current
// consumer buffer.
func (r *lineReader) fill(d time.Duration) error {
	var timer *time.Timer
	var expired <-chan time.Time
	for {
		// Checked before data is handed out, so that a cancellation is
		// noticed even while the remote keeps the input queue full.
		if err := r.ctx.Err(); err != nil {
			return err
		}
		r.mu.Lock()
		if len(r.q) > 0 {
			r.cur, r.q = r.q, r.cur[:0]
			r.pos = 0
			r.mu.Unlock()
			signal(r.space)
			return nil
		}
		err := r.qerr
		r.mu.Unlock()
		if err != nil {
			return err
		}
		if d <= 0 {
			return ErrTimeout
		}
		if timer == nil {
			timer = time.NewTimer(d)
			defer timer.Stop()
			expired = timer.C
		}
		select {
		case <-r.wake:
		case <-expired:
			d = 0 // one last look at the queue, then give up
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
}

// getUntil is get with an absolute deadline.
func (r *lineReader) getUntil(deadline time.Time) (byte, error) {
	if r.pos < len(r.cur) {
		c := r.cur[r.pos]
		r.pos++
		return c, nil
	}
	return r.get(time.Until(deadline))
}
