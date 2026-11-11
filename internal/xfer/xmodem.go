package xfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Start characters a receiver sends to request the first block.
const (
	wantCRC = 'C' // XMODEM-CRC / YMODEM
	wantG   = 'G' // YMODEM-G
)

// errBadBlock reports a damaged X/YMODEM block (recoverable).
var errBadBlock = errors.New("xfer: damaged block")

// Block check modes of the X/YMODEM receiver.
const (
	checkSum  = iota // 8-bit arithmetic checksum
	checkCRC         // CRC-16
	checkAuto        // undecided: recognise either by the block length
)

// xyInterByte is the timeout between the bytes of one block.
func (x *session) xyInterByte() time.Duration {
	return min(x.o.Timeout, 2*time.Second)
}

// xyInitInterval is the interval at which a receiver repeats its start
// character while it waits for the sender to begin.
func (x *session) xyInitInterval() time.Duration {
	return min(x.o.Timeout, 3*time.Second)
}

// xyInitAttempts is how many start characters a receiver sends before it
// gives up; the total wait is about Timeout*Retries.
func (x *session) xyInitAttempts() int {
	n := int(x.o.Timeout*time.Duration(x.o.Retries)/x.xyInitInterval()) + 1
	return max(n, x.o.Retries)
}

// xyReadBlock reads one X/YMODEM packet. It returns typ soh/stx with the
// block number and payload (valid until the next call), or typ eot.
// With *mode == checkAuto the check type is recognised from the block itself
// and stored in *mode. Errors: ErrTimeout (nothing started within timeout),
// errBadBlock (damaged block), ErrCanceled (two CANs), or a fatal error.
func (x *session) xyReadBlock(timeout time.Duration, mode *int) (typ, num byte, data []byte, err error) {
	r := x.rd
	deadline := time.Now().Add(timeout)
	size, cans := 0, 0
	for n := 0; size == 0; n++ {
		if n&0xff == 0xff && time.Now().After(deadline) {
			return 0, 0, nil, ErrTimeout
		}
		c, err := r.getUntil(deadline)
		if err != nil {
			return 0, 0, nil, err
		}
		switch c {
		case soh:
			typ, size = soh, 128
		case stx:
			typ, size = stx, 1024
		case eot:
			return eot, 0, nil, nil
		case can:
			if cans++; cans >= 2 {
				return 0, 0, nil, ErrCanceled
			}
			continue
		}
		cans = 0
	}

	ib := x.xyInterByte()
	bad := func(err error) (byte, byte, []byte, error) {
		if err == ErrTimeout {
			err = errBadBlock
		}
		return 0, 0, nil, err
	}
	num, err = r.get(ib)
	if err != nil {
		return bad(err)
	}
	inv, err := r.get(ib)
	if err != nil {
		return bad(err)
	}
	if cap(x.scratch) < 1024 {
		x.scratch = make([]byte, 1024)
	}
	data = x.scratch[:size]
	for i := range data {
		if r.pos < len(r.cur) {
			data[i] = r.cur[r.pos]
			r.pos++
			continue
		}
		if data[i], err = r.get(ib); err != nil {
			return bad(err)
		}
	}
	c1, err := r.get(ib)
	if err != nil {
		return bad(err)
	}
	m := *mode
	var c2 byte
	if m != checkSum {
		c2, err = r.get(ib)
		switch {
		case err == nil:
			m = checkCRC
		case err == ErrTimeout && m == checkAuto:
			m = checkSum // only one check byte arrived
		default:
			return bad(err)
		}
	}
	if num != ^inv {
		return bad(errBadBlock)
	}
	if m == checkCRC {
		if crc16(data) != uint16(c1)<<8|uint16(c2) {
			return bad(errBadBlock)
		}
	} else if checksum8(data) != c1 {
		return bad(errBadBlock)
	}
	*mode = m
	return typ, num, data, nil
}

// xyBlock builds an X/YMODEM packet around a full 128 or 1024 byte payload.
func xyBlock(dst []byte, num byte, payload []byte, crc bool) []byte {
	start := byte(soh)
	if len(payload) == 1024 {
		start = stx
	}
	dst = append(dst[:0], start, num, ^num)
	dst = append(dst, payload...)
	if crc {
		c := crc16(payload)
		return append(dst, byte(c>>8), byte(c))
	}
	return append(dst, checksum8(payload))
}

