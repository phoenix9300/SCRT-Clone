package xfer

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"time"
)

// ZMODEM framing characters.
const (
	zpad   = '*'  // pad character, begins headers
	zdle   = 0x18 // ZMODEM data link escape (the CAN character)
	zdlee  = 0x58 // escaped ZDLE as transmitted
	zbin   = 'A'  // binary header, CRC-16
	zhex   = 'B'  // hex header, CRC-16
	zbin32 = 'C'  // binary header, CRC-32
)

// ZMODEM frame types.
const (
	zrqinit    = 0  // request receive init (sender announces itself)
	zrinit     = 1  // receive init (receiver capabilities)
	zsinit     = 2  // send init sequence (sender flags, attention string)
	zack       = 3  // acknowledgement
	zfile      = 4  // file name and attributes follow
	zskip      = 5  // receiver skips this file
	znak       = 6  // last header was garbled
	zabort     = 7  // abort batch transfer
	zfin       = 8  // finish session
	zrpos      = 9  // resume data transmission at this position
	zdata      = 10 // data subpackets follow
	zeof       = 11 // end of file
	zferr      = 12 // fatal read or write error
	zcrc       = 13 // request for / response with file CRC
	zchallenge = 14 // receiver's challenge
	zcompl     = 15 // request is complete
	zcan       = 16 // pseudo frame: other end sent CAN*5
	zfreecnt   = 17 // request for free bytes on file system
	zcommand   = 18 // command from sending program
	zstderr    = 19 // output to standard error, data follows
)

// Frame end characters, sent as ZDLE <char> after a data subpacket.
const (
	zcrce = 'h' // CRC next, frame ends, header follows
	zcrcg = 'i' // CRC next, frame continues nonstop
	zcrcq = 'j' // CRC next, frame continues, ZACK expected
	zcrcw = 'k' // CRC next, frame ends, ZACK expected
	zrub0 = 'l' // translate to 0x7f
	zrub1 = 'm' // translate to 0xff
)

// Positions of the flag bytes in a header (the position bytes ZP0..ZP3 are
// stored in transmission order, ZF0 is the last byte).
const (
	zf0 = 3
	zf1 = 2
	zf2 = 1
	zf3 = 0
	zp0 = 0
	zp1 = 1
)

// ZRINIT capability flags (ZF0).
const (
	canFDX  = 0x01 // can send and receive true full duplex
	canOVIO = 0x02 // can receive data during disk I/O
	canFC32 = 0x20 // can use 32-bit frame check
	escCtl  = 0x40 // receiver expects control characters to be escaped
)

// ZSINIT flags (ZF0).
const tescCtl = 0x40 // transmitter escapes control characters

// ZFILE conversion options (ZF0) and management options (ZF1).
const (
	zcbin   = 1 // binary transfer
	zcresum = 3 // resume interrupted file transfer
	zmclob  = 4 // replace existing file
)

// maxSubpacket is the largest data subpacket accepted from or sent to a peer.
const maxSubpacket = 8192

// gotFrameEnd flags a frame end character in the return value of zdlRead.
const gotFrameEnd = 0x100

// Recoverable receive errors.
var (
	errBadCRC    = errors.New("xfer: CRC mismatch")
	errBadEscape = errors.New("xfer: invalid ZDLE sequence")
	errTooLong   = errors.New("xfer: data subpacket too long")
)

// recoverable reports whether a receive error is a transmission error that
// the protocol's retry machinery handles, as opposed to a fatal condition.
func recoverable(err error) bool {
	return err == ErrTimeout || err == errBadCRC || err == errBadEscape || err == errTooLong || err == errBadBlock
}

// zhdr is a ZMODEM header: a frame type and four flag or position bytes.
type zhdr struct {
	typ byte
	b   [4]byte
}

// pos returns the 32-bit file position carried by the header.
func (h zhdr) pos() uint32 { return binary.LittleEndian.Uint32(h.b[:]) }

// posHdr builds a header carrying a file position (or any 32-bit value).
func posHdr(typ byte, pos uint32) zhdr {
	h := zhdr{typ: typ}
	binary.LittleEndian.PutUint32(h.b[:], pos)
	return h
}

// unwrapPos maps a 32-bit wire position to a 64-bit file position: the
// largest position not beyond ref with those low 32 bits, or the plain value
// if there is none. It lets transfers of files larger than 4 GiB work with
// peers that let the position wrap.
func unwrapPos(p uint32, ref int64) int64 {
	v := ref&^0xffffffff | int64(p)
	if v > ref {
		v -= 1 << 32
	}
	if v < 0 {
		return int64(p)
	}
	return v
}

