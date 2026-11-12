package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/xfer"
)

// diverter hands a connection's byte stream to an in-band file transfer
// (X/Y/ZMODEM) and gives it back afterwards.
type diverter struct {
	c  *Conn
	mu sync.Mutex
	// queue holds chunks not yet consumed by the transfer
	queue  [][]byte
	notify chan struct{}
	err    error
}

func (d *diverter) push(p []byte) {
	cp := make([]byte, len(p))
	copy(cp, p)
	d.mu.Lock()
	d.queue = append(d.queue, cp)
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

func (d *diverter) fail(err error) {
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

// Unread implements xfer.Unreader.
func (d *diverter) Unread(p []byte) {
	if len(p) == 0 {
		return
	}
	cp := make([]byte, len(p))
	copy(cp, p)
	d.mu.Lock()
	d.queue = append([][]byte{cp}, d.queue...)
	d.mu.Unlock()
}

// ReadTimeout implements xfer.Stream.
func (d *diverter) ReadTimeout(p []byte, timeout time.Duration) (int, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		d.mu.Lock()
		if len(d.queue) > 0 {
			n := copy(p, d.queue[0])
			if n == len(d.queue[0]) {
				d.queue = d.queue[1:]
			} else {
				d.queue[0] = d.queue[0][n:]
			}
			d.mu.Unlock()
			return n, nil
		}
		err := d.err
		d.mu.Unlock()
		if err != nil {
			return 0, err
		}
		select {
		case <-d.notify:
		case <-deadline.C:
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// Write implements xfer.Stream (bytes to the remote side).
func (d *diverter) Write(p []byte) (int, error) {
	d.c.mu.Lock()
	t := d.c.transport
	d.c.mu.Unlock()
	if t == nil {
		return 0, io.ErrClosedPipe
	}
	n, err := t.Write(p)
	d.c.bytesOut.Add(int64(n))
	return n, err
}

func (c *Conn) beginDivert(initial []byte) (*diverter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.divert != nil {
		return nil, errors.New("a file transfer is already in progress")
	}
	if c.transport == nil {
		return nil, errors.New("not connected")
	}
	d := &diverter{c: c, notify: make(chan struct{}, 1)}
	if len(initial) > 0 {
		d.queue = append(d.queue, append([]byte(nil), initial...))
	}
	c.divert = d
	return d, nil
}

func (c *Conn) endDivert(d *diverter) {
	c.mu.Lock()
	if c.divert == d {
		c.divert = nil
	}
	c.mu.Unlock()
	// anything the transfer did not consume belongs to the terminal
	d.mu.Lock()
	rest := d.queue
	d.queue = nil
	d.mu.Unlock()
	for _, chunk := range rest {
		c.emit(chunk, true)
	}
}

// zmodemDetector wraps the protocol detector with the per-session switch.
type zmodemDetector struct {
	det xfer.Detector
}

// TransferEvent is broadcast to the UI while a file transfer runs.
type TransferEvent struct {
	ID        uint32 `json:"id"`
	Transfer  string `json:"transfer"`
	Protocol  string `json:"protocol"`
	Direction string `json:"direction"` // upload | download
	Kind      string `json:"kind"`      // start | file | progress | fileDone | skipped | done | error
	File      string `json:"file,omitempty"`
	Size      int64  `json:"size"`
	Bytes     int64  `json:"bytes"`
	Index     int    `json:"index"`
	Count     int    `json:"count,omitempty"`
	Error     string `json:"error,omitempty"`
	Dir       string `json:"dir,omitempty"`
}

var transferSeq struct {
	sync.Mutex
	n int
}

func nextTransferID() string {
	transferSeq.Lock()
	defer transferSeq.Unlock()
	transferSeq.n++
	return fmt.Sprintf("x%d", transferSeq.n)
}

// transfers currently running, for cancellation
var activeTransfers sync.Map // transfer id -> context.CancelFunc

// CancelTransfer aborts a running in-band transfer.
func CancelTransfer(id string) bool {
	if v, ok := activeTransfers.Load(id); ok {
		v.(context.CancelFunc)()
		return true
	}
	return false
}

func (c *Conn) downloadDir() string {
	o := c.Options().Transfer
	dir := o.DownloadDir
	if dir == "" {
		dir = c.mgr.Store.GlobalString("downloadDir", "")
	}
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, "Downloads")
		} else {
			dir = os.TempDir()
		}
	}
	return expandHome(dir)
}

// checkZmodem looks for the start of a ZMODEM session in host output. It
// returns the part of p that should still be printed and whether the chunk
// was taken over.
func (c *Conn) checkZmodem(p []byte) (rest []byte, handled bool) {
	if !c.Options().Transfer.ZmodemAuto {
		return nil, false
	}
	kind, idx := c.zdetect.det.Feed(p)
	if kind == xfer.DetectNone {
		return nil, false
	}
	c.zdetect.det.Reset()
	if idx < 0 || idx > len(p) {
		idx = 0
	}
	switch kind {
	case xfer.DetectReceive:
		d, err := c.beginDivert(p[idx:])
		if err != nil {
			return nil, false
		}
		go c.runTransfer(d, "zmodem", "download", nil, nil)
	case xfer.DetectSend:
		// The remote "rz" is waiting for files: ask the UI which ones.
		d, err := c.beginDivert(p[idx:])
		if err != nil {
			return nil, false
		}
		go c.zmodemUploadPrompt(d)
	}
	return p[:idx], true
}

// pendingUploads holds diverted sessions waiting for the user to pick files.
var pendingUploads sync.Map // conn id -> chan []string

// QueueZmodemUpload registers files to send automatically the next time the
// remote side starts a ZMODEM receive (scripts: crt.FileTransfer).
func (c *Conn) QueueZmodemUpload(files []string) {
	c.mu.Lock()
	c.zqueue = append(c.zqueue, files...)
	c.mu.Unlock()
}

// ClearZmodemUpload empties the queued upload list.
func (c *Conn) ClearZmodemUpload() {
	c.mu.Lock()
	c.zqueue = nil
	c.mu.Unlock()
}

func (c *Conn) zmodemUploadPrompt(d *diverter) {
	c.mu.Lock()
	queued := c.zqueue
	c.zqueue = nil
	c.mu.Unlock()
	if len(queued) > 0 {
		c.runTransfer(d, "zmodem", "upload", queued, nil)
		return
	}
	ch := make(chan []string, 1)
	pendingUploads.Store(c.ID, ch)
	defer pendingUploads.Delete(c.ID)
	c.mgr.Broadcast("xfer.uploadRequest", map[string]any{"id": c.ID, "protocol": "zmodem"})
	var files []string
	select {
	case files = <-ch:
	case <-time.After(5 * time.Minute):
	}
	if len(files) == 0 {
		xfer.Abort(d)
		// swallow the receiver's reaction to the abort
		buf := make([]byte, 4096)
		for {
			if _, err := d.ReadTimeout(buf, 300*time.Millisecond); err != nil {
				break
			}
		}
		c.endDivert(d)
		return
	}
	c.runTransfer(d, "zmodem", "upload", files, nil)
}

// AnswerUpload supplies the files for a pending ZMODEM upload request
// (an empty list cancels it).
func (c *Conn) AnswerUpload(files []string) bool {
	if v, ok := pendingUploads.Load(c.ID); ok {
		select {
		case v.(chan []string) <- files:
			return true
		default:
		}
	}
	return false
}

// TransferRequest starts an in-band transfer from the UI.
type TransferRequest struct {
	Protocol  string   `json:"protocol"`  // zmodem | ymodem | xmodem | kermit | ascii
	Direction string   `json:"direction"` // upload | download
	Files     []string `json:"files"`     // local files to send
	Dest      string   `json:"dest"`      // xmodem download: local file to write
}

// StartTransfer begins a user-initiated X/Y/ZMODEM transfer.
func (c *Conn) StartTransfer(req TransferRequest) error {
	if req.Direction == "upload" && len(req.Files) == 0 {
		return errors.New("no files selected")
	}
	if req.Protocol == "xmodem" && req.Direction == "download" && req.Dest == "" {
		return errors.New("XMODEM needs a destination file name")
	}
	d, err := c.beginDivert(nil)
	if err != nil {
		return err
	}
	go c.runTransfer(d, req.Protocol, req.Direction, req.Files, &req)
	return nil
}

func (c *Conn) runTransfer(d *diverter, protocol, direction string, files []string, req *TransferRequest) {
	id := nextTransferID()
	ctx, cancel := context.WithCancel(context.Background())
	activeTransfers.Store(id, cancel)
	defer func() {
		cancel()
		activeTransfers.Delete(id)
		c.endDivert(d)
	}()

	to := c.Options().Transfer
	base := TransferEvent{ID: c.ID, Transfer: id, Protocol: protocol, Direction: direction, Count: len(files)}
	send := func(ev TransferEvent) { c.mgr.Broadcast("xfer.progress", ev) }
	dir := c.downloadDir()
	start := base
	start.Kind = "start"
	if direction == "download" {
		start.Dir = dir
	}
	send(start)

	opts := xfer.Options{
		Overwrite: to.ZmodemOverwrite, Resume: to.ZmodemResume,
		Use1K: to.Xmodem1K, UseCRC: to.XmodemCRC, TrimPadding: true,
		OnEvent: func(e xfer.Event) {
			ev := base
			ev.File, ev.Size, ev.Bytes, ev.Index = e.File.Name, e.File.Size, e.Bytes, e.Index
			switch e.Kind {
			case xfer.FileStart:
				ev.Kind = "file"
			case xfer.Progress:
				ev.Kind = "progress"
			case xfer.FileDone:
				ev.Kind = "fileDone"
			case xfer.FileSkipped:
				ev.Kind = "skipped"
			case xfer.Error:
				ev.Kind = "error"
				if e.Err != nil {
					ev.Error = e.Err.Error()
				}
			default:
				return
			}
			send(ev)
		},
	}

	var err error
	switch {
	case direction == "download" && (protocol == "zmodem" || protocol == "ymodem" || protocol == "kermit"):
		if merr := os.MkdirAll(dir, 0o755); merr != nil {
			err = merr
			xfer.Abort(d)
			break
		}
		sink := &xfer.DirSink{Dir: dir, Overwrite: to.ZmodemOverwrite, Resume: to.ZmodemResume}
		switch protocol {
		case "zmodem":
			err = xfer.ZmodemReceive(ctx, d, sink, opts)
		case "kermit":
			err = xfer.KermitReceive(ctx, d, sink, opts)
		default:
			err = xfer.YmodemReceive(ctx, d, sink, opts)
		}
	case direction == "download" && protocol == "xmodem":
		var f *os.File
		dest := expandHome(req.Dest)
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(dir, dest)
		}
		if f, err = os.Create(dest); err == nil {
			ev := base
			ev.Kind, ev.File, ev.Size = "file", filepath.Base(dest), -1
			send(ev)
			var n int64
			n, err = xfer.XmodemReceive(ctx, d, f, opts)
			_ = f.Close()
			ev.Kind, ev.Bytes = "fileDone", n
			if err == nil {
				send(ev)
			}
		}
	case direction == "upload":
		var sf []xfer.SendFile
		for _, p := range files {
			f, ferr := xfer.SendFileFromPath(expandHome(p))
			if ferr != nil {
				err = ferr
				break
			}
			sf = append(sf, f)
		}
		if err != nil {
			if protocol == "zmodem" {
				xfer.Abort(d)
			}
			break
		}
		switch protocol {
		case "zmodem":
			err = xfer.ZmodemSend(ctx, d, sf, opts)
		case "ymodem":
			err = xfer.YmodemSend(ctx, d, sf, opts)
		case "kermit":
			err = xfer.KermitSend(ctx, d, sf, opts)
		case "xmodem":
			var r io.ReadSeekCloser
			if r, err = sf[0].Open(); err == nil {
				ev := base
				ev.Kind, ev.File, ev.Size = "file", sf[0].Info.Name, sf[0].Info.Size
				send(ev)
				var n int64
				n, err = xfer.XmodemSend(ctx, d, r, opts)
				_ = r.Close()
				ev.Kind, ev.Bytes = "fileDone", n
				if err == nil {
					send(ev)
				}
			}
		default:
			err = fmt.Errorf("unknown transfer protocol %q", protocol)
		}
	default:
		err = fmt.Errorf("unsupported transfer %s %s", protocol, direction)
	}

	done := base
	done.Kind = "done"
	if err != nil {
		done.Kind = "error"
		done.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			done.Error = "cancelled"
		}
	}
	send(done)
}

// SendASCII sends a text file as if it were typed (honouring send delays).
func (c *Conn) SendASCII(path string) error {
	data, err := os.ReadFile(expandHome(path))
	if err != nil {
		return err
	}
	o := c.Options().Terminal
	// normalise line endings to what the host expects from the Enter key
	out := make([]byte, 0, len(data))
	nl := "\r"
	switch o.NewlineOnSend {
	case "lf":
		nl = "\n"
	case "crlf":
		nl = "\r\n"
	}
	for i := 0; i < len(data); i++ {
		b := data[i]
		if b == '\r' {
			if i+1 < len(data) && data[i+1] == '\n' {
				i++
			}
			out = append(out, nl...)
		} else if b == '\n' {
			out = append(out, nl...)
		} else {
			out = append(out, b)
		}
	}
	go c.Send(out, time.Duration(o.CharSendDelay)*time.Millisecond, time.Duration(o.LineSendDelay)*time.Millisecond)
	return nil
}
