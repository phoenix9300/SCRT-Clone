//go:build !windows

package conn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

type localTransport struct {
	f    *os.File
	cmd  *exec.Cmd
	once sync.Once
	wait chan struct{}
}

// DefaultShell returns the user's login shell.
func DefaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	for _, s := range []string{"/bin/bash", "/bin/zsh", "/bin/sh"} {
		if _, err := os.Stat(s); err == nil {
			return s
		}
	}
	return "/bin/sh"
}

func dialLocal(ctx context.Context, req *DialRequest) (Transport, error) {
	o := req.Opts.Local
	shell := strings.TrimSpace(o.Shell)
	if shell == "" {
		shell = DefaultShell()
	}
	cmd := exec.Command(shell, o.Args...)
	if o.Login && len(o.Args) == 0 {
		// login shells are started with a leading dash in argv[0]
		cmd.Args = []string{"-" + filepath.Base(shell)}
	}
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") || strings.HasPrefix(kv, "COLORTERM=") || strings.HasPrefix(kv, "TERM_PROGRAM=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	filtered = append(filtered, "TERM="+req.TermType(), "TERM_PROGRAM=Hawser")
	if req.Opts.Terminal.Emulation == "xterm" || req.Opts.Terminal.Emulation == "" {
		filtered = append(filtered, "COLORTERM=truecolor")
	}
	for k, v := range o.Env {
		filtered = append(filtered, k+"="+v)
	}
	cmd.Env = filtered
	if o.Cwd != "" {
		cmd.Dir = expandHome(o.Cwd)
	} else if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cols, rows := req.Cols, req.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("cannot start %s: %w", shell, err)
	}
	t := &localTransport{f: f, cmd: cmd, wait: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(t.wait)
	}()
	return t, nil
}

func (t *localTransport) Read(p []byte) (int, error) {
	n, err := t.f.Read(p)
	if err != nil {
		// Linux reports EIO when the child side of the pty closes
		var pe *os.PathError
		if errors.As(err, &pe) && pe.Err == syscall.EIO {
			<-t.wait
			if code := t.cmd.ProcessState.ExitCode(); code > 0 {
				return n, &ExitStatusError{Code: code}
			}
			return n, errProcessExited
		}
	}
	return n, err
}

func (t *localTransport) Write(p []byte) (int, error) { return t.f.Write(p) }

func (t *localTransport) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return pty.Setsize(t.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (t *localTransport) Close() error {
	t.once.Do(func() {
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Signal(syscall.SIGHUP)
		}
		_ = t.f.Close()
	})
	return nil
}

func (t *localTransport) Info() map[string]string {
	out := map[string]string{"command": strings.Join(t.cmd.Args, " ")}
	if t.cmd.Process != nil {
		out["pid"] = fmt.Sprint(t.cmd.Process.Pid)
	}
	return out
}