// xyWaitStart waits for the receiver's start character: NAK (checksum), 'C'
// (CRC) or, if allowG is set, 'G' (YMODEM-G). An ACK is skipped so that the
// function can be used right after a block that the receiver acknowledges.
// Start characters the receiver repeated while it was waiting are purged.
func (x *session) xyWaitStart(total time.Duration, allowG bool) (byte, error) {
	r := x.rd
	deadline := time.Now().Add(total)
	cans := 0
	for n := 0; ; n++ {
		if n&0xff == 0xff && time.Now().After(deadline) {
			return 0, ErrTimeout
		}
		c, err := r.getUntil(deadline)
		if err != nil {
			return 0, err
		}
		switch c {
		case can:
			if cans++; cans >= 2 {
				return 0, ErrCanceled
			}
			continue
		case nak, wantCRC, wantG:
			if c == wantG && !allowG {
				break
			}
			for r.buffered() {
				d, err := r.get(0)
				if err != nil {
					break
				}
				if d != c {
					r.unget()
					break
				}
			}
			return c, nil
		}
		cans = 0
	}
}

// xySendBlock transmits a packet and waits for its acknowledgement,
// retransmitting on NAK or timeout. first marks the first block of a
// transfer, where the receiver may still be repeating its start character.
func (x *session) xySendBlock(pkt []byte, first bool) error {
	r := x.rd
	stale := min(time.Second, x.o.Timeout/2)
	for tries := 0; ; tries++ {
		if tries > x.o.Retries {
			return ErrTooManyErrors
		}
		if err := x.write(pkt); err != nil {
			return err
		}
		sent := time.Now()
		deadline := sent.Add(x.o.Timeout)
		cans := 0
	wait:
		for {
			c, err := r.getUntil(deadline)
			if err == ErrTimeout {
				break wait
			}
			if err != nil {
				return err
			}
			switch c {
			case ack:
				return nil
			case can:
				if cans++; cans >= 2 {
					return ErrCanceled
				}
				continue
			case nak, wantCRC:
				// On the first block these may be start characters that
				// were already under way when the block was sent; repeating
				// the block for them would desynchronise the
				// acknowledgements. A genuine request is repeated.
				if c == nak && !first {
					break wait
				}
				if first && time.Since(sent) >= stale {
					break wait
				}
			}
			cans = 0
		}
	}
}

// xySendEOT ends a file: EOT until the receiver acknowledges it. With
// peekStart set (YMODEM) a start character for the next file also counts as
// acknowledgement and is left in the input.
func (x *session) xySendEOT(peekStart bool) error {
	r := x.rd
	for tries := 0; ; tries++ {
		if tries > x.o.Retries {
			return ErrTooManyErrors
		}
		if err := x.writeByte(eot); err != nil {
			return err
		}
		deadline := time.Now().Add(x.o.Timeout)
		cans := 0
	wait:
		for {
			c, err := r.getUntil(deadline)
			if err == ErrTimeout {
				break wait
			}
			if err != nil {
				return err
			}
			switch c {
			case ack:
				return nil
			case nak:
				break wait
			case can:
				if cans++; cans >= 2 {
					return ErrCanceled
				}
				continue
			case wantCRC, wantG:
				if peekStart {
					r.unget()
					return nil
				}
			}
			cans = 0
		}
	}
}

// XmodemReceive receives one file with XMODEM and writes it to w. It handles
// 128 and 1024 byte blocks, with CRC-16 if o.UseCRC is set (falling back to
// the checksum when the sender does not answer the CRC request).
//
// XMODEM does not transmit the file size: the final block is padded with SUB
// (0x1A) characters. The returned count is the number of bytes written to w,
// which includes that padding unless o.TrimPadding is set.
func XmodemReceive(ctx context.Context, s Stream, w io.Writer, o Options) (int64, error) {
	x := newSession(ctx, s, o)
	n, err := x.xmodemReceive(w)
	return n, x.end(err)
}