// zmodem is the state of one ZMODEM session (either direction).
type zmodem struct {
	*session

	// Transmit side.
	esc      [256]byte // 0: send as is, 1: ZDLE-escape, 2: escape after '@', 3: ZRUB
	lastTx   byte      // last byte put on the wire, for the CR-after-@ rule
	use32    bool      // send binary headers and their data with CRC-32
	txData32 bool      // data subpackets after the last header sent use CRC-32
	out      []byte    // transmit scratch buffer

	// Receive side.
	rxCtl    bool   // discard unescaped control characters
	rxData32 bool   // data subpackets after the last header received use CRC-32
	rxbuf    []byte // receive scratch buffer
	attn     []byte // sender's attention sequence (receiver side)

	// Peer capabilities (sender side, from ZRINIT).
	rxFlags  byte
	rxBufLen int // receiver's buffer size, 0 = can stream
}

func newZmodem(x *session) *zmodem {
	z := &zmodem{session: x}
	z.setTxEscape(x.o.EscapeControl)
	return z
}

// setTxEscape builds the transmit escape table. ZDLE, DLE, XON and XOFF
// (with or without the high bit) are always escaped, CR only after '@'
// (protects against the "@ CR" escape of some packet networks); with ctl set
// every control character including DEL and 0xff is escaped.
func (z *zmodem) setTxEscape(ctl bool) {
	for i := range z.esc {
		c := byte(i)
		var e byte
		switch {
		case c == 0x7f || c == 0xff:
			if ctl {
				e = 3
			}
		case c&0x60 != 0:
		case c == zdle, c == 0x10, c == 0x90, c == xon, c == xon|0x80, c == xoff, c == xoff|0x80:
			e = 1
		case c == '\r' || c == '\r'|0x80:
			e = 2
			if ctl {
				e = 1
			}
		default:
			if ctl {
				e = 1
			}
		}
		z.esc[i] = e
	}
}

// putEsc appends c to the transmit buffer with ZDLE escaping applied.
func (z *zmodem) putEsc(c byte) {
	e := z.esc[c]
	if e != 0 {
		switch e {
		case 2:
			if z.lastTx&0x7f != '@' {
				break
			}
			fallthrough
		case 1:
			z.out = append(z.out, zdle)
			c ^= 0x40
		case 3:
			z.out = append(z.out, zdle)
			if c == 0x7f {
				c = zrub0
			} else {
				c = zrub1
			}
		}
	}
	z.out = append(z.out, c)
	z.lastTx = c
}

const hexDigits = "0123456789abcdef"

// sendHex transmits a hex header. Hex headers are used for everything the
// receiver sends and for the sender's ZRQINIT and ZFIN; they survive 7-bit
// and control-character-hostile links.
func (z *zmodem) sendHex(h zhdr) error {
	raw := [5]byte{h.typ, h.b[0], h.b[1], h.b[2], h.b[3]}
	crc := crc16(raw[:])
	b := append(z.out[:0], zpad, zpad, zdle, zhex)
	for _, c := range raw {
		b = append(b, hexDigits[c>>4], hexDigits[c&15])
	}
	b = append(b, hexDigits[crc>>12], hexDigits[crc>>8&15], hexDigits[crc>>4&15], hexDigits[crc&15])
	// CR and LF (the latter traditionally with the high bit set) make the
	// header readable; XON releases a peer that was flow-controlled by line
	// noise. ZACK and ZFIN carry no XON.
	b = append(b, '\r', '\n'|0x80)
	if h.typ != zfin && h.typ != zack {
		b = append(b, xon)
	}
	z.out = b
	z.txData32 = false
	z.lastTx = 0
	return z.write(b)
}

// sendBin transmits a binary header, with CRC-32 if the receiver supports it.
func (z *zmodem) sendBin(h zhdr) error {
	raw := [5]byte{h.typ, h.b[0], h.b[1], h.b[2], h.b[3]}
	z.lastTx = 0
	if z.use32 {
		z.out = append(z.out[:0], zpad, zdle, zbin32)
		for _, c := range raw {
			z.putEsc(c)
		}
		crc := crc32.ChecksumIEEE(raw[:])
		for range 4 {
			z.putEsc(byte(crc))
			crc >>= 8
		}
	} else {
		z.out = append(z.out[:0], zpad, zdle, zbin)
		for _, c := range raw {
			z.putEsc(c)
		}
		crc := crc16(raw[:])
		z.putEsc(byte(crc >> 8))
		z.putEsc(byte(crc))
	}
	z.txData32 = z.use32
	return z.write(z.out)
}

