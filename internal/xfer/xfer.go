package xfer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ASCII control characters used by the protocols.
const (
	soh  = 0x01 // start of a 128-byte X/YMODEM block
	stx  = 0x02 // start of a 1024-byte X/YMODEM block
	eot  = 0x04 // end of transmission
	ack  = 0x06
	bs   = 0x08
	xon  = 0x11
	xoff = 0x13
	nak  = 0x15
	can  = 0x18 // cancel; doubles as ZDLE in ZMODEM
	sub  = 0x1a // CP/M end-of-file, the X/YMODEM padding byte
)

// Errors returned by the transfer functions.
var (
	// ErrSkip is returned by Sink.Create to decline an incoming file.
	ErrSkip = errors.New("xfer: skip file")
	// ErrCanceled reports that the remote side cancelled the transfer.
	ErrCanceled = errors.New("xfer: transfer cancelled by remote")
	// ErrTimeout reports that the remote side stopped responding.
	ErrTimeout = errors.New("xfer: timed out waiting for remote")
	// ErrTooManyErrors reports that the retry limit was exhausted without
	// the transfer making progress.
	ErrTooManyErrors = errors.New("xfer: too many transmission errors")
	// ErrProtocol reports a violation of the protocol by the remote side
	// that cannot be recovered from.
	ErrProtocol = errors.New("xfer: protocol error")
)

// FileInfo describes a file as announced by the sending side.
type FileInfo struct {
	Name    string    // base name as transmitted (sanitised: no path traversal)
	Size    int64     // -1 if unknown
	ModTime time.Time // zero if unknown
	Mode    uint32    // unix mode bits, 0 if unknown
}

// Sink receives incoming files.
type Sink interface {
	// Create is called for each incoming file. Return a writer and the offset
	// to resume from (0 = from start; >0 only meaningful for ZMODEM crash
	// recovery). Return xfer.ErrSkip to skip the file. Any other error
	// aborts the whole transfer.
	//
	// The writer is closed when the file is complete and also when the
	// transfer fails part-way.
	Create(info FileInfo) (w io.WriteCloser, offset int64, err error)
}

// SendFile is one file of an outgoing batch.
type SendFile struct {
	// Info describes the file to the receiver. If Info.Size is negative the
	// size is determined by seeking the opened file.
	Info FileInfo
	// Open opens the file content. It is called once, when the file's turn
	// comes.
	Open func() (io.ReadSeekCloser, error)
}

// SendFileFromPath builds a SendFile for a regular file on disk.
func SendFileFromPath(path string) (SendFile, error) {
	st, err := os.Stat(path)
	if err != nil {
		return SendFile{}, err
	}
	if !st.Mode().IsRegular() {
		return SendFile{}, &os.PathError{Op: "send", Path: path, Err: errors.New("not a regular file")}
	}
	return SendFile{
		Info: FileInfo{
			Name:    filepath.Base(path),
			Size:    st.Size(),
			ModTime: st.ModTime(),
			Mode:    uint32(st.Mode().Perm()),
		},
		Open: func() (io.ReadSeekCloser, error) { return os.Open(path) },
	}, nil
}

// EventKind identifies a transfer event.
type EventKind int

// Event kinds. Every transfer ends with exactly one Done (success) or Error
// (failure, including cancellation) event.
const (
	FileStart   EventKind = iota // a file is about to be transferred
	Progress                     // periodic progress, at most about 10 per second
	FileDone                     // the current file was transferred completely
	FileSkipped                  // the current file was skipped (Err may say why)
	Error                        // the transfer failed; Err is the returned error
	Done                         // the transfer completed successfully
)

// String returns the name of the event kind.
func (k EventKind) String() string {
	switch k {
	case FileStart:
		return "FileStart"
	case Progress:
		return "Progress"
	case FileDone:
		return "FileDone"
	case FileSkipped:
		return "FileSkipped"
	case Error:
		return "Error"
	case Done:
		return "Done"
	}
	return "EventKind(?)"
}

