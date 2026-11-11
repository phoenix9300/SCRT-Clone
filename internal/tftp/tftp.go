// Package tftp implements a small TFTP server and client: RFC 1350 in octet
// mode with option negotiation (RFC 2347) for blksize (RFC 2348), timeout
// and tsize (RFC 2349).
package tftp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Opcodes.
const (
	opRRQ   = 1
	opWRQ   = 2
	opDATA  = 3
	opACK   = 4
	opERROR = 5
	opOACK  = 6
)

// TFTP error codes.
const (
	CodeUndefined       = 0
	CodeNotFound        = 1
	CodeAccessViolation = 2
	CodeDiskFull        = 3
	CodeIllegalOp       = 4
	CodeUnknownTID      = 5
	CodeFileExists      = 6
	CodeOptionRejected  = 8
)

const (
	defaultBlockSize = 512
	minBlockSize     = 8
	maxBlockSize     = 65464
	defaultTimeout   = 5 * time.Second
	defaultRetries   = 5
)

// Error is an error packet received from (or sent to) the peer.
type Error struct {
	Code    uint16
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("tftp: error %d: %s", e.Code, e.Message) }

// ErrTimeout reports that the peer stopped answering.
var ErrTimeout = errors.New("tftp: timed out waiting for peer")

func errorPacket(code uint16, msg string) []byte {
	p := make([]byte, 4, 5+len(msg))
	binary.BigEndian.PutUint16(p, opERROR)
	binary.BigEndian.PutUint16(p[2:], code)
	return append(append(p, msg...), 0)
}

func ackPacket(block uint16) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint16(p, opACK)
	binary.BigEndian.PutUint16(p[2:], block)
	return p
}

func parseError(p []byte) error {
	e := &Error{}
	if len(p) >= 4 {
		e.Code = binary.BigEndian.Uint16(p[2:])
		e.Message = string(bytes.TrimRight(p[4:], "\x00"))
	}
	return e
}

type option struct{ name, value string }

// request is a parsed RRQ or WRQ.
type request struct {
	op       uint16
	filename string
	mode     string
	opts     []option
}

func parseRequest(p []byte) (request, error) {
	var r request
	if len(p) < 4 {
		return r, errors.New("short packet")
	}
	r.op = binary.BigEndian.Uint16(p)
	if r.op != opRRQ && r.op != opWRQ {
		return r, errors.New("not a request")
	}
	fields := bytes.Split(p[2:], []byte{0})
	if len(fields) < 3 { // name, mode and the empty tail after the last NUL
		return r, errors.New("malformed request")
	}
	fields = fields[:len(fields)-1]
	r.filename, r.mode = string(fields[0]), strings.ToLower(string(fields[1]))
	for i := 2; i+1 < len(fields); i += 2 {
		r.opts = append(r.opts, option{strings.ToLower(string(fields[i])), string(fields[i+1])})
	}
	return r, nil
}

func buildRequest(op uint16, name string, opts []option) []byte {
	p := make([]byte, 2, 64)
	binary.BigEndian.PutUint16(p, op)
	p = append(append(p, name...), 0)
	p = append(append(p, "octet"...), 0)
	for _, o := range opts {
		p = append(append(p, o.name...), 0)
		p = append(append(p, o.value...), 0)
	}
	return p
}

func oackPacket(opts []option) []byte {
	p := make([]byte, 2, 64)
	binary.BigEndian.PutUint16(p, opOACK)
	for _, o := range opts {
		p = append(append(p, o.name...), 0)
		p = append(append(p, o.value...), 0)
	}
	return p
}

