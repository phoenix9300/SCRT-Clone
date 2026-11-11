package xfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// startupWait is how long a ZMODEM session listens for a header that the
// peer may already have sent (the announcement that triggered the session)
// before it speaks itself.
const startupWait = 250 * time.Millisecond

// ZmodemReceive receives a batch of files with ZMODEM and stores them through
// sink. It is the counterpart of a remote "sz".
//
// The function may be started from a Detector hit, in which case the first
// bytes it reads are the sender's ZRQINIT header, or on the user's request
// before or after the remote sender was started.
//
// It returns nil when the sender finished the session normally, ErrCanceled
// if the remote cancelled, ctx.Err() if ctx was cancelled (the remote is sent
// the cancel sequence), or another error describing the failure.
func ZmodemReceive(ctx context.Context, s Stream, sink Sink, o Options) error {
	x := newSession(ctx, s, o)
	z := newZmodem(x)
	return x.end(z.receive(sink))
}

func (z *zmodem) receive(sink Sink) error {
	z.rxCtl = z.o.EscapeControl

	// Take in what the sender already sent. One or more ZRQINITs are expected
	// here when the session was auto-started; they are answered by the single
	// ZRINIT below.
	var pending *zhdr
	wait := startupWait
	for pending == nil {
		h, err := z.readHeader(wait)
		if err == ErrTimeout {
			break
		}
		if err != nil {
			return err
		}
		if h.typ != zrqinit {
			pending = &h
		}
		wait = 0 // only what is already here
	}

	tries := 0
	needInit := true
	for {
		var h zhdr
		if pending != nil {
			h, pending = *pending, nil
		} else {
			if needInit {
				if err := z.sendRinit(); err != nil {
					return err
				}
				needInit = false
			}
			var err error
			h, err = z.readHeader(z.o.Timeout)
			if err == ErrTimeout {
				if tries++; tries > z.o.Retries {
					return ErrTimeout
				}
				needInit = true
				continue
			}
			if err != nil {
				return err
			}
		}

		switch h.typ {
		case zrqinit:
			// The sender (re)announced itself.
			needInit = true

		case zsinit:
			// Sender flags and attention string.
			z.rxCtl = z.o.EscapeControl || h.b[zf0]&tescCtl != 0
			data, _, err := z.readData(maxSubpacket)
			if err != nil {
				if !recoverable(err) {
					return err
				}
				if err := z.sendHex(zhdr{typ: znak}); err != nil {
					return err
				}
				continue
			}
			for i, c := range data {
				if c == 0 {
					data = data[:i]
					break
				}
			}
			if len(data) > 32 {
				data = data[:32]
			}
			z.attn = append(z.attn[:0], data...)
			if err := z.sendHex(zhdr{typ: zack}); err != nil {
				return err
			}

		case zfile:
			tries = 0
			if err := z.recvFile(sink); err != nil {
				return err
			}
			needInit = true

		case zeof:
			// A repeated ZEOF: the sender missed the ZRINIT that
			// acknowledged the end of the previous file.
			needInit = true

		case zfin:
			return z.recvFin()

		case zfreecnt:
			// Free space on the file system; 0 means "plenty".
			if err := z.sendHex(zhdr{typ: zack}); err != nil {
				return err
			}

		case zcommand:
			// Remote command execution is never honoured. Read the command
			// and report failure.
			if _, _, err := z.readData(maxSubpacket); err != nil && !recoverable(err) {
				return err
			}
			if err := z.sendHex(posHdr(zcompl, 1)); err != nil {
				return err
			}

		case zcan, zabort:
			return ErrCanceled

		default:
			// ZDATA, ZNAK, ZACK ...: stale or meaningless here.
		}
	}
}

// sendRinit announces the receiver's capabilities.
func (z *zmodem) sendRinit() error {
	h := zhdr{typ: zrinit}
	h.b[zf0] = canFDX | canOVIO
	if !z.o.CRC16 {
		h.b[zf0] |= canFC32
	}
	if z.o.EscapeControl {
		h.b[zf0] |= escCtl
	}
	// ZP0/ZP1 (receive buffer size) stay 0: full streaming.
	return z.sendHex(h)
}

// recvFile handles one ZFILE frame: it reads the file information, asks the
// sink where to put the file and receives the content.
func (z *zmodem) recvFile(sink Sink) error {
	data, _, err := z.readData(maxSubpacket)
	if err != nil {
		if !recoverable(err) {
			return err
		}
		// The sender repeats the ZFILE frame.
		return z.sendHex(zhdr{typ: znak})
	}
	info, err := parseFileHeader(data)
	if err != nil {
		z.cur = info
		z.fileEvent(FileSkipped, 0, err)
		return z.sendHex(zhdr{typ: zskip})
	}
	z.cur = info
	w, offset, err := sink.Create(info)
	if errors.Is(err, ErrSkip) {
		z.fileEvent(FileSkipped, 0, nil)
		return z.sendHex(zhdr{typ: zskip})
	}
	if err != nil {
		_ = z.sendHex(zhdr{typ: zferr})
		return fmt.Errorf("xfer: %s: %w", info.Name, err)
	}
	if offset < 0 {
		offset = 0
	}
	z.fileEvent(FileStart, offset, nil)
	n, err := z.recvFileData(w, offset)
	if cerr := w.Close(); err == nil && cerr != nil {
		_ = z.sendHex(zhdr{typ: zferr})
		err = fmt.Errorf("xfer: %s: %w", info.Name, cerr)
	}
	if err != nil {
		return err
	}
	z.fileEvent(FileDone, n, nil)
	z.idx++
	z.cur = FileInfo{}
	return nil
}