// Event reports transfer progress to Options.OnEvent.
type Event struct {
	Kind  EventKind
	File  FileInfo
	Bytes int64 // bytes transferred so far for the current file
	Index int   // index of the file in the batch
	Err   error
}

// Options tunes a transfer. The zero value is usable.
type Options struct {
	// OnEvent, if not nil, receives progress events. It is called from the
	// goroutine running the transfer and must not block for long.
	OnEvent func(Event)
	// Timeout is the per-packet/header timeout (default 10s).
	Timeout time.Duration
	// Retries is the number of consecutive failures tolerated before the
	// transfer is abandoned (default 10).
	Retries int
	// Leftover, if not nil, is called at most once just before the transfer
	// function returns with bytes that were read from the stream but are not
	// part of the protocol, so the host can feed them to the terminal. If it
	// is nil and the stream implements Unreader the bytes are unread instead.
	Leftover func([]byte)

	// ZMODEM specifics.

	// WindowSize is the sender's transmit window in bytes: the sender asks
	// for acknowledgements and never runs more than WindowSize bytes ahead
	// of them. 0 means full streaming.
	WindowSize int
	// EscapeControl escapes all control characters (ZCTLESC), for links that
	// are not transparent to them.
	EscapeControl bool
	// BlockSize is the maximum data subpacket size, default 1024 (up to 8192).
	BlockSize int
	// CRC16 forces 16-bit CRCs even when both sides support CRC-32.
	CRC16 bool
	// Overwrite (send) asks the receiver to replace existing files.
	Overwrite bool
	// Resume (send) asks the receiver for crash recovery: a receiver that
	// already holds the beginning of the file continues where it stopped.
	// On the receiving side resumption is decided by the Sink.
	Resume bool

	// X/YMODEM specifics.

	// Use1K selects XMODEM-1K / YMODEM 1024-byte blocks when sending.
	Use1K bool
	// UseCRC makes the XMODEM receiver ask for CRC-16 (it sends 'C'); it
	// falls back to the arithmetic checksum if the sender does not answer.
	UseCRC bool
	// YmodemG makes the YMODEM receiver ask for YMODEM-G streaming (no
	// per-block acknowledgements, no error recovery).
	YmodemG bool
	// TrimPadding strips the trailing SUB (0x1A) padding of the final block
	// when the file size is unknown (XMODEM, or YMODEM without a length).
	// Files that genuinely end in 0x1A bytes lose those bytes.
	TrimPadding bool
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Retries <= 0 {
		o.Retries = 10
	}
	if o.BlockSize <= 0 {
		o.BlockSize = 1024
	}
	if o.BlockSize > maxSubpacket {
		o.BlockSize = maxSubpacket
	}
	if o.WindowSize < 0 {
		o.WindowSize = 0
	}
	return o
}

// abortSequence is the standard cancel sequence: eight CAN characters to
// cancel the remote program followed by eight backspaces that erase the CANs
// should they reach a command line instead.
var abortSequence = []byte{
	can, can, can, can, can, can, can, can,
	bs, bs, bs, bs, bs, bs, bs, bs,
}

// Abort writes the standard cancel sequence (CAN x8 + BS x8 for ZMODEM). It
// cancels a remote XMODEM, YMODEM or ZMODEM program.
func Abort(w io.Writer) {
	_, _ = w.Write(abortSequence)
}

// session holds the state shared by all protocols for one transfer call.
type session struct {
	ctx context.Context
	s   Stream
	rd  *lineReader
	o   Options

	cur          FileInfo // file currently being transferred
	idx          int      // its index in the batch
	lastProgress time.Time
	scratch      []byte
}

func newSession(ctx context.Context, s Stream, o Options) *session {
	return &session{
		ctx: ctx,
		s:   s,
		rd:  newLineReader(ctx, s),
		o:   o.withDefaults(),
	}
}

