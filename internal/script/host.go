// Package script is Hawser's scripting host: one implementation of the crt
// object model with Python, JavaScript and VBScript front ends.
package script

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
)

// UI is everything a script needs from the window.
type UI interface {
	MessageBox(ctx context.Context, msg, title string, buttons int) (int, error)
	Prompt(ctx context.Context, msg, title, def string, password bool) (value string, ok bool, err error)
	FileOpen(ctx context.Context, title, button, defName, filter string) (string, error)
	// ScreenText returns the 1-based inclusive region, rows joined by "\n".
	ScreenText(ctx context.Context, conn uint32, r1, c1, r2, c2 int) (string, error)
	ScreenInfo(ctx context.Context, conn uint32) (rows, cols, curRow, curCol int, selection string, err error)
	Tabs(ctx context.Context) (ids []uint32, active uint32, err error)
	ActivateTab(ctx context.Context, conn uint32) error
	// SetCaption with conn 0 sets the window title.
	SetCaption(ctx context.Context, conn uint32, caption string) error
	GetCaption(ctx context.Context, conn uint32) (string, error)
	Clipboard(ctx context.Context) (string, error)
	SetClipboard(ctx context.Context, text string) error
	Status(text string)
	// OpenTab opens a connection and makes the window show it.
	OpenTab(ctx context.Context, req conn.OpenRequest) (*conn.Conn, error)
	OpenSFTP(ctx context.Context, conn uint32) error
}

// Run states.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
	StateStopped = "stopped"
)

const maxOutput = 64 << 10

// RunRequest describes a script to start.
type RunRequest struct {
	Conn   uint32   `json:"conn"`
	Path   string   `json:"path"`
	Source string   `json:"source"`
	Lang   string   `json:"lang"`
	Args   []string `json:"args"`
}

