package conn

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// LogContext supplies the values for log file name and line substitutions.
type LogContext struct {
	Session  string
	Host     string
	Port     int
	Username string
}

// ExpandLogPattern substitutes the % sequences used in log file names and
// custom log lines:
//
//	%H host   %S session name   %P port   %U username
//	%Y year (4)  %y year (2)  %M month  %D day  %h hour  %m minute  %s second
//	%t milliseconds   %%  literal percent
//
// When forFile is true, characters that are unsafe in file names are
// replaced in substituted values.
func ExpandLogPattern(pattern string, ctx LogContext, now time.Time, forFile bool) string {
	clean := func(s string) string {
		if !forFile {
			return s
		}
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
				return '_'
			}
			return r
		}, s)
	}
	var sb strings.Builder
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if ch != '%' || i+1 >= len(pattern) {
			sb.WriteByte(ch)
			continue
		}
		i++
		switch pattern[i] {
		case 'H':
			sb.WriteString(clean(ctx.Host))
		case 'S':
			name := ctx.Session
			if j := strings.LastIndexByte(name, '/'); j >= 0 {
				name = name[j+1:]
			}
			if name == "" {
				name = ctx.Host
			}
			sb.WriteString(clean(name))
		case 'P':
			fmt.Fprintf(&sb, "%d", ctx.Port)
		case 'U':
			sb.WriteString(clean(ctx.Username))
		case 'Y':
			fmt.Fprintf(&sb, "%04d", now.Year())
		case 'y':
			fmt.Fprintf(&sb, "%02d", now.Year()%100)
		case 'M':
			fmt.Fprintf(&sb, "%02d", int(now.Month()))
		case 'D':
			fmt.Fprintf(&sb, "%02d", now.Day())
		case 'h':
			fmt.Fprintf(&sb, "%02d", now.Hour())
		case 'm':
			fmt.Fprintf(&sb, "%02d", now.Minute())
		case 's':
			fmt.Fprintf(&sb, "%02d", now.Second())
		case 't':
			fmt.Fprintf(&sb, "%03d", now.Nanosecond()/1e6)
		case '%':
			sb.WriteByte('%')
		default:
			sb.WriteByte('%')
			sb.WriteByte(pattern[i])
		}
	}
	return sb.String()
}

// sessionLog writes a session's output to a file, either raw (every byte
// as received) or as readable text with control sequences removed.
type sessionLog struct {
	mu       sync.Mutex
	f        *os.File
	w        *bufio.Writer
	path     string
	pattern  string // unexpanded file name, for midnight rotation
	dir      string
	raw      bool
	opts     store.LogOptions
	ctx      LogContext
	line     []byte // current partial text line
	atStart  bool
	day      int
	timer    *time.Timer
	closed   bool
	lastByte byte
}

func (l *sessionLog) Path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

func openLogFile(path string, appendMode bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600)
}

func (l *sessionLog) resolve(now time.Time) string {
	p := ExpandLogPattern(l.pattern, l.ctx, now, true)
	p = expandHome(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(l.dir, p)
	}
	return p
}

func (l *sessionLog) custom(pattern string) {
	if pattern == "" {
		return
	}
	text := ExpandLogPattern(pattern, l.ctx, time.Now(), false)
	_, _ = l.w.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		_, _ = l.w.WriteString(lineEnding)
	}
}

// event writes the custom "on connect" / "on disconnect" line.
func (l *sessionLog) event(kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	if len(l.line) > 0 { // end a partial line, but never emit an empty one
		l.flushLineLocked(true)
	}
	switch kind {
	case "connect":
		l.custom(l.opts.OnConnect)
	case "disconnect":
		l.custom(l.opts.OnDisconnect)
	}
	_ = l.w.Flush()
}

func (l *sessionLog) rotateLocked(now time.Time) {
	if !l.opts.NewFileAtMidnight || now.YearDay() == l.day {
		return
	}
	l.day = now.YearDay()
	np := l.resolve(now)
	if np == l.path {
		return
	}
	l.flushLineLocked(true)
	_ = l.w.Flush()
	if f, err := openLogFile(np, l.opts.Append); err == nil {
		_ = l.f.Close()
		l.f = f
		l.w = bufio.NewWriterSize(f, 32*1024)
		l.path = np
	}
}