// write sends p to the remote unless the context has been cancelled.
func (x *session) write(p []byte) error {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	_, err := x.s.Write(p)
	return err
}

func (x *session) writeByte(c byte) error {
	b := [1]byte{c}
	return x.write(b[:])
}

func (x *session) emit(e Event) {
	if x.o.OnEvent != nil {
		x.o.OnEvent(e)
	}
}

func (x *session) fileEvent(kind EventKind, bytes int64, err error) {
	x.emit(Event{Kind: kind, File: x.cur, Index: x.idx, Bytes: bytes, Err: err})
}

// progress emits a throttled Progress event.
func (x *session) progress(bytes int64) {
	if x.o.OnEvent == nil {
		return
	}
	now := time.Now()
	if now.Sub(x.lastProgress) < 100*time.Millisecond {
		return
	}
	x.lastProgress = now
	x.o.OnEvent(Event{Kind: Progress, File: x.cur, Index: x.idx, Bytes: bytes})
}

// end finishes a transfer: it cancels the remote if the transfer failed on
// our side, emits the final event, stops the pump and hands unconsumed bytes
// back to the host.
func (x *session) end(err error) error {
	if err != nil {
		if cerr := x.ctx.Err(); cerr != nil {
			err = cerr
		}
		switch {
		case errors.Is(err, ErrCanceled):
			// The remote cancelled; swallow the rest of its cancel sequence.
			x.eatCancelTail()
		case x.streamFailed():
			// Nothing can be sent or drained any more.
		default:
			Abort(x.s)
			x.drainAfterAbort()
		}
		x.emit(Event{Kind: Error, File: x.cur, Index: x.idx, Err: err})
	} else {
		x.emit(Event{Kind: Done, Index: x.idx})
	}
	if left := x.rd.close(); len(left) > 0 {
		if x.o.Leftover != nil {
			x.o.Leftover(left)
		} else if u, ok := x.s.(Unreader); ok {
			u.Unread(left)
		}
	}
	return err
}

func (x *session) streamFailed() bool {
	x.rd.mu.Lock()
	defer x.rd.mu.Unlock()
	return x.rd.qerr != nil
}

// eatCancelTail consumes the remainder of a remote cancel sequence (CAN and
// BS characters) so that it does not reach the terminal.
func (x *session) eatCancelTail() {
	x.rd.ctx = context.Background()
	for range 64 {
		c, err := x.rd.get(100 * time.Millisecond)
		if err != nil {
			return
		}
		if c != can && c != bs {
			x.rd.unget()
			return
		}
	}
}

// drainAfterAbort discards what the remote still had in flight when we
// cancelled it, so that megabytes of file data do not flood the terminal.
// Draining stops when the line has been quiet for a moment. If the remote
// acknowledges with its own cancel sequence (lrzsz does), draining stops
// right there and whatever follows - typically the shell prompt - is kept.
func (x *session) drainAfterAbort() {
	x.rd.ctx = context.Background()
	quiet := min(max(x.o.Timeout/4, 50*time.Millisecond), 300*time.Millisecond)
	deadline := time.Now().Add(10 * quiet)
	cans := 0
	for n := 0; ; n++ {
		if n&0xfff == 0 && time.Now().After(deadline) {
			return
		}
		c, err := x.rd.get(quiet)
		if err != nil {
			return
		}
		if c == can {
			cans++
			continue
		}
		if cans >= 5 {
			for c == bs || c == can {
				if c, err = x.rd.get(100 * time.Millisecond); err != nil {
					return
				}
			}
			x.rd.unget()
			return
		}
		cans = 0
	}
}

// purge discards input until the line has been quiet for the given time.
// It is used to resynchronise X/YMODEM after a damaged block.
func (x *session) purge(quiet time.Duration) {
	deadline := time.Now().Add(20 * quiet)
	for n := 0; ; n++ {
		if n&0xff == 0 && time.Now().After(deadline) {
			return
		}
		if _, err := x.rd.get(quiet); err != nil {
			return
		}
	}
}
