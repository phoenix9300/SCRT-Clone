package xfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// YmodemReceive receives a batch of files with YMODEM (the counterpart of a
// remote "sb") and stores them through sink. With o.YmodemG it asks for
// YMODEM-G streaming, which has no error recovery and is only suitable for
// error-free links.
//
// YMODEM cannot decline a single file: when the sink returns ErrSkip the
// file is received and discarded. Resume offsets returned by the sink are
// ignored.
func YmodemReceive(ctx context.Context, s Stream, sink Sink, o Options) error {
	x := newSession(ctx, s, o)
	return x.end(x.ymodemReceive(sink))
}

func (x *session) ymodemReceive(sink Sink) error {
	g := x.o.YmodemG
	start := byte(wantCRC)
	if g {
		start = wantG
	}
	mode := checkCRC
	for {
		// Block 0: file name and attributes, or an empty name at the end of
		// the batch.
		var info FileInfo
		endOfBatch := false
		for errs := 0; ; {
			if err := x.writeByte(start); err != nil {
				return err
			}
			typ, num, data, err := x.xyReadBlock(x.xyInitInterval(), &mode)
			if err == nil && typ == eot {
				// A repeated EOT: the sender missed our acknowledgement.
				if err := x.writeByte(ack); err != nil {
					return err
				}
				continue
			}
			if err == nil && num == 0 {
				if data[0] == 0 {
					endOfBatch = true
					break
				}
				if info, err = parseFileHeader(data); err == nil {
					break
				}
				return fmt.Errorf("%w: %v", ErrProtocol, err)
			}
			if err == nil {
				return fmt.Errorf("%w: data block %d received instead of a YMODEM header block", ErrProtocol, num)
			}
			if !recoverable(err) {
				return err
			}
			if err == errBadBlock {
				if g {
					return fmt.Errorf("%w: damaged block during YMODEM-G transfer", ErrProtocol)
				}
				x.purge(x.xyInterByte() / 4)
			}
			if errs++; errs > x.xyInitAttempts() {
				return ErrTimeout
			}
		}
		if endOfBatch {
			if g {
				return nil
			}
			return x.writeByte(ack)
		}

		x.cur = info
		w, _, err := sink.Create(info)
		skipped := errors.Is(err, ErrSkip)
		if err != nil && !skipped {
			return fmt.Errorf("xfer: %s: %w", info.Name, err)
		}
		if skipped {
			x.fileEvent(FileSkipped, 0, nil)
			w = nopWriteCloser{io.Discard}
		} else {
			x.fileEvent(FileStart, 0, nil)
		}
		n, err := x.ymodemReceiveData(w, info.Size, start)
		if cerr := w.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("xfer: %s: %w", info.Name, cerr)
		}
		if err != nil {
			return err
		}
		if !skipped {
			x.fileEvent(FileDone, n, nil)
		}
		x.idx++
		x.cur = FileInfo{}
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// ymodemReceiveData receives the data blocks of one file. size is the
// announced length (-1 if unknown); padding beyond it is dropped.
func (x *session) ymodemReceiveData(w io.Writer, size int64, start byte) (int64, error) {
	g := start == wantG
	mode := checkCRC
	var (
		written  int64
		hold     []byte // final block held back for TrimPadding when size is unknown
		expected = byte(1)
		started  bool
		errs     int
	)
	store := func(p []byte) error {
		if size >= 0 && written+int64(len(p)) > size {
			p = p[:size-written]
		}
		if len(p) == 0 {
			return nil
		}
		n, err := w.Write(p)
		written += int64(n)
		return err
	}
	// Acknowledge block 0 and ask for the data.
	reply := []byte{ack, start}
	if g {
		reply = reply[1:]
	}
	for {
		if len(reply) > 0 {
			if err := x.write(reply); err != nil {
				return written, err
			}
		}
		typ, num, data, err := x.xyReadBlock(x.o.Timeout, &mode)
		switch {
		case err == nil && typ == eot:
			if err := x.writeByte(ack); err != nil {
				return written, err
			}
			if err := store(bytes.TrimRight(hold, string(rune(sub)))); err != nil {
				return written, err
			}
			return written, nil

		case err == nil && num == expected && (started || num != 0):
			if size < 0 && x.o.TrimPadding {
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
			reply = []byte{ack}
			if g {
				reply = nil
			}
			x.progress(written + int64(len(hold)))

		case err == nil && started && num == expected-1:
			reply = []byte{ack} // duplicate

		case err == nil && !started && num == 0:
			// Block 0 again: the sender missed its acknowledgement.
			reply = []byte{ack, start}

		case err == nil:
			return written, fmt.Errorf("%w: block %d received, %d expected", ErrProtocol, num, expected)

		case recoverable(err):
			if g && (started || err == errBadBlock) {
				return written, fmt.Errorf("%w: transmission error during YMODEM-G transfer", ErrProtocol)
			}
			if errs++; errs > x.o.Retries {
				if err == ErrTimeout {
					return written, ErrTimeout
				}
				return written, ErrTooManyErrors
			}
			if err == errBadBlock {
				x.purge(x.xyInterByte() / 4)
			}
			if started {
				reply = []byte{nak}
			} else {
				reply = []byte{start}
			}

		default:
			return written, err
		}
	}
}

// YmodemSend sends a batch of files with YMODEM (the counterpart of a
// remote "rb"). Each file is announced with its name, size, modification
// time and mode. o.Use1K selects 1024-byte data blocks. If the receiver asks
// for YMODEM-G the data is streamed without per-block acknowledgements.
func YmodemSend(ctx context.Context, s Stream, files []SendFile, o Options) error {
	x := newSession(ctx, s, o)
	return x.end(x.ymodemSend(files))
}

func (x *session) ymodemSend(files []SendFile) error {
	firstWait := x.o.Timeout * time.Duration(x.o.Retries+1)
	var firstErr error
	var pkt []byte
	sentAny := false
	for i, f := range files {
		x.idx = i
		x.cur = f.Info
		x.cur.Name = wireName(f.Info.Name)

		rs, size, err := openForSend(f)
		if err != nil {
			x.fileEvent(FileSkipped, 0, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("xfer: %s: %w", x.cur.Name, err)
			}
			continue
		}
		x.cur.Size = size
		err = func() error {
			defer rs.Close()
			// The receiver asks for the header block.
			wait := x.o.Timeout
			if !sentAny {
				wait = firstWait
			}
			start, err := x.xyWaitStart(wait, true)
			if err != nil {
				return err
			}
			if start == nak {
				return fmt.Errorf("%w: the receiver speaks XMODEM, not YMODEM", ErrProtocol)
			}
			sentAny = true
			g := start == wantG
			meta := buildFileHeader(x.cur, size, 0, 0)
			hdr := make([]byte, 128)
			if len(meta) > 128 {
				hdr = make([]byte, 1024)
			}
			if len(meta) > len(hdr) {
				return fmt.Errorf("xfer: %s: file name too long for YMODEM", x.cur.Name)
			}
			copy(hdr, meta)
			pkt = xyBlock(pkt, 0, hdr, true)
			if g {
				err = x.write(pkt)
			} else {
				err = x.xySendBlock(pkt, true)
			}
			if err != nil {
				return err
			}
			// The receiver asks for the data.
			start, err = x.xyWaitStart(x.o.Timeout, true)
			if err != nil {
				return err
			}
			g = start == wantG
			x.fileEvent(FileStart, 0, nil)
			sent, err := x.xySendData(rs, true, g)
			if err != nil {
				return err
			}
			if err := x.xySendEOT(true); err != nil {
				return err
			}
			x.fileEvent(FileDone, sent, nil)
			return nil
		}()
		if err != nil {
			return err
		}
	}
	x.idx = len(files)
	x.cur = FileInfo{}

	// End of batch: an all-zero header block.
	wait := x.o.Timeout
	if !sentAny {
		wait = firstWait
	}
	start, err := x.xyWaitStart(wait, true)
	if err != nil {
		return err
	}
	pkt = xyBlock(pkt, 0, make([]byte, 128), true)
	if start == wantG {
		if err := x.write(pkt); err != nil {
			return err
		}
		return firstErr
	}
	// The acknowledgement of the final block is a courtesy: wait for it only
	// briefly, and do not let its loss turn a completed batch into a failure.
	for range 2 {
		if err := x.write(pkt); err != nil {
			return err
		}
		deadline := time.Now().Add(min(x.o.Timeout, 2*time.Second))
		for {
			c, err := x.rd.getUntil(deadline)
			if err != nil {
				if cerr := x.ctx.Err(); cerr != nil {
					return cerr
				}
				if err == ErrTimeout {
					break
				}
				return firstErr // the remote is gone; the batch is complete
			}
			if c == ack {
				return firstErr
			}
		}
	}
	return firstErr
}

// openForSend opens a file of an outgoing batch and determines its size.
func openForSend(f SendFile) (io.ReadSeekCloser, int64, error) {
	if f.Open == nil {
		return nil, 0, errors.New("no Open function")
	}
	rs, err := f.Open()
	if err != nil {
		return nil, 0, err
	}
	size, err := rs.Seek(0, io.SeekEnd)
	if err == nil {
		_, err = rs.Seek(0, io.SeekStart)
	}
	if err != nil {
		rs.Close()
		return nil, 0, err
	}
	return rs, size, nil
}
