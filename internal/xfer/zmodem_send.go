package xfer

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"time"
)

// ZmodemSend sends a batch of files with ZMODEM. It is the counterpart of a
// remote "rz".
//
// The function first listens briefly for the receiver's ZRINIT (present when
// the session was started from a Detector hit) and otherwise invites the
// receiver with ZRQINIT. It does not type the "rz" command; if the remote is
// sitting at a shell prompt the host should send "rz\r" itself first.
//
// Files that cannot be opened and files the receiver declines are reported
// with a FileSkipped event and the batch continues; an open error is
// returned once the session has been completed in an orderly fashion.
func ZmodemSend(ctx context.Context, s Stream, files []SendFile, o Options) error {
	x := newSession(ctx, s, o)
	z := newZmodem(x)
	return x.end(z.send(files))
}

func (z *zmodem) send(files []SendFile) error {
	if err := z.getRinit(); err != nil {
		return err
	}
	if err := z.sendSinit(); err != nil {
		return err
	}

	var bytesLeft int64
	for _, f := range files {
		if f.Info.Size > 0 {
			bytesLeft += f.Info.Size
		}
	}
	var firstErr error
	for i, f := range files {
		z.idx = i
		z.cur = f.Info
		z.cur.Name = wireName(f.Info.Name)
		err := z.sendFile(f, len(files)-i, bytesLeft)
		if f.Info.Size > 0 {
			bytesLeft -= f.Info.Size
		}
		var skip *skipError
		if errors.As(err, &skip) {
			z.fileEvent(FileSkipped, 0, skip.cause)
			if skip.cause != nil && firstErr == nil {
				firstErr = fmt.Errorf("xfer: %s: %w", z.cur.Name, skip.cause)
			}
			continue
		}
		if err != nil {
			return err
		}
	}
	z.idx = len(files)
	z.cur = FileInfo{}
	if err := z.sendFin(); err != nil {
		return err
	}
	return firstErr
}

// skipError marks a file that was left out of the batch. cause is nil when
// the receiver declined the file.
type skipError struct{ cause error }

func (e *skipError) Error() string { return "xfer: file skipped" }