// write records output. data is the raw chunk, text its stripped form.
func (l *sessionLog) write(data, text []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	now := time.Now()
	l.rotateLocked(now)
	if l.raw {
		_, _ = l.w.Write(data)
	} else if !l.opts.OnlyCustom {
		for _, b := range text {
			switch b {
			case '\n':
				l.flushLineLocked(true)
			case '\r':
				// carriage returns are implied by the newline
			case 0x08:
				if n := len(l.line); n > 0 {
					l.line = l.line[:n-1]
				}
			default:
				l.line = append(l.line, b)
			}
			l.lastByte = b
		}
	}
	// write out promptly but not on every chunk
	if l.timer == nil {
		l.timer = time.AfterFunc(700*time.Millisecond, l.Flush)
	}
}

// flushLineLocked writes the pending text line; newline terminates it.
func (l *sessionLog) flushLineLocked(newline bool) {
	if l.raw {
		return
	}
	if len(l.line) == 0 && !newline {
		return
	}
	if l.atStart && l.opts.OnEachLine != "" {
		_, _ = l.w.WriteString(ExpandLogPattern(l.opts.OnEachLine, l.ctx, time.Now(), false))
	}
	_, _ = l.w.Write(l.line)
	l.line = l.line[:0]
	l.atStart = false
	if newline {
		_, _ = l.w.WriteString(lineEnding)
		l.atStart = true
	}
}

// Flush forces buffered log data to disk.
func (l *sessionLog) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.timer = nil
	if l.closed {
		return
	}
	_ = l.w.Flush()
}

func (l *sessionLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.flushLineLocked(false)
	if !l.raw && !l.atStart {
		_, _ = l.w.WriteString(lineEnding)
	}
	_ = l.w.Flush()
	_ = l.f.Close()
	l.closed = true
	if l.timer != nil {
		l.timer.Stop()
	}
}

// LogRequest starts logging with explicit settings; zero values fall back to
// the session's log options.
type LogRequest struct {
	File   string `json:"file"`
	Raw    *bool  `json:"raw"`
	Append *bool  `json:"append"`
}

func (c *Conn) logContext() LogContext {
	o := c.Options()
	host := o.Host
	switch o.Protocol {
	case "serial":
		host = filepath.Base(o.Serial.Port)
	case "local":
		host = "local"
	}
	return LogContext{Session: c.SessionPath, Host: host, Port: o.Port, Username: o.Username}
}

// StartLog begins logging session output and returns the file path.
func (c *Conn) StartLog(req LogRequest) (string, error) {
	o := c.Options().Log
	l := &sessionLog{opts: o, ctx: c.logContext(), dir: c.mgr.Store.LogsDir(), raw: o.Raw, atStart: true}
	if req.Raw != nil {
		l.raw = *req.Raw
	}
	appendMode := o.Append
	if req.Append != nil {
		appendMode = *req.Append
	}
	l.opts.Append = appendMode
	l.pattern = req.File
	if l.pattern == "" {
		l.pattern = o.File
	}
	if l.pattern == "" {
		return "", errors.New("no log file name configured")
	}
	now := time.Now()
	l.day = now.YearDay()
	l.path = l.resolve(now)
	f, err := openLogFile(l.path, appendMode)
	if err != nil {
		return "", err
	}
	l.f = f
	l.w = bufio.NewWriterSize(f, 32*1024)
	c.mu.Lock()
	old := c.log
	c.log = l
	connected := c.state == StateConnected
	c.mu.Unlock()
	if old != nil {
		old.close()
	}
	if connected {
		l.event("connect")
	}
	c.mgr.Broadcast("conn.state", c.Info(false))
	return l.path, nil
}

// StopLog ends logging.
func (c *Conn) StopLog() {
	c.mu.Lock()
	l := c.log
	c.log = nil
	dead := c.dead
	c.mu.Unlock()
	if l != nil {
		l.close()
		if !dead {
			c.mgr.Broadcast("conn.state", c.Info(false))
		}
	}
}

// LogPath returns the active log file ("" when not logging).
func (c *Conn) LogPath() string {
	c.mu.Lock()
	l := c.log
	c.mu.Unlock()
	if l == nil {
		return ""
	}
	return l.Path()
}

func (c *Conn) startAutoLog() {
	o := c.Options().Log
	c.mu.Lock()
	l := c.log
	c.mu.Unlock()
	if l != nil {
		l.event("connect")
		return
	}
	if !o.StartOnConnect || o.PromptForFile {
		return
	}
	if _, err := c.StartLog(LogRequest{}); err != nil {
		connPrompter{c}.Notice("Cannot start logging: " + err.Error() + "\r\n")
	}
}
