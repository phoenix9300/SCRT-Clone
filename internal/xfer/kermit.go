package xfer

// Kermit file transfer protocol (sender and receiver), implemented from the
// public protocol description (Frank da Cruz, "Kermit Protocol Manual" and
// "Kermit, A File Transfer Protocol").
//
// Supported: the basic packet format and the long-packet extension, packet
// types S F A D Z B Y N E, the three block check types, control-character
// prefixing, eighth-bit prefixing (only when the peer asks for it), repeat
// counts, file attributes (length and date), batch transfers and
// retransmission. Not supported: sliding windows, streaming, text-mode
// conversion, locking shifts, server commands.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	kMark        = 0x01 // MARK: start of packet
	kMaxRegular  = 94   // largest value of a regular packet's LEN field
	kMaxLongWire = 9024 // largest extended length (95*95-1)
	kDefaultLong = 4096 // default and ceiling of the long packet data size

	kCapLong = 2 // CAPAS: long packets
	kCapAttr = 8 // CAPAS: attribute packets
)

// kermitCfg holds protocol knobs that are not part of Options. The zero value
// gives the defaults of KermitSend and KermitReceive; tests use the others.
type kermitCfg struct {
	maxl    int  // MAXL announced (regular packet LEN limit); default 94
	chkt    byte // block check proposed: '1', '2' or '3'; default '3'
	longMax int  // MAXLX announced; 0 = 4096, <0 disables long packets
	qbin    byte // QBIN announced: 'Y' (default, accept if asked), 'N' or a prefix
	rept    byte // REPT announced: '~' (default) or ' ' for none
	noAttr  bool // do not use attribute packets
}

func ktoc(x int) byte  { return byte(x + 32) }
func kunc(c byte) int  { return int(c) - 32 }
func kctl(c byte) byte { return c ^ 64 }

// errKBad reports a damaged packet (recoverable).
var errKBad = errors.New("kermit: damaged packet")

// kCRCTable is the table for the CRC-16 used by block check type 3: the
// reflected polynomial 0x8408 (CCITT), initial value 0, no final inversion.
var kCRCTable = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i)
		for range 8 {
			if c&1 != 0 {
				c = c>>1 ^ 0x8408
			} else {
				c >>= 1
			}
		}
		t[i] = c
	}
	return
}()

// kAppendCheck appends the block check of the given type for b.
func kAppendCheck(dst []byte, chk int, b []byte) []byte {
	switch chk {
	case 2:
		s := 0
		for _, c := range b {
			s += int(c)
		}
		s &= 0xfff
		return append(dst, ktoc(s>>6), ktoc(s&63))
	case 3:
		var crc uint16
		for _, c := range b {
			crc = crc>>8 ^ kCRCTable[byte(crc)^c]
		}
		return append(dst, ktoc(int(crc>>12)&15), ktoc(int(crc>>6)&63), ktoc(int(crc)&63))
	}
	s := 0
	for _, c := range b {
		s += int(c)
	}
	return append(dst, ktoc((s+(s&192)>>6)&63))
}

// kparams are the parameters exchanged in S and Y(S) packets.
type kparams struct {
	maxl, time, npad     int
	padc, eol            byte
	qctl, qbin, chkt, rp byte
	capas                int
	maxlong              int
}

func kvalidPrefix(c byte) bool { return (c >= 33 && c <= 62) || (c >= 96 && c <= 126) }

func (p kparams) bytes() []byte {
	return []byte{
		ktoc(p.maxl), ktoc(p.time), ktoc(0), kctl(0), ktoc(int(p.eol)),
		p.qctl, p.qbin, p.chkt, p.rp, ktoc(p.capas), ktoc(0),
		ktoc(p.maxlong / 95), ktoc(p.maxlong % 95),
	}
}