// sendData transmits one data subpacket terminated by the given frame end.
func (z *zmodem) sendData(data []byte, frameEnd byte) error {
	z.out = z.out[:0]
	for _, c := range data {
		z.putEsc(c)
	}
	z.out = append(z.out, zdle, frameEnd)
	z.lastTx = frameEnd
	if z.txData32 {
		crc := crc32Frame(data, frameEnd)
		for range 4 {
			z.putEsc(byte(crc))
			crc >>= 8
		}
	} else {
		crc := crc16Frame(data, frameEnd)
		z.putEsc(byte(crc >> 8))
		z.putEsc(byte(crc))
	}
	if frameEnd == zcrcw {
		z.out = append(z.out, xon)
	}
	return z.write(z.out)
}

// sendPos transmits a hex header carrying a file position.
func (z *zmodem) sendPos(typ byte, pos int64) error {
	return z.sendHex(posHdr(typ, uint32(pos)))
}

// zdlRead returns the next logical byte of a binary header or data
// subpacket, undoing ZDLE escaping. A frame end sequence is returned as
// gotFrameEnd|char. XON and XOFF are always discarded; other unescaped
// control characters are discarded when control escaping is in force. Five
// consecutive CAN characters cancel the session.
func (z *zmodem) zdlRead(d time.Duration) (int, error) {
	r := z.rd
	for {
		c, err := r.get(d)
		if err != nil {
			return 0, err
		}
		if c&0x60 != 0 {
			return int(c), nil
		}
		switch c {
		case zdle:
			return z.zdlEscape(d)
		case xon, xon | 0x80, xoff, xoff | 0x80:
			continue
		}
		if z.rxCtl {
			continue
		}
		return int(c), nil
	}
}

// zdlEscape decodes what follows a ZDLE.
func (z *zmodem) zdlEscape(d time.Duration) (int, error) {
	r := z.rd
	cans := 1
	for {
		c, err := r.get(d)
		if err != nil {
			return 0, err
		}
		switch c {
		case zdle:
			if cans++; cans >= 5 {
				return 0, ErrCanceled
			}
			continue
		case zcrce, zcrcg, zcrcq, zcrcw:
			return gotFrameEnd | int(c), nil
		case zrub0:
			return 0x7f, nil
		case zrub1:
			return 0xff, nil
		case xon, xon | 0x80, xoff, xoff | 0x80:
			continue
		}
		if c&0x60 == 0x40 {
			return int(c ^ 0x40), nil
		}
		if z.rxCtl && c&0x60 == 0 {
			continue
		}
		return 0, errBadEscape
	}
}

// readData reads one data subpacket of at most limit bytes and verifies its
// CRC. The returned slice is valid until the next call.
func (z *zmodem) readData(limit int) (data []byte, frameEnd byte, err error) {
	r := z.rd
	d := z.o.Timeout
	buf := z.rxbuf[:0]
	var c int
	for {
		// Fast path: plain bytes straight from the reader's buffer.
		if r.pos < len(r.cur) && r.cur[r.pos]&0x60 != 0 {
			c = int(r.cur[r.pos])
			r.pos++
		} else if c, err = z.zdlRead(d); err != nil {
			z.rxbuf = buf
			return nil, 0, err
		}
		if c&gotFrameEnd != 0 {
			break
		}
		if len(buf) >= limit {
			z.rxbuf = buf
			return nil, 0, errTooLong
		}
		buf = append(buf, byte(c))
	}
	z.rxbuf = buf
	frameEnd = byte(c)
	if z.rxData32 {
		var got uint32
		for i := range 4 {
			c, err := z.zdlRead(d)
			if err != nil {
				return nil, 0, err
			}
			if c&gotFrameEnd != 0 {
				return nil, 0, errBadCRC
			}
			got |= uint32(c) << (8 * i)
		}
		if got != crc32Frame(buf, frameEnd) {
			return nil, 0, errBadCRC
		}
	} else {
		var got uint16
		for range 2 {
			c, err := z.zdlRead(d)
			if err != nil {
				return nil, 0, err
			}
			if c&gotFrameEnd != 0 {
				return nil, 0, errBadCRC
			}
			got = got<<8 | uint16(c)
		}
		if got != crc16Frame(buf, frameEnd) {
			return nil, 0, errBadCRC
		}
	}
	return buf, frameEnd, nil
}