func (x *session) xmodemReceive(w io.Writer) (int64, error) {
	mode := checkSum
	start := byte(nak)
	if x.o.UseCRC {
		mode, start = checkAuto, wantCRC
	}
	var (
		written  int64
		hold     []byte // last block, held back while TrimPadding is set
		expected = byte(1)
		started  bool
		errs     int
		reply    = start
	)
	x.cur = FileInfo{Size: -1}
	x.fileEvent(FileStart, 0, nil)
	store := func(p []byte) error {
		if len(p) == 0 {
			return nil
		}
		n, err := w.Write(p)
		written += int64(n)
		return err
	}
	for {
		if err := x.writeByte(reply); err != nil {
			return written, err
		}
		timeout := x.o.Timeout
		if !started {
			timeout = x.xyInitInterval()
		}
		typ, num, data, err := x.xyReadBlock(timeout, &mode)
		switch {
		case err == nil && typ == eot:
			if err := x.writeByte(ack); err != nil {
				return written, err
			}
			if err := store(bytes.TrimRight(hold, string(rune(sub)))); err != nil {
				return written, err
			}
			x.fileEvent(FileDone, written, nil)
			return written, nil

		case err == nil && num == expected:
			if x.o.TrimPadding {
				if err := store(hold); err != nil {
					return written, err
				}
				hold = append(hold[:0], data...)
			} else if err := store(data); err != nil {
				return written, err
			}
			started = true
			expected++
			errs = 0
			reply = ack
			x.progress(written + int64(len(hold)))

		case err == nil && started && num == expected-1:
			// Duplicate: our ACK was lost.
			reply = ack

		case err == nil:
			return written, fmt.Errorf("%w: block %d received, %d expected", ErrProtocol, num, expected)

		case recoverable(err):
			errs++
			if err == errBadBlock {
				x.purge(x.xyInterByte() / 4)
			}
			if started {
				if errs > x.o.Retries {
					return written, ErrTooManyErrors
				}
				reply = nak
				break
			}
			if errs > x.xyInitAttempts() {
				return written, ErrTimeout
			}
			if start == wantCRC && errs >= 4 && err == ErrTimeout {
				// Nobody answers the CRC request: ask the old way. The
				// check type stays undecided until the first block shows it.
				start = nak
			}
			reply = start

		default:
			return written, err
		}
	}
}

// XmodemSend sends the content of r with XMODEM. The receiver chooses
// between checksum and CRC-16; o.Use1K selects 1024-byte blocks
// (XMODEM-1K). The final block is padded with SUB (0x1A).
//
// The returned count is the number of bytes of r that were sent and
// acknowledged, not counting padding.
func XmodemSend(ctx context.Context, s Stream, r io.Reader, o Options) (int64, error) {
	x := newSession(ctx, s, o)
	n, err := x.xmodemSend(r)
	return n, x.end(err)
}

func (x *session) xmodemSend(r io.Reader) (int64, error) {
	x.cur = FileInfo{Size: -1}
	start, err := x.xyWaitStart(x.o.Timeout*time.Duration(x.o.Retries+1), false)
	if err != nil {
		return 0, err
	}
	x.fileEvent(FileStart, 0, nil)
	sent, err := x.xySendData(r, start == wantCRC, false)
	if err != nil {
		return sent, err
	}
	if err := x.xySendEOT(false); err != nil {
		return sent, err
	}
	x.fileEvent(FileDone, sent, nil)
	return sent, nil
}

// xySendData transmits the content of r as numbered blocks starting with
// block 1. With streaming set (YMODEM-G) blocks are not acknowledged.
func (x *session) xySendData(r io.Reader, crc, streaming bool) (int64, error) {
	blockSize := 128
	if x.o.Use1K {
		blockSize = 1024
	}
	buf := make([]byte, 1024)
	var pkt []byte
	var sent int64
	num := byte(1)
	first := true
	for {
		n, rerr := io.ReadFull(r, buf[:blockSize])
		last := rerr == io.EOF || rerr == io.ErrUnexpectedEOF
		if rerr != nil && !last {
			return sent, rerr
		}
		// A short final piece goes out in 128-byte blocks to keep the
		// padding small.
		for off := 0; off < n; {
			size := blockSize
			if n-off < blockSize {
				size = 128
			}
			end := min(off+size, n)
			for i := end; i < off+size; i++ {
				buf[i] = sub
			}
			pkt = xyBlock(pkt, num, buf[off:off+size], crc)
			if streaming {
				if err := x.write(pkt); err != nil {
					return sent, err
				}
				if err := x.xyCheckAbort(); err != nil {
					return sent, err
				}
			} else if err := x.xySendBlock(pkt, first); err != nil {
				return sent, err
			}
			first = false
			num++
			sent += int64(end - off)
			off += size
			x.progress(sent)
		}
		if last {
			return sent, nil
		}
	}
}

// xyCheckAbort looks at the reverse channel of a streaming (YMODEM-G)
// transfer without blocking: the receiver cancels on any error.
func (x *session) xyCheckAbort() error {
	r := x.rd
	for r.buffered() {
		c, err := r.get(0)
		if err != nil {
			return err
		}
		switch c {
		case can:
			if c, err = r.get(x.xyInterByte()); err == nil && c == can {
				return ErrCanceled
			}
		case nak:
			return fmt.Errorf("%w: receiver rejected a block during YMODEM-G streaming", ErrProtocol)
		}
	}
	return nil
}