func kparseParams(d []byte) kparams {
	p := kparams{maxl: 80, time: 5, eol: 13, qctl: '#', qbin: 'N', chkt: '1', rp: ' '}
	at := func(i int) (byte, bool) {
		if i < len(d) {
			return d[i], true
		}
		return 0, false
	}
	if c, ok := at(0); ok {
		if v := kunc(c); v >= 10 && v <= kMaxRegular {
			p.maxl = v
		}
	}
	if c, ok := at(1); ok {
		p.time = max(kunc(c), 0)
	}
	if c, ok := at(2); ok {
		p.npad = min(max(kunc(c), 0), 90)
	}
	if c, ok := at(3); ok {
		p.padc = kctl(c)
	}
	if c, ok := at(4); ok {
		if v := kunc(c); v > 0 && v < 32 {
			p.eol = byte(v)
		}
	}
	if c, ok := at(5); ok && kvalidPrefix(c) {
		p.qctl = c
	}
	if c, ok := at(6); ok {
		p.qbin = c
	}
	if c, ok := at(7); ok {
		p.chkt = c
	}
	if c, ok := at(8); ok {
		p.rp = c
	}
	i := 9
	for {
		c, ok := at(i)
		if !ok {
			return p
		}
		v := kunc(c)
		if i == 9 {
			p.capas = max(v, 0)
		}
		i++
		if v < 0 || v&1 == 0 {
			break
		}
	}
	i++ // WINDO
	if c1, ok := at(i); ok {
		if c2, ok := at(i + 1); ok {
			if a, b := kunc(c1), kunc(c2); a >= 0 && b >= 0 {
				p.maxlong = a*95 + b
			}
		}
	}
	return p
}

// kermit is the state of one transfer.
type kermit struct {
	*session
	cfg  kermitCfg
	own  kparams
	peer kparams

	chk      int  // block check type in use for everything but S and Y(S)
	qctlIn   byte // prefix the peer uses for control characters
	qctlOut  byte // prefix we use
	qbin     byte // eighth-bit prefix, 0 = none
	rept     byte // repeat prefix, 0 = none
	sendMax  int  // LEN limit for packets we send
	sendLong int  // data+check limit of long packets we send; 0 = none
	npad     int
	padc     byte
	eol      byte
	attr     bool // peer accepts attribute packets

	seq     byte   // sender: sequence number of the packet in flight; receiver: expected
	lastAck []byte // receiver: last acknowledgement, for duplicates
}

func newKermit(ctx context.Context, s Stream, o Options, cfg kermitCfg) *kermit {
	k := &kermit{session: newSession(ctx, s, o), cfg: cfg}
	p := kparams{
		maxl: kMaxRegular, eol: 13, qctl: '#', qbin: 'Y', chkt: '3', rp: '~',
		capas: kCapLong | kCapAttr, maxlong: kDefaultLong,
	}
	if cfg.maxl >= 20 && cfg.maxl < kMaxRegular {
		p.maxl = cfg.maxl
	}
	secs := int((k.o.Timeout + 500*time.Millisecond) / time.Second)
	p.time = min(max(secs, 1), kMaxRegular)
	if cfg.chkt >= '1' && cfg.chkt <= '3' {
		p.chkt = cfg.chkt
	}
	if cfg.qbin != 0 {
		p.qbin = cfg.qbin
	}
	if cfg.rept != 0 {
		p.rp = cfg.rept
	}
	if cfg.noAttr {
		p.capas &^= kCapAttr
	}
	switch {
	case cfg.longMax < 0:
		p.capas &^= kCapLong
		p.maxlong = 0
	case cfg.longMax > 0:
		p.maxlong = min(cfg.longMax, kDefaultLong)
	}
	k.own = p
	k.peer = kparseParams(nil)
	k.chk = 1
	k.qctlIn, k.qctlOut = '#', '#'
	k.sendMax = 80
	k.eol = 13
	return k
}

