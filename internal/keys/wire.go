package keys

import (
	"encoding/binary"
	"fmt"
	"math/big"
)

// wireReader decodes the SSH binary encoding of RFC 4251 section 5. The first
// failure is latched in err and every later read returns a zero value, so
// callers can decode a whole structure and check the error once.
type wireReader struct {
	buf []byte
	err error
}

func newWireReader(b []byte) *wireReader { return &wireReader{buf: b} }

func (r *wireReader) fail(what string) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: truncated %s", ErrMalformed, what)
	}
	r.buf = nil
}

// uint32 reads a big-endian 32-bit integer.
func (r *wireReader) uint32() uint32 {
	if r.err != nil || len(r.buf) < 4 {
		r.fail("uint32")
		return 0
	}
	v := binary.BigEndian.Uint32(r.buf)
	r.buf = r.buf[4:]
	return v
}

// str reads a length-prefixed byte string. The result aliases the input.
func (r *wireReader) str() []byte {
	n := r.uint32()
	if r.err != nil {
		return nil
	}
	if uint64(n) > uint64(len(r.buf)) {
		r.fail("string")
		return nil
	}
	s := r.buf[:n:n]
	r.buf = r.buf[n:]
	return s
}

// mpint reads a multiple-precision integer. Negative values never occur in
// key material and are rejected.
func (r *wireReader) mpint() *big.Int {
	b := r.str()
	if r.err != nil {
		return new(big.Int)
	}
	if len(b) > 0 && b[0]&0x80 != 0 {
		r.err = fmt.Errorf("%w: negative integer", ErrMalformed)
		r.buf = nil
		return new(big.Int)
	}
	return new(big.Int).SetBytes(b)
}

// rest returns the unread remainder.
func (r *wireReader) rest() []byte { return r.buf }

// done reports whether the reader consumed its input without error.
func (r *wireReader) done() bool { return r.err == nil && len(r.buf) == 0 }

// wireWriter builds SSH binary encoded data.
type wireWriter struct {
	buf []byte
}

func (w *wireWriter) uint32(v uint32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, v)
}

func (w *wireWriter) raw(b []byte) {
	w.buf = append(w.buf, b...)
}

func (w *wireWriter) str(b []byte) {
	w.uint32(uint32(len(b)))
	w.buf = append(w.buf, b...)
}

func (w *wireWriter) text(s string) {
	w.uint32(uint32(len(s)))
	w.buf = append(w.buf, s...)
}

// mpint writes n (which must not be negative) as an SSH mpint: big-endian,
// minimal length, with a leading zero byte when the top bit would otherwise
// be set.
func (w *wireWriter) mpint(n *big.Int) {
	if n == nil || n.Sign() <= 0 {
		w.uint32(0)
		return
	}
	b := n.Bytes()
	if b[0]&0x80 != 0 {
		w.uint32(uint32(len(b) + 1))
		w.buf = append(w.buf, 0)
	} else {
		w.uint32(uint32(len(b)))
	}
	w.buf = append(w.buf, b...)
	zero(b)
}

func (w *wireWriter) bytes() []byte { return w.buf }

// zero overwrites b with zeros. It is used to scrub derived keys and
// decrypted buffers once they are no longer needed.
func zero(b []byte) {
	clear(b)
}