// getRinit obtains the receiver's ZRINIT.
func (z *zmodem) getRinit() error {
	timeout := startupWait // the receiver may have announced itself already
	for tries := 0; ; {
		h, err := z.readHeader(timeout)
		timeout = z.o.Timeout
		if err == ErrTimeout {
			if tries++; tries > z.o.Retries+1 {
				return ErrTimeout
			}
			if err := z.sendHex(zhdr{typ: zrqinit}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		switch h.typ {
		case zrinit:
			z.applyRinit(h)
			return nil
		case zchallenge:
			// Echo the challenge value to prove we can hear the receiver.
			ack := h
			ack.typ = zack
			if err := z.sendHex(ack); err != nil {
				return err
			}
		case znak:
			if err := z.sendHex(zhdr{typ: zrqinit}); err != nil {
				return err
			}
		case zcan, zabort, zfin:
			return ErrCanceled
		default:
			// ZRQINIT (our own, echoed by a remote shell) and anything else.
		}
	}
}

// applyRinit records the receiver's capabilities.
func (z *zmodem) applyRinit(h zhdr) {
	z.rxFlags = h.b[zf0]
	z.use32 = z.rxFlags&canFC32 != 0 && !z.o.CRC16
	z.setTxEscape(z.o.EscapeControl || z.rxFlags&escCtl != 0)
	z.rxBufLen = int(h.b[zp0]) | int(h.b[zp1])<<8
	if z.rxBufLen == 0 && z.rxFlags&canOVIO == 0 {
		// The receiver cannot take data while it writes to disk: send one
		// subpacket at a time and wait for its acknowledgement.
		z.rxBufLen = z.o.BlockSize
	}
}

// sendSinit tells the receiver that we escape control characters, if it did
// not ask for that itself. No attention string is needed: this sender
// watches the reverse channel while it streams.
func (z *zmodem) sendSinit() error {
	if !z.o.EscapeControl || z.rxFlags&escCtl != 0 {
		return nil
	}
	h := zhdr{typ: zsinit}
	h.b[zf0] = tescCtl
	for tries := 0; ; {
		if err := z.sendHex(h); err != nil {
			return err
		}
		if err := z.sendData([]byte{0}, zcrcw); err != nil {
			return err
		}
		deadline := time.Now().Add(z.o.Timeout)
	wait:
		for {
			r, err := z.readHeader(time.Until(deadline))
			if err == ErrTimeout {
				break wait
			}
			if err != nil {
				return err
			}
			switch r.typ {
			case zack:
				return nil
			case znak:
				break wait
			case zcan, zabort, zfin:
				return ErrCanceled
			}
		}
		if tries++; tries > z.o.Retries {
			return ErrTimeout
		}
	}
}

// sendFile transmits one file. It returns a *skipError if the file was left
// out of the batch.
func (z *zmodem) sendFile(f SendFile, filesLeft int, bytesLeft int64) error {
	if f.Open == nil {
		return &skipError{cause: errors.New("no Open function")}
	}
	rs, err := f.Open()
	if err != nil {
		return &skipError{cause: err}
	}
	defer rs.Close()
	size, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return &skipError{cause: err}
	}
	z.cur.Size = size

	hdr := zhdr{typ: zfile}
	hdr.b[zf0] = zcbin
	if z.o.Resume {
		hdr.b[zf0] = zcresum
	}
	if z.o.Overwrite {
		hdr.b[zf1] = zmclob
	}
	meta := buildFileHeader(z.cur, size, filesLeft, max(bytesLeft, size))

	// Offer the file and wait for the receiver's verdict.
	var pos int64
	tries, extraInit := 0, 0
	offer := true
verdict:
	for {
		if offer {
			if tries > z.o.Retries {
				return ErrTimeout
			}
			if err := z.sendBin(hdr); err != nil {
				return err
			}
			if err := z.sendData(meta, zcrcw); err != nil {
				return err
			}
			offer = false
		}
		h, err := z.readHeader(z.o.Timeout)
		if err == ErrTimeout {
			tries++
			offer = true
			continue
		}
		if err != nil {
			return err
		}
		switch h.typ {
		case zrpos:
			pos = unwrapPos(h.pos(), size)
			if pos > size {
				pos = size
			}
			break verdict
		case zskip:
			return &skipError{}
		case zrinit:
			// Either a duplicate of the receiver's announcement or its way
			// of saying that it is still waiting for a ZFILE. Tolerate one
			// before repeating the offer.
			if extraInit++; extraInit > 1 {
				tries++
				offer = true
			}
		case znak:
			tries++
			offer = true
		case zcrc:
			// The receiver wants the CRC-32 of the file (or of its first
			// ZP0..3 bytes) to decide whether it already has it.
			crc, err := fileCRC(rs, int64(h.pos()))
			if err != nil {
				_ = z.sendHex(zhdr{typ: zferr})
				return fmt.Errorf("xfer: %s: %w", z.cur.Name, err)
			}
			if err := z.sendBin(posHdr(zcrc, crc)); err != nil {
				return err
			}
		case zferr:
			return fmt.Errorf("%w: receiver reported a file error", ErrCanceled)
		case zcan, zabort, zfin:
			return ErrCanceled
		}
	}

	z.fileEvent(FileStart, pos, nil)
	tx := &zsender{zmodem: z, rs: rs, size: size, buf: make([]byte, z.o.BlockSize)}
	skipped, err := tx.run(pos)
	if err != nil {
		return err
	}
	if skipped {
		return &skipError{}
	}
	z.fileEvent(FileDone, tx.txpos, nil)
	return nil
}

// fileCRC computes the CRC-32 of the first n bytes of rs (the whole file if n
// is 0) as requested by a ZCRC frame.
func fileCRC(rs io.ReadSeeker, n int64) (uint32, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	h := crc32.NewIEEE()
	var err error
	if n > 0 {
		_, err = io.CopyN(h, rs, n)
		if err == io.EOF {
			err = nil
		}
	} else {
		_, err = io.Copy(h, rs)
	}
	return h.Sum32(), err
}

// zsender streams the data of one file.
type zsender struct {
	*zmodem
	rs   io.ReadSeeker
	size int64
	buf  []byte

	txpos   int64 // file position of the next byte to send
	lastAck int64 // highest position acknowledged by the receiver
	inFrame bool  // a ZDATA frame is open (last subpacket was ZCRCG/ZCRCQ)
	frame   int   // bytes sent in the current frame
	sinceQ  int   // bytes sent since the last acknowledgement request
	sync    bool  // the next subpacket must be acknowledged (after ZRPOS)

	blk     int // current subpacket size
	goodRun int // subpackets sent since the last error
	tries   int // consecutive timeouts

	// Progress tracking for the give-up decision: the highest position the
	// receiver has confirmed (by ZACK or by asking for it with ZRPOS) and
	// when it last advanced.
	high      int64
	advanced  time.Time
	sentSince bool // data was sent since the last reposition
}

// Outcomes of handling a header while sending file data.
const (
	txContinue = iota // keep going
	txDone            // the receiver has the whole file
	txSkipped         // the receiver does not want the rest of the file
)

// run sends the file from pos and returns once the receiver has confirmed
// the end of the file or declined it.
func (t *zsender) run(pos int64) (skipped bool, err error) {
	t.blk = len(t.buf)
	t.high, t.advanced = pos, time.Now()
	if err := t.seek(pos); err != nil {
		return false, err
	}
	for {
		// After an error the receiver may have repeated its ZRPOS many times
		// (lrzsz does so for every stretch of in-flight data it discards).
		// Take in everything that has arrived before restarting, so that a
		// burst of repeats causes a single restart.
		if t.sync {
			if st, err := t.poll(); err != nil || st != txContinue {
				return st == txSkipped, err
			}
		}

		// Respect the transmit window.
		if w := int64(t.o.WindowSize); w > 0 && t.inFrame {
			for t.inFrame && t.txpos-t.lastAck >= w {
				st, err := t.await(false)
				if err != nil || st != txContinue {
					return st == txSkipped, err
				}
			}
		}

		// Read the next piece of the file.
		want := t.blk
		frameEnd := byte(zcrcg)
		if t.rxBufLen > 0 {
			if room := t.rxBufLen - t.frame; !t.inFrame {
				want = min(want, t.rxBufLen)
				if want >= t.rxBufLen {
					frameEnd = zcrcw
				}
			} else if want >= room {
				want = room
				frameEnd = zcrcw
			}
		}
		n, rerr := io.ReadFull(t.rs, t.buf[:want])
		eof := rerr == io.EOF || rerr == io.ErrUnexpectedEOF
		if rerr != nil && !eof {
			_ = t.sendHex(zhdr{typ: zferr})
			return false, fmt.Errorf("xfer: %s: %w", t.cur.Name, rerr)
		}
		switch {
		case eof:
			frameEnd = zcrce
		case t.sync:
			frameEnd = zcrcw
		case frameEnd == zcrcw:
		case t.o.WindowSize > 0 && t.sinceQ+n >= max(t.o.WindowSize/4, 1):
			frameEnd = zcrcq
		}

		// Send it.
		if n > 0 || t.inFrame {
			if !t.inFrame {
				if err := t.sendBin(posHdr(zdata, uint32(t.txpos))); err != nil {
					return false, err
				}
				t.inFrame, t.frame = true, 0
			}
			if err := t.sendData(t.buf[:n], frameEnd); err != nil {
				return false, err
			}
			t.txpos += int64(n)
			t.frame += n
			t.sinceQ += n
			t.sentSince = true
			if frameEnd == zcrcq {
				t.sinceQ = 0
			}
			if frameEnd == zcrce || frameEnd == zcrcw {
				t.inFrame = false
			}
			t.progress(t.txpos)
			if t.goodRun++; t.goodRun >= 32 && t.blk < len(t.buf) {
				t.blk = min(2*t.blk, len(t.buf))
				t.goodRun = 0
			}
		}

		var st int
		var err error
		switch {
		case eof:
			st, err = t.finish()
		case frameEnd == zcrcw:
			st, err = t.awaitAck()
		default:
			st, err = t.poll()
		}
		if err != nil || st != txContinue {
			return st == txSkipped, err
		}
	}
}

func (t *zsender) seek(pos int64) error {
	if _, err := t.rs.Seek(pos, io.SeekStart); err != nil {
		_ = t.sendHex(zhdr{typ: zferr})
		return fmt.Errorf("xfer: %s: %w", t.cur.Name, err)
	}
	t.txpos, t.lastAck = pos, pos
	return nil
}

// confirm records that the receiver has everything up to pos.
func (t *zsender) confirm(pos int64) {
	if pos > t.high {
		t.high, t.advanced = pos, time.Now()
	}
}

// reposition restarts transmission at pos, as requested by a ZRPOS.
func (t *zsender) reposition(pos int64) error {
	if t.inFrame {
		// Close the open frame so that the receiver looks for a header.
		if err := t.sendData(nil, zcrce); err != nil {
			return err
		}
		t.inFrame = false
	}
	if pos > t.size {
		pos = t.size
	}
	// Repeated requests for the same position are normal (see run), so the
	// transfer is abandoned only when the receiver has made no progress at
	// all for the whole retry budget.
	t.confirm(pos)
	if time.Since(t.advanced) > t.o.Timeout*time.Duration(t.o.Retries) {
		return ErrTooManyErrors
	}
	if err := t.seek(pos); err != nil {
		return err
	}
	// The first subpacket after an error is sent with ZCRCW: waiting for
	// its acknowledgement keeps repeated ZRPOS headers from causing a
	// restart storm. Smaller subpackets get through a noisy line better.
	t.sync = true
	t.sinceQ = 0
	if t.sentSince {
		t.goodRun = 0
		t.blk = max(t.blk/2, min(256, len(t.buf)))
	}
	t.sentSince = false
	return nil
}

// handle processes a header received from the receiver during the data
// phase.
func (t *zsender) handle(h zhdr) (int, error) {
	switch h.typ {
	case zack:
		if p := unwrapPos(h.pos(), t.txpos); p > t.lastAck {
			t.lastAck = p
			t.confirm(p)
		}
	case zrpos:
		return txContinue, t.reposition(unwrapPos(h.pos(), t.txpos))
	case zskip:
		return txSkipped, nil
	case zrinit:
		// The receiver considers the file complete.
		return txDone, nil
	case zferr:
		return txContinue, fmt.Errorf("%w: receiver reported a file error", ErrCanceled)
	case zcan, zabort, zfin:
		return txContinue, ErrCanceled
	}
	return txContinue, nil
}

// poll looks at the reverse channel without blocking and handles a header
// if one has arrived.
func (t *zsender) poll() (int, error) {
	r := t.rd
	for r.buffered() {
		c, err := r.get(0)
		if err != nil {
			return txContinue, err
		}
		switch c {
		case xoff, xoff | 0x80:
			// Flow control: wait for the XON (or anything else).
			c, err := r.get(t.o.Timeout)
			if err != nil && err != ErrTimeout {
				return txContinue, err
			}
			if err == nil && c != xon && c != xon|0x80 {
				r.unget()
			}
		case zpad, zdle:
			r.unget()
			h, err := t.readHeader(min(t.o.Timeout, 2*time.Second))
			if err == ErrTimeout {
				return txContinue, nil // line noise
			}
			if err != nil {
				return txContinue, err
			}
			if st, err := t.handle(h); err != nil || st != txContinue {
				return st, err
			}
		}
	}
	return txContinue, nil
}

// await blocks until a header arrives and handles it. On a timeout it
// solicits an acknowledgement (or, if resend is set, restarts from the last
// acknowledged position).
func (t *zsender) await(resend bool) (int, error) {
	h, err := t.readHeader(t.o.Timeout)
	if err == ErrTimeout {
		if t.tries++; t.tries > t.o.Retries {
			return txContinue, ErrTimeout
		}
		if resend || !t.inFrame {
			return txContinue, t.reposition(t.lastAck)
		}
		return txContinue, t.sendData(nil, zcrcq)
	}
	if err != nil {
		return txContinue, err
	}
	if h.typ == zack || h.typ == zrpos {
		t.tries = 0
	}
	return t.handle(h)
}

// awaitAck waits for the acknowledgement of a ZCRCW subpacket.
func (t *zsender) awaitAck() (int, error) {
	for {
		pos := t.txpos
		st, err := t.await(true)
		if err != nil || st != txContinue {
			return st, err
		}
		if t.txpos != pos {
			return txContinue, nil // repositioned: carry on from there
		}
		if t.lastAck == t.txpos {
			t.sync = false
			return txContinue, nil
		}
	}
}

// finish announces the end of the file and waits for the receiver to
// confirm it with ZRINIT.
func (t *zsender) finish() (int, error) {
	announce := true
	for {
		if announce {
			if err := t.sendBin(posHdr(zeof, uint32(t.txpos))); err != nil {
				return txContinue, err
			}
			announce = false
		}
		h, err := t.readHeader(t.o.Timeout)
		if err == ErrTimeout {
			if t.tries++; t.tries > t.o.Retries {
				return txContinue, ErrTimeout
			}
			announce = true
			continue
		}
		if err != nil {
			return txContinue, err
		}
		switch h.typ {
		case zrinit:
			return txDone, nil
		case znak:
			announce = true
		case zrpos:
			t.tries = 0
			return txContinue, t.reposition(unwrapPos(h.pos(), t.txpos))
		default:
			if st, err := t.handle(h); err != nil || st != txContinue {
				return st, err
			}
		}
	}
}

// sendFin ends the session: ZFIN, the receiver's ZFIN, then "OO" (over and
// out). A receiver that does not answer is not an error; the files are
// already transferred.
func (z *zmodem) sendFin() error {
	timeout := min(z.o.Timeout, 5*time.Second)
	for range 3 {
		if err := z.sendHex(zhdr{typ: zfin}); err != nil {
			return err
		}
		deadline := time.Now().Add(timeout)
	wait:
		for {
			h, err := z.readHeader(time.Until(deadline))
			if err == ErrTimeout {
				break wait
			}
			if errors.Is(err, ErrCanceled) {
				return nil
			}
			if err != nil {
				return err
			}
			switch h.typ {
			case zfin:
				return z.write([]byte("OO"))
			case zcan, zabort:
				return nil
			}
		}
	}
	return nil
}