// negotiate derives the working parameters from both parameter sets.
func (k *kermit) negotiate() {
	own, peer := k.own, k.peer
	k.sendMax = peer.maxl
	k.npad, k.padc, k.eol = peer.npad, peer.padc, peer.eol
	k.qctlOut, k.qctlIn = own.qctl, peer.qctl
	k.qbin = 0
	switch {
	case kvalidPrefix(peer.qbin) && own.qbin == 'Y':
		k.qbin = peer.qbin
	case kvalidPrefix(own.qbin) && (peer.qbin == 'Y' || peer.qbin == own.qbin):
		k.qbin = own.qbin
	}
	if k.qbin == k.qctlOut || k.qbin == k.qctlIn {
		k.qbin = 0
	}
	k.rept = 0
	if own.rp == peer.rp && kvalidPrefix(own.rp) && own.rp != k.qctlOut && own.rp != k.qctlIn && own.rp != k.qbin {
		k.rept = own.rp
	}
	k.sendLong = 0
	if own.capas&kCapLong != 0 && peer.capas&kCapLong != 0 {
		n := peer.maxlong
		if n <= 0 {
			n = 500
		}
		k.sendLong = min(n, kDefaultLong)
	}
	k.attr = peer.capas&kCapAttr != 0 && !k.cfg.noAttr
}

// unit appends the encoding of one data byte.
func (k *kermit) unit(dst []byte, b byte) []byte {
	c := b
	if k.qbin != 0 && b&0x80 != 0 {
		dst = append(dst, k.qbin)
		c = b & 0x7f
	}
	switch {
	case c < 32 || c == 127:
		dst = append(dst, k.qctlOut, c^64)
	case c == k.qctlOut || (k.qbin != 0 && c == k.qbin) || (k.rept != 0 && c == k.rept):
		dst = append(dst, k.qctlOut, c)
	default:
		dst = append(dst, c)
	}
	return dst
}

// encodeBlock encodes as much of src as fits into capacity bytes.
func (k *kermit) encodeBlock(src []byte, capacity int, noRepeat bool) (out []byte, used int) {
	out = make([]byte, 0, capacity)
	var tmp [8]byte
	for used < len(src) {
		b := src[used]
		r := 1
		if k.rept != 0 && !noRepeat {
			for used+r < len(src) && src[used+r] == b && r < kMaxRegular {
				r++
			}
		}
		u := k.unit(tmp[:0], b)
		if r >= 4 && len(out)+2+len(u) <= capacity {
			out = append(out, k.rept, ktoc(r))
			out = append(out, u...)
			used += r
			continue
		}
		if len(out)+len(u) > capacity {
			break
		}
		out = append(out, u...)
		used++
	}
	return out, used
}

// decode reverses encodeBlock for data sent by the peer.
func (k *kermit) decode(dst, d []byte) ([]byte, error) {
	bad := fmt.Errorf("%w: truncated data field", ErrProtocol)
	for i := 0; i < len(d); {
		rep := 1
		c := d[i]
		i++
		if k.rept != 0 && c == k.rept {
			if i+1 >= len(d) {
				return dst, bad
			}
			if rep = kunc(d[i]); rep < 0 {
				return dst, fmt.Errorf("%w: bad repeat count", ErrProtocol)
			}
			c = d[i+1]
			i += 2
		}
		hi := false
		if k.qbin != 0 && c == k.qbin {
			if i >= len(d) {
				return dst, bad
			}
			hi = true
			c = d[i]
			i++
		}
		if c == k.qctlIn {
			if i >= len(d) {
				return dst, bad
			}
			c = d[i]
			i++
			if c7 := c & 0x7f; c7 != k.qctlIn && (k.qbin == 0 || c7 != k.qbin) && (k.rept == 0 || c7 != k.rept) && c7 >= 63 && c7 <= 95 {
				c ^= 64
			}
		}
		if hi {
			c |= 0x80
		}
		for range rep {
			dst = append(dst, c)
		}
	}
	return dst, nil
}

// build assembles a packet ready for the wire.
func (k *kermit) build(seq, typ byte, data []byte, chk int, long bool) []byte {
	b := make([]byte, 0, len(data)+16+k.npad)
	if !long {
		b = append(b, ktoc(len(data)+chk+2), ktoc(int(seq)), typ)
	} else {
		n := len(data) + chk
		b = append(b, ktoc(0), ktoc(int(seq)), typ, ktoc(n/95), ktoc(n%95))
		b = kAppendCheck(b, 1, b)
	}
	b = append(b, data...)
	b = kAppendCheck(b, chk, b)
	out := make([]byte, 0, len(b)+k.npad+2)
	for range k.npad {
		out = append(out, k.padc)
	}
	out = append(out, kMark)
	out = append(out, b...)
	return append(out, k.eol)
}