// readHeader scans the input for the next valid header, discarding anything
// else (terminal noise, stale data subpackets, damaged headers). It returns
// ErrTimeout if no valid header arrives within the timeout and ErrCanceled
// when five consecutive CAN characters are seen.
func (z *zmodem) readHeader(timeout time.Duration) (zhdr, error) {
	r := z.rd
	deadline := time.Now().Add(timeout)
	const (
		idle    = iota // looking for ZPAD
		gotPad         // seen ZPAD
		gotZdle        // seen ZPAD ZDLE
	)
	state, cans := idle, 0
	for n := 0; ; n++ {
		if n&0x3ff == 0x3ff && time.Now().After(deadline) {
			// Garbage keeps flowing but no header shows up.
			return zhdr{}, ErrTimeout
		}
		c, err := r.getUntil(deadline)
		if err != nil {
			return zhdr{}, err
		}
		if c == can {
			if cans++; cans >= 5 {
				return zhdr{}, ErrCanceled
			}
		} else {
			cans = 0
		}
		switch state {
		case idle:
			if c == zpad || c == zpad|0x80 {
				state = gotPad
			}
		case gotPad:
			switch c {
			case zpad, zpad | 0x80:
			case zdle:
				state = gotZdle
			default:
				state = idle
			}
		case gotZdle:
			state = idle
			var h zhdr
			var ok bool
			switch c {
			case zbin:
				h, ok, err = z.readBinHeader(deadline, false)
			case zbin32:
				h, ok, err = z.readBinHeader(deadline, true)
			case zhex:
				h, ok, err = z.readHexHeader(deadline)
			default:
				continue
			}
			if err != nil {
				return zhdr{}, err
			}
			if ok {
				return h, nil
			}
			cans = 0
		}
	}
}

// readBinHeader reads the body of a binary header. ok is false if the header
// is damaged; err is set only for fatal conditions.
func (z *zmodem) readBinHeader(deadline time.Time, crc32bit bool) (h zhdr, ok bool, err error) {
	n := 7
	if crc32bit {
		n = 9
	}
	var raw [9]byte
	for i := range n {
		c, err := z.zdlRead(time.Until(deadline))
		if err != nil {
			if recoverable(err) {
				err = nil
			}
			return h, false, err
		}
		if c&gotFrameEnd != 0 {
			return h, false, nil
		}
		raw[i] = byte(c)
	}
	if crc32bit {
		if crc32.ChecksumIEEE(raw[:5]) != binary.LittleEndian.Uint32(raw[5:9]) {
			return h, false, nil
		}
	} else if crc16(raw[:5]) != binary.BigEndian.Uint16(raw[5:7]) {
		return h, false, nil
	}
	h.typ = raw[0]
	copy(h.b[:], raw[1:5])
	z.rxData32 = crc32bit
	return h, true, nil
}

// readHexHeader reads the body of a hex header and its trailing CR LF.
func (z *zmodem) readHexHeader(deadline time.Time) (h zhdr, ok bool, err error) {
	r := z.rd
	var raw [7]byte
	for i := range raw {
		var v byte
		for range 2 {
			c, err := r.getUntil(deadline)
			if err != nil {
				if recoverable(err) {
					err = nil
				}
				return h, false, err
			}
			c &= 0x7f
			switch {
			case c >= '0' && c <= '9':
				c -= '0'
			case c >= 'a' && c <= 'f':
				c -= 'a' - 10
			case c >= 'A' && c <= 'F':
				c -= 'A' - 10
			default:
				r.unget()
				return h, false, nil
			}
			v = v<<4 | c
		}
		raw[i] = v
	}
	if crc16(raw[:5]) != binary.BigEndian.Uint16(raw[5:7]) {
		return h, false, nil
	}
	h.typ = raw[0]
	copy(h.b[:], raw[1:5])
	z.rxData32 = false

	// The header is followed by CR LF (LF usually with the high bit set) and,
	// for most frame types, XON. Consume them so that they neither disturb
	// the protocol nor leak into the terminal after the final header.
	const trailerWait = 100 * time.Millisecond
	c, err := r.get(trailerWait)
	if err != nil {
		return h, true, nil
	}
	switch c {
	case '\r', '\r' | 0x80:
		if c, err = r.get(trailerWait); err != nil {
			return h, true, nil
		}
		if c != '\n' && c != '\n'|0x80 {
			r.unget()
			return h, true, nil
		}
	case '\n', '\n' | 0x80:
	default:
		r.unget()
		return h, true, nil
	}
	if h.typ != zack && h.typ != zfin {
		// Take the XON only if it is already here; a late one is discarded
		// by the header scanner or the data reader.
		if c, err = r.get(0); err == nil && c != xon {
			r.unget()
		}
	}
	return h, true, nil
}

// sendAttn transmits the sender's attention sequence, which interrupts a
// sender that cannot watch the reverse channel while streaming. The code
// 0xDD requests a line break (not available on a byte stream, ignored) and
// 0xDE a one second pause.
func (z *zmodem) sendAttn() error {
	if len(z.attn) == 0 {
		return nil
	}
	start := 0
	for i, c := range z.attn {
		if c != 0xdd && c != 0xde {
			continue
		}
		if err := z.write(z.attn[start:i]); err != nil {
			return err
		}
		start = i + 1
		if c == 0xde {
			t := time.NewTimer(time.Second)
			select {
			case <-t.C:
			case <-z.ctx.Done():
				t.Stop()
				return z.ctx.Err()
			}
		}
	}
	return z.write(z.attn[start:])
}