// recvFileData receives the content of one file starting at offset and
// returns the final file position. It returns when the sender's ZEOF matches
// the receive position.
func (z *zmodem) recvFileData(w io.Writer, offset int64) (int64, error) {
	rxpos := offset
	if err := z.sendPos(zrpos, rxpos); err != nil {
		return rxpos, err
	}
	// tries counts consecutive timeouts, errs consecutive damaged frames;
	// both are reset whenever data is stored.
	tries, errs := 0, 0
	resync := func() error {
		if errs++; errs > z.o.Retries {
			return ErrTooManyErrors
		}
		if err := z.sendAttn(); err != nil {
			return err
		}
		return z.sendPos(zrpos, rxpos)
	}
	for {
		h, err := z.readHeader(z.o.Timeout)
		if err == ErrTimeout {
			if tries++; tries > z.o.Retries {
				return rxpos, ErrTimeout
			}
			if err := z.sendPos(zrpos, rxpos); err != nil {
				return rxpos, err
			}
			continue
		}
		if err != nil {
			return rxpos, err
		}

		switch h.typ {
		case zdata:
			// behind > 0: the frame starts before our position (a repeated
			// restart); the overlap is read and dropped. behind < 0: the
			// sender is ahead of us, data was lost.
			behind := int32(uint32(rxpos) - h.pos())
			if behind < 0 {
				if err := resync(); err != nil {
					return rxpos, err
				}
				continue
			}
			skip := int(behind)
		frame:
			for {
				data, frameEnd, err := z.readData(maxSubpacket)
				if err != nil {
					if !recoverable(err) {
						return rxpos, err
					}
					if err := resync(); err != nil {
						return rxpos, err
					}
					break frame
				}
				if skip > 0 {
					n := min(skip, len(data))
					data = data[n:]
					skip -= n
				}
				if len(data) > 0 {
					if _, err := w.Write(data); err != nil {
						_ = z.sendHex(zhdr{typ: zferr})
						return rxpos, fmt.Errorf("xfer: %s: %w", z.cur.Name, err)
					}
					rxpos += int64(len(data))
					tries, errs = 0, 0
					z.progress(rxpos)
				}
				switch frameEnd {
				case zcrcq:
					if err := z.sendPos(zack, rxpos); err != nil {
						return rxpos, err
					}
				case zcrcw:
					if err := z.sendPos(zack, rxpos); err != nil {
						return rxpos, err
					}
					break frame
				case zcrce:
					break frame
				}
			}

		case zeof:
			if h.pos() != uint32(rxpos) {
				// Stale: it was sent before our ZRPOS arrived. If nothing
				// better follows, the timeout repeats the ZRPOS.
				continue
			}
			return rxpos, nil

		case zfile:
			// The sender repeated ZFILE: it missed our ZRPOS.
			if _, _, err := z.readData(maxSubpacket); err != nil && !recoverable(err) {
				return rxpos, err
			}
			if err := z.sendPos(zrpos, rxpos); err != nil {
				return rxpos, err
			}

		case znak:
			if err := z.sendPos(zrpos, rxpos); err != nil {
				return rxpos, err
			}

		case zfin:
			// The sender gave up on the session.
			_ = z.sendHex(zhdr{typ: zfin})
			return rxpos, fmt.Errorf("%w: sender ended the session before the file was complete", ErrCanceled)

		case zcan, zabort:
			return rxpos, ErrCanceled

		case zferr:
			return rxpos, fmt.Errorf("%w: sender reported a file error", ErrCanceled)

		default:
			// ZRQINIT, ZSINIT, ZACK ...: ignore.
		}
	}
}

// recvFin completes the session: it answers the sender's ZFIN and consumes
// the "OO" (over and out) that a well-behaved sender transmits in return.
func (z *zmodem) recvFin() error {
	if err := z.sendHex(zhdr{typ: zfin}); err != nil {
		return err
	}
	wait := min(z.o.Timeout, 2*time.Second)
	deadline := time.Now().Add(wait)
	for range 64 {
		c, err := z.rd.getUntil(deadline)
		if err != nil {
			break
		}
		switch c {
		case 'O':
			if c, err = z.rd.get(wait / 4); err == nil && c != 'O' {
				z.rd.unget()
			}
			return nil
		case xon, '\r' | 0x80, '\n' | 0x80:
			// Remnants of the header trailer.
			continue
		case zpad:
			// Either the sender repeats ZFIN because it missed ours, or this
			// is terminal output. Look ahead before treating it as a header.
			seq := []byte{c}
			isHeader := false
			for len(seq) < 3 && !isHeader {
				c, err := z.rd.get(wait / 4)
				if err != nil {
					break
				}
				seq = append(seq, c)
				if c == zdle {
					isHeader = true
				} else if c != zpad {
					break
				}
			}
			z.rd.unread(seq)
			if !isHeader {
				return nil
			}
			h, err := z.readHeader(time.Until(deadline))
			if err == nil && h.typ == zfin {
				if err := z.sendHex(zhdr{typ: zfin}); err != nil {
					return err
				}
				deadline = time.Now().Add(wait)
				continue
			}
			return nil
		}
		// Not ours: terminal output has resumed.
		z.rd.unget()
		break
	}
	// The session is complete even if the sender's "OO" never shows up.
	return nil
}