type kpkt struct {
	seq, typ byte
	data     []byte
}

// readPacket reads the next packet. Bytes before MARK are skipped. A
// damaged packet yields errKBad, silence ErrTimeout.
func (k *kermit) readPacket(deadline time.Time) (kpkt, error) {
	for {
		c, err := k.rd.getUntil(deadline)
		if err != nil {
			return kpkt{}, err
		}
		if c == kMark {
			break
		}
	}
	next := func() (byte, error) {
		c, err := k.rd.get(k.o.Timeout)
		if err != nil {
			return 0, err
		}
		if c == kMark {
			k.rd.unget()
			return 0, errKBad
		}
		return c, nil
	}
	raw := make([]byte, 0, 128)
	for range 3 {
		c, err := next()
		if err != nil {
			return kpkt{}, err
		}
		raw = append(raw, c)
	}
	l, seq, typ := kunc(raw[0]), kunc(raw[1]), raw[2]
	if seq < 0 || seq > 63 {
		return kpkt{}, errKBad
	}
	chk := k.chk
	if typ == 'S' {
		chk = 1
	}
	hdr, rest := 3, 0
	if l == 0 {
		for range 3 {
			c, err := next()
			if err != nil {
				return kpkt{}, err
			}
			raw = append(raw, c)
		}
		if hc := kAppendCheck(nil, 1, raw[:5]); hc[0] != raw[5] {
			return kpkt{}, errKBad
		}
		x1, x2 := kunc(raw[3]), kunc(raw[4])
		if x1 < 0 || x1 > 94 || x2 < 0 || x2 > 94 {
			return kpkt{}, errKBad
		}
		n := x1*95 + x2
		if n < chk || n > kMaxLongWire {
			return kpkt{}, errKBad
		}
		hdr, rest = 6, n
	} else {
		// C-Kermit occasionally sends LEN 95 (DEL) although MAXL is 94.
		if l < chk+2 || l > kMaxRegular+1 {
			return kpkt{}, errKBad
		}
		rest = l - 2
	}
	for range rest {
		c, err := next()
		if err != nil {
			return kpkt{}, err
		}
		raw = append(raw, c)
	}
	body := raw[:len(raw)-chk]
	if want := kAppendCheck(nil, chk, body); string(want) != string(raw[len(raw)-chk:]) {
		return kpkt{}, errKBad
	}
	k.eatEOL(0)
	return kpkt{seq: byte(seq), typ: typ, data: append([]byte(nil), body[hdr:]...)}, nil
}

// eatEOL consumes the packet terminator if it has arrived (waiting up to d).
func (k *kermit) eatEOL(d time.Duration) {
	if d <= 0 && !k.rd.buffered() {
		return
	}
	c, err := k.rd.get(d)
	if err != nil {
		return
	}
	if c != '\r' && c != '\n' {
		k.rd.unget()
	}
}

func kerrText(typ byte, d []byte, k *kermit) string {
	out, err := k.decode(nil, d)
	if err != nil {
		out = d
	}
	s := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '?'
		}
		return r
	}, string(out))
	return s
}

// exchange sends one packet and waits for its acknowledgement.
func (k *kermit) exchange(typ byte, data []byte) (kpkt, error) {
	return k.exchangeN(typ, data, k.o.Retries)
}