// RunInfo is the externally visible state of a run.
type RunInfo struct {
	ID     string `json:"id"`
	Conn   uint32 `json:"conn"`
	Path   string `json:"path"`
	Lang   string `json:"lang"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// Host starts and tracks script runs.
type Host struct {
	Mgr *conn.Manager
	UI  UI
	// Python is the interpreter to use (empty: python3, then python).
	Python string
	// PythonFunc, when set, is consulted at each start instead of Python.
	PythonFunc func() string
	// OnState is called when a run starts and when it finishes.
	OnState func(RunInfo)

	mu   sync.Mutex
	runs []*Run
	seq  int
}

// Run is one script execution.
type Run struct {
	h      *Host
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	info    RunInfo
	out     []byte
	stopped bool
}

// Info snapshots the run.
func (r *Run) Info() RunInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := r.info
	i.Output = string(r.out)
	return i
}

// Done is closed when the run has finished.
func (r *Run) Done() <-chan struct{} { return r.done }

// Wait blocks until the run has finished and returns its final state.
func (r *Run) Wait() RunInfo { <-r.done; return r.Info() }

// Write appends captured output (capped).
func (r *Run) Write(p []byte) (int, error) {
	r.mu.Lock()
	if room := maxOutput - len(r.out); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		r.out = append(r.out, p[:room]...)
	}
	r.mu.Unlock()
	return len(p), nil
}

var langHeader = regexp.MustCompile(`(?i)\$language\s*=\s*"?([A-Za-z]+)"?`)

// detectLang picks the language from the extension or a header comment.
func detectLang(req RunRequest) string {
	switch strings.ToLower(req.Lang) {
	case "python", "py":
		return "python"
	case "javascript", "jscript", "js":
		return "javascript"
	case "vbscript", "vbs", "vb":
		return "vbscript"
	}
	switch strings.ToLower(filepath.Ext(req.Path)) {
	case ".py":
		return "python"
	case ".js":
		return "javascript"
	case ".vbs":
		return "vbscript"
	}
	lines := strings.SplitN(req.Source, "\n", 12)
	for _, l := range lines {
		if m := langHeader.FindStringSubmatch(l); m != nil {
			switch strings.ToLower(m[1]) {
			case "vbscript":
				return "vbscript"
			case "jscript", "javascript":
				return "javascript"
			}
			return "python"
		}
	}
	return "python"
}

// Start launches a script on a connection.
func (h *Host) Start(req RunRequest) (*Run, error) {
	cn := h.Mgr.Get(req.Conn)
	if cn == nil {
		return nil, errors.New("the session no longer exists")
	}
	if req.Source == "" {
		if req.Path == "" {
			return nil, errors.New("no script given")
		}
		b, err := os.ReadFile(req.Path)
		if err != nil {
			return nil, err
		}
		req.Source = string(b)
	}
	req.Source = strings.TrimPrefix(req.Source, "\xef\xbb\xbf")
	lang := detectLang(req)
	ctx, cancel := context.WithCancel(context.Background())
	r := &Run{h: h, cancel: cancel, done: make(chan struct{})}
	h.mu.Lock()
	for _, o := range h.runs {
		if o.info.Conn != req.Conn {
			continue
		}
		select {
		case <-o.done:
		default:
			h.mu.Unlock()
			cancel()
			return nil, fmt.Errorf("a script (%s) is already running on this session; stop it first", o.info.ID)
		}
	}
	h.seq++
	r.info = RunInfo{ID: fmt.Sprintf("run%d", h.seq), Conn: req.Conn, Path: req.Path, Lang: lang, State: StateRunning}
	h.runs = append(h.runs, r)
	if len(h.runs) > 100 { // forget the oldest finished runs
		keep := h.runs[:0]
		drop := len(h.runs) - 100
		for _, o := range h.runs {
			select {
			case <-o.done:
				if drop > 0 {
					drop--
					continue
				}
			default:
			}
			keep = append(keep, o)
		}
		h.runs = keep
	}
	h.mu.Unlock()
	h.notify(r)
	e := &env{h: h, ctx: ctx, run: r, cn: cn, path: req.Path, args: req.Args}
	go func() { // end the run when its tab is closed
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if h.Mgr.Get(req.Conn) == nil {
					r.stop()
					return
				}
			}
		}
	}()
	go func() {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("internal error: %v", p)
				}
			}()
			switch lang {
			case "javascript":
				return runJS(e, req.Source)
			case "vbscript":
				return runVBS(e, req.Source)
			}
			return runPython(e, req.Source)
		}()
		r.mu.Lock()
		switch {
		case r.stopped:
			r.info.State = StateStopped
		case err != nil && !errors.Is(err, errQuit):
			r.info.State, r.info.Error = StateError, err.Error()
		default:
			r.info.State = StateDone
		}
		r.mu.Unlock()
		cancel()
		close(r.done)
		h.notify(r)
	}()
	return r, nil
}

func (h *Host) notify(r *Run) {
	if h.OnState != nil {
		h.OnState(r.Info())
	}
}

// Stop cancels a run by id.
func (h *Host) Stop(runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runs {
		if r.info.ID == runID {
			r.stop()
		}
	}
}

// StopConn cancels every run on a connection.
func (h *Host) StopConn(connID uint32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.runs {
		if r.info.Conn == connID {
			r.stop()
		}
	}
}

// StopAll cancels every run and waits briefly for them to end.
func (h *Host) StopAll() {
	h.mu.Lock()
	runs := append([]*Run(nil), h.runs...)
	h.mu.Unlock()
	for _, r := range runs {
		r.stop()
	}
	for _, r := range runs {
		select {
		case <-r.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *Run) stop() {
	select {
	case <-r.done:
		return
	default:
	}
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()
	r.cancel()
}

// Runs lists all known runs, oldest first.
func (h *Host) Runs() []RunInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RunInfo, 0, len(h.runs))
	for _, r := range h.runs {
		out = append(out, r.Info())
	}
	return out
}

func (h *Host) python() string {
	if h.PythonFunc != nil {
		if p := h.PythonFunc(); p != "" {
			return p
		}
	}
	return h.Python
}