func parseOACK(p []byte) []option {
	var opts []option
	fields := bytes.Split(p[2:], []byte{0})
	for i := 0; i+1 < len(fields); i += 2 {
		opts = append(opts, option{strings.ToLower(string(fields[i])), string(fields[i+1])})
	}
	return opts
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// conn is one end of a transfer: a UDP socket talking to a single peer TID.
type conn struct {
	pc      net.PacketConn
	peer    net.Addr // nil until the peer's transfer ID is known (client side)
	host    net.Addr // client side: the server address the request went to
	blksize int
	timeout time.Duration
	retries int
	buf     []byte
}

func newConn(pc net.PacketConn, peer net.Addr, timeout time.Duration, retries int) *conn {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if retries <= 0 {
		retries = defaultRetries
	}
	return &conn{pc: pc, peer: peer, blksize: defaultBlockSize, timeout: timeout, retries: retries, buf: make([]byte, maxBlockSize+4)}
}

// read returns the next packet from the peer. The first packet received
// fixes the peer's transfer ID; packets from any other address are answered
// with an "unknown transfer ID" error and otherwise ignored.
func (c *conn) read(deadline time.Time) (op uint16, pkt []byte, err error) {
	for {
		if err := c.pc.SetReadDeadline(deadline); err != nil {
			return 0, nil, err
		}
		n, from, err := c.pc.ReadFrom(c.buf)
		if err != nil {
			return 0, nil, err
		}
		if c.peer == nil {
			if c.host != nil && !sameHost(from, c.host) {
				continue // not from the server we asked
			}
			c.peer = from
		} else if from.String() != c.peer.String() {
			c.pc.WriteTo(errorPacket(CodeUnknownTID, "unknown transfer ID"), from)
			continue
		}
		if n < 4 {
			continue
		}
		return binary.BigEndian.Uint16(c.buf), c.buf[:n], nil
	}
}

func (c *conn) fail(code uint16, msg string) {
	if c.peer != nil {
		c.pc.WriteTo(errorPacket(code, msg), c.peer)
	}
}

// exchange sends pkt to dst and waits for the acknowledgement of block,
// retransmitting on timeout only. Duplicate acknowledgements are ignored and
// never trigger a retransmission (the "Sorcerer's Apprentice" fix). An OACK
// is accepted in place of ACK 0 and returned.
func (c *conn) exchange(pkt []byte, dst net.Addr, block uint16) (oack []byte, err error) {
	for try := 0; try <= c.retries; try++ {
		if _, err := c.pc.WriteTo(pkt, dst); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(c.timeout)
		for {
			op, p, err := c.read(deadline)
			if isTimeout(err) {
				break
			}
			if err != nil {
				return nil, err
			}
			switch {
			case op == opERROR:
				return nil, parseError(p)
			case op == opACK && binary.BigEndian.Uint16(p[2:]) == block:
				return nil, nil
			case op == opOACK && block == 0:
				return append([]byte(nil), p...), nil
			}
		}
	}
	return nil, ErrTimeout
}

// send transmits r as DATA blocks starting with block 1. Block numbers wrap
// around to 0 after 65535, so files of any size can be sent.
func (c *conn) send(r io.Reader) (int64, error) {
	pkt := make([]byte, 4+c.blksize)
	binary.BigEndian.PutUint16(pkt, opDATA)
	var block uint16
	var total int64
	for {
		block++
		n, err := io.ReadFull(r, pkt[4:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			c.fail(CodeUndefined, "read error")
			return total, err
		}
		binary.BigEndian.PutUint16(pkt[2:], block)
		if _, err := c.exchange(pkt[:4+n], c.peer, block); err != nil {
			return total, err
		}
		total += int64(n)
		if n < c.blksize {
			return total, nil
		}
	}
}

// receive stores incoming DATA blocks in w. last is the packet that
// solicits the next block (the request, ACK 0 or OACK); it has already been
// sent to dst and is repeated on timeout. first, if not nil, is a DATA
// packet that was read during the handshake.
func (c *conn) receive(w io.Writer, last []byte, dst net.Addr, first []byte) (int64, error) {
	var expect uint16 = 1
	var total int64
	started := false
	tries := 0
	deadline := time.Now().Add(c.timeout)
	for {
		op, p := uint16(opDATA), first
		if first == nil {
			var err error
			op, p, err = c.read(deadline)
			if isTimeout(err) {
				if tries++; tries > c.retries {
					return total, ErrTimeout
				}
				if _, err := c.pc.WriteTo(last, dst); err != nil {
					return total, err
				}
				deadline = time.Now().Add(c.timeout)
				continue
			}
			if err != nil {
				return total, err
			}
		}
		first = nil
		if op == opERROR {
			return total, parseError(p)
		}
		if op != opDATA {
			continue
		}
		switch block := binary.BigEndian.Uint16(p[2:]); {
		case block == expect:
			data := p[4:]
			if len(data) > c.blksize {
				c.fail(CodeIllegalOp, "block too large")
				return total, errors.New("tftp: peer sent an oversized block")
			}
			if _, err := w.Write(data); err != nil {
				c.fail(CodeDiskFull, "write error")
				return total, err
			}
			total += int64(len(data))
			last, dst = ackPacket(block), c.peer
			if _, err := c.pc.WriteTo(last, dst); err != nil {
				return total, err
			}
			if len(data) < c.blksize {
				return total, nil
			}
			expect++
			started, tries = true, 0
			deadline = time.Now().Add(c.timeout)
		case started && block == expect-1:
			// Duplicate: our ACK was lost. Repeat it.
			c.pc.WriteTo(last, dst)
		}
	}
}

func atoiRange(s string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= lo && n <= hi
}

// sameHost reports whether two UDP addresses belong to the same host.
func sameHost(a, b net.Addr) bool {
	ua, ok1 := a.(*net.UDPAddr)
	ub, ok2 := b.(*net.UDPAddr)
	if !ok1 || !ok2 {
		return true
	}
	return ua.IP.Equal(ub.IP)
}