func (k *kermit) exchangeN(typ byte, data []byte, retries int) (kpkt, error) {
	chk := k.chk
	if typ == 'S' {
		chk = 1
	}
	long := len(data)+chk+2 > kMaxRegular || len(data)+chk+2 > k.sendMax
	if long && (k.sendLong == 0 || len(data)+chk > k.sendLong) {
		return kpkt{}, fmt.Errorf("%w: packet data too long", ErrProtocol)
	}
	pkt := k.build(k.seq, typ, data, chk, long)
	lastErr := ErrTimeout
	for attempt := 0; attempt <= retries; attempt++ {
		if err := k.write(pkt); err != nil {
			return kpkt{}, err
		}
		deadline := time.Now().Add(k.o.Timeout)
	wait:
		for {
			p, err := k.readPacket(deadline)
			switch {
			case err == nil:
			case errors.Is(err, errKBad):
				lastErr = ErrTooManyErrors
				break wait
			case errors.Is(err, ErrTimeout):
				lastErr = ErrTimeout
				break wait
			default:
				return kpkt{}, err
			}
			switch p.typ {
			case 'Y':
				if p.seq == k.seq {
					k.seq = (k.seq + 1) & 63
					return p, nil
				}
			case 'N':
				if p.seq == k.seq {
					lastErr = ErrTooManyErrors
					break wait
				}
				if p.seq == (k.seq+1)&63 {
					// A NAK for the next packet acknowledges this one.
					k.seq = (k.seq + 1) & 63
					return kpkt{seq: p.seq, typ: 'Y'}, nil
				}
			case 'E':
				return kpkt{}, fmt.Errorf("%w: %s", ErrCanceled, kerrText('E', p.data, k))
			}
		}
	}
	return kpkt{}, lastErr
}

// finish ends the transfer: it tells the peer if we gave up, emits the final
// event and hands unconsumed bytes back.
func (k *kermit) finish(err error) error {
	if err != nil {
		cancelled := false
		if cerr := k.ctx.Err(); cerr != nil {
			err, cancelled = cerr, true
		}
		if !errors.Is(err, ErrCanceled) && !k.streamFailed() {
			msg := "Transfer cancelled"
			if !cancelled {
				msg = err.Error()
			}
			if len(msg) > 60 {
				msg = msg[:60]
			}
			msg = strings.Map(func(r rune) rune {
				if r < 32 || r > 126 || r == '#' || r == '~' || r == '&' {
					return '?'
				}
				return r
			}, msg)
			_, _ = k.s.Write(k.build(k.seq, 'E', []byte(msg), k.chk, false))
		}
		k.emit(Event{Kind: Error, File: k.cur, Index: k.idx, Err: err})
	} else {
		k.emit(Event{Kind: Done, Index: k.idx})
	}
	if left := k.rd.close(); len(left) > 0 {
		if k.o.Leftover != nil {
			k.o.Leftover(left)
		} else if u, ok := k.s.(Unreader); ok {
			u.Unread(left)
		}
	}
	return err
}

// kermitWireName converts a file name to the conservative form sent to the
// peer: base name only, letters, digits and . - _ + kept, the rest replaced
// by '_', at most max bytes long (the extension is preserved).
func kermitWireName(name string, max int) string {
	b := []byte(wireName(name))
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '+':
		default:
			b[i] = '_'
		}
	}
	if len(b) > max {
		ext := ""
		if i := strings.LastIndexByte(string(b), '.'); i > 0 && len(b)-i <= 8 {
			ext = string(b[i:])
		}
		b = append(b[:max-len(ext)], ext...)
	}
	return string(b)
}

// kAttrs builds the data field of an A packet.
func kAttrs(info FileInfo, size int64) []byte {
	var b []byte
	add := func(tag byte, v string) {
		b = append(b, tag, ktoc(len(v)))
		b = append(b, v...)
	}
	add('"', "B8")
	if size >= 0 {
		add('1', strconv.FormatInt(size, 10))
		add('!', strconv.FormatInt((size+1023)/1024, 10))
	}
	if !info.ModTime.IsZero() {
		add('#', info.ModTime.UTC().Format("20060102 15:04:05"))
	}
	return b
}

// kParseAttrs applies the attributes of an A packet to info.
func kParseAttrs(d []byte, info *FileInfo) {
	for len(d) >= 2 {
		tag, n := d[0], kunc(d[1])
		if n < 0 || 2+n > len(d) {
			return
		}
		v := string(d[2 : 2+n])
		d = d[2+n:]
		switch tag {
		case '1':
			if s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && s >= 0 {
				info.Size = s
			}
		case '#':
			v = strings.TrimSpace(v)
			if i := strings.IndexByte(v, ' '); i > 8 {
				v = v[:i]
			}
			for _, layout := range []string{"20060102 15:04:05", "20060102 15:04", "20060102"} {
				if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
					info.ModTime = t
					break
				}
			}
		}
	}
}

// kbuf reads a file ahead so that runs can be detected across packets.
type kbuf struct {
	r   io.Reader
	b   []byte
	pos int
	eof bool
}

func (b *kbuf) window() ([]byte, error) {
	if !b.eof && len(b.b)-b.pos < 8192 {
		n := copy(b.b, b.b[b.pos:])
		b.b, b.pos = b.b[:n], 0
		if cap(b.b) < 32768 {
			nb := make([]byte, n, 32768)
			copy(nb, b.b)
			b.b = nb
		}
		for len(b.b) < 16384 {
			m, err := b.r.Read(b.b[len(b.b):cap(b.b)][:16384-len(b.b)])
			b.b = b.b[:len(b.b)+m]
			if err == io.EOF {
				b.eof = true
				break
			}
			if err != nil {
				return nil, err
			}
		}
	}
	return b.b[b.pos:], nil
}

// KermitSend sends files with the Kermit protocol (the counterpart of a
// remote "kermit -r" / "gkermit -r"). Files are sent in binary mode. The
// receiver may skip a file or cancel the batch; cancelling returns an error
// that wraps ErrCanceled after the batch has been closed properly.
func KermitSend(ctx context.Context, s Stream, files []SendFile, o Options) error {
	return kermitSend(ctx, s, files, o, kermitCfg{})
}

func kermitSend(ctx context.Context, s Stream, files []SendFile, o Options, cfg kermitCfg) error {
	k := newKermit(ctx, s, o, cfg)
	return k.finish(k.send(files))
}

func (k *kermit) send(files []SendFile) error {
	ack, err := k.exchange('S', k.own.bytes())
	if err != nil {
		return err
	}
	k.peer = kparseParams(ack.data)
	k.negotiate()
	k.chk = 1
	if k.peer.chkt == k.own.chkt {
		k.chk = int(k.own.chkt - '0')
	}
	cancelAll := false
	for i, f := range files {
		if cancelAll = cancelAll || k.ctx.Err() != nil; cancelAll {
			break
		}
		if cancelAll, err = k.sendOne(i, f); err != nil {
			return err
		}
	}
	k.cur = FileInfo{}
	// All data has been acknowledged; the final B is not worth failing for
	// when its acknowledgement is lost.
	if _, err := k.exchangeN('B', nil, min(k.o.Retries, 2)); err != nil {
		if !errors.Is(err, ErrTimeout) && !errors.Is(err, ErrTooManyErrors) {
			return err
		}
	}
	k.eatEOL(50 * time.Millisecond)
	if cancelAll {
		return fmt.Errorf("%w: receiver cancelled the batch", ErrCanceled)
	}
	return k.ctx.Err()
}

// kAckAction interprets the data of an acknowledgement: X skips the file, Z
// cancels the batch (and N refuses a file in reply to an attribute packet).
func kAckAction(d []byte, attr bool) (skip, all bool) {
	if len(d) == 1 {
		switch d[0] {
		case 'X':
			return true, false
		case 'Z':
			return true, true
		case 'N':
			return attr, false
		}
	}
	return false, false
}

func (k *kermit) sendOne(idx int, f SendFile) (cancelAll bool, err error) {
	info := f.Info
	rc, err := f.Open()
	if err != nil {
		return false, fmt.Errorf("xfer: %s: %w", info.Name, err)
	}
	defer rc.Close()
	if info.Size < 0 {
		n, err := rc.Seek(0, io.SeekEnd)
		if err == nil {
			_, err = rc.Seek(0, io.SeekStart)
		}
		if err != nil {
			return false, fmt.Errorf("xfer: %s: %w", info.Name, err)
		}
		info.Size = n
	}
	k.cur, k.idx = info, idx

	nameCap := min(k.sendMax, kMaxRegular) - k.chk - 2
	name, _ := k.encodeBlock([]byte(kermitWireName(info.Name, 64)), nameCap, true)
	skip := false
	ack, err := k.exchange('F', name)
	if err != nil {
		return false, err
	}
	skip, cancelAll = kAckAction(ack.data, false)
	if !skip && k.attr {
		if ad := kAttrs(info, info.Size); len(ad)+k.chk+2 <= min(k.sendMax, kMaxRegular) {
			if ack, err = k.exchange('A', ad); err != nil {
				return false, err
			}
			skip, cancelAll = kAckAction(ack.data, true)
		}
	}
	var sent int64
	if skip {
		k.fileEvent(FileSkipped, 0, nil)
	} else {
		k.fileEvent(FileStart, 0, nil)
		kb := &kbuf{r: rc}
		regCap := min(k.sendMax, kMaxRegular) - k.chk - 2
		for !skip {
			src, err := kb.window()
			if err != nil {
				return false, fmt.Errorf("xfer: %s: %w", info.Name, err)
			}
			if len(src) == 0 {
				break
			}
			capacity := regCap
			if k.sendLong > 0 {
				capacity = k.sendLong - k.chk
			}
			enc, used := k.encodeBlock(src, capacity, false)
			if used == 0 {
				return false, fmt.Errorf("%w: peer packet size too small", ErrProtocol)
			}
			ack, err := k.exchange('D', enc)
			if err != nil {
				return false, err
			}
			kb.pos += used
			sent += int64(used)
			k.progress(sent)
			skip, cancelAll = kAckAction(ack.data, false)
		}
	}
	var zd []byte
	if skip {
		zd = []byte("D")
	}
	if _, err := k.exchange('Z', zd); err != nil {
		return false, err
	}
	if skip {
		if sent > 0 {
			k.fileEvent(FileSkipped, sent, nil)
		}
	} else {
		k.fileEvent(FileDone, sent, nil)
	}
	return cancelAll, nil
}

// KermitReceive receives a batch of files with the Kermit protocol (the
// counterpart of a remote "kermit -s" / "gkermit -s") and stores them through
// sink. A file the sink declines with ErrSkip is refused towards the sender.
// Resume offsets returned by the sink are ignored. Binary transfers only.
func KermitReceive(ctx context.Context, s Stream, sink Sink, o Options) error {
	return kermitReceive(ctx, s, sink, o, kermitCfg{})
}

func kermitReceive(ctx context.Context, s Stream, sink Sink, o Options, cfg kermitCfg) error {
	k := newKermit(ctx, s, o, cfg)
	return k.finish(k.receive(sink))
}

func (k *kermit) nak() error {
	return k.write(k.build(k.seq, 'N', nil, k.chk, false))
}

func (k *kermit) receive(sink Sink) error {
	var (
		info    FileInfo
		pending bool // F seen, sink.Create not called yet
		w       io.WriteCloser
		skip    bool
		written int64
		errs    int
		peerSet bool
	)
	closeFile := func() error {
		if w == nil {
			return nil
		}
		err := w.Close()
		w = nil
		if err != nil {
			return fmt.Errorf("xfer: %s: %w", info.Name, err)
		}
		return nil
	}
	defer closeFile()
	create := func() error {
		if !pending {
			return nil
		}
		pending = false
		fw, _, err := sink.Create(info)
		switch {
		case errors.Is(err, ErrSkip):
			skip = true
			k.fileEvent(FileSkipped, 0, nil)
		case err != nil:
			return fmt.Errorf("xfer: %s: %w", info.Name, err)
		default:
			w = fw
			k.fileEvent(FileStart, 0, nil)
		}
		return nil
	}
	fail := func(msg string, err error) error {
		return fmt.Errorf("%w: %s: %v", ErrProtocol, msg, err)
	}

	for {
		p, err := k.readPacket(time.Now().Add(k.o.Timeout))
		if err != nil {
			if errors.Is(err, errKBad) || errors.Is(err, ErrTimeout) {
				if errs++; errs > k.o.Retries {
					if errors.Is(err, ErrTimeout) {
						return ErrTimeout
					}
					return ErrTooManyErrors
				}
				if err := k.nak(); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if p.typ == 'E' {
			return fmt.Errorf("%w: %s", ErrCanceled, kerrText('E', p.data, k))
		}
		if p.seq != k.seq {
			if p.seq == (k.seq-1)&63 && k.lastAck != nil {
				// The sender missed our acknowledgement: repeat it.
				if err := k.write(k.lastAck); err != nil {
					return err
				}
				continue
			}
			if errs++; errs > k.o.Retries {
				return ErrTooManyErrors
			}
			if err := k.nak(); err != nil {
				return err
			}
			continue
		}
		var (
			ackData []byte
			ackChk  = k.chk
			done    bool
		)
		switch p.typ {
		case 'S':
			k.peer = kparseParams(p.data)
			k.negotiate()
			echo := k.peer.chkt
			if echo < '1' || echo > '3' || (k.cfg.chkt != 0 && echo > k.cfg.chkt) {
				echo = '1'
			}
			k.own.chkt = echo
			ackData, ackChk = k.own.bytes(), 1
			k.chk = int(echo - '0')
			peerSet = true
		case 'F':
			if !peerSet {
				goto unexpected
			}
			if err := closeFile(); err != nil {
				return err
			}
			name, err := k.decode(nil, p.data)
			if err == nil {
				var clean string
				if clean, err = sanitizeName(string(name)); err == nil {
					info = FileInfo{Name: clean, Size: -1}
				}
			}
			if err != nil {
				return fail("bad file name", err)
			}
			pending, skip, written = true, false, 0
			k.cur = info
		case 'A':
			if !pending {
				goto unexpected
			}
			kParseAttrs(p.data, &info)
			k.cur = info
		case 'D':
			if err := create(); err != nil {
				return err
			}
			if w == nil && !skip {
				goto unexpected
			}
			if skip {
				ackData = []byte("X")
				break
			}
			buf, err := k.decode(nil, p.data)
			if err != nil {
				return err
			}
			if _, err := w.Write(buf); err != nil {
				return fmt.Errorf("xfer: %s: %w", info.Name, err)
			}
			written += int64(len(buf))
			k.progress(written)
		case 'Z':
			if w == nil && !pending && !skip {
				goto unexpected
			}
			discard := len(p.data) > 0 && p.data[0] == 'D'
			if !discard {
				if err := create(); err != nil {
					return err
				}
			}
			wasOpen := w != nil
			if err := closeFile(); err != nil {
				return err
			}
			switch {
			case discard && (wasOpen || pending):
				k.fileEvent(FileSkipped, written, nil)
			case wasOpen:
				k.fileEvent(FileDone, written, nil)
			}
			pending, skip = false, false
			k.idx++
			k.cur = FileInfo{}
		case 'B':
			if err := closeFile(); err != nil {
				return err
			}
			done = true
		default:
			goto unexpected
		}
		errs = 0
		{
			ack := k.build(p.seq, 'Y', ackData, ackChk, false)
			k.lastAck = ack
			k.seq = (k.seq + 1) & 63
			if err := k.write(ack); err != nil {
				return err
			}
			if done {
				k.linger()
				return nil
			}
		}
		continue
	unexpected:
		if errs++; errs > k.o.Retries {
			return fmt.Errorf("%w: unexpected %q packet", ErrProtocol, p.typ)
		}
		if err := k.nak(); err != nil {
			return err
		}
	}
}

// linger stays around briefly after the final acknowledgement so that a
// repeated B (the sender missed our reply) is answered. Bytes that are not a
// packet end the wait and are left for the host.
func (k *kermit) linger() {
	wait := min(k.o.Timeout+k.o.Timeout/4, 1500*time.Millisecond)
	deadline := time.Now().Add(wait)
	for {
		c, err := k.rd.getUntil(deadline)
		if err != nil {
			return
		}
		if c == '\r' || c == '\n' {
			continue
		}
		k.rd.unget()
		if c != kMark {
			return
		}
		p, err := k.readPacket(deadline)
		switch {
		case err == nil && p.typ == 'B':
			if k.write(k.lastAck) != nil {
				return
			}
			deadline = time.Now().Add(wait)
		case err == nil, errors.Is(err, errKBad):
		default:
			return
		}
	}
}
