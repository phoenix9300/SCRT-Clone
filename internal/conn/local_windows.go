//go:build windows

package conn

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/UserExistsError/conpty"
)

type localTransport struct {
	cpty *conpty.ConPty
	cmd  string
	once sync.Once
}

// DefaultShell returns the preferred interactive shell on Windows.
func DefaultShell() string {
	for _, s := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := lookPath(s); err == nil {
			return p
		}
	}
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return "cmd.exe"
}

func dialLocal(ctx context.Context, req *DialRequest) (Transport, error) {
	o := req.Opts.Local
	shell := strings.TrimSpace(o.Shell)
	if shell == "" {
		shell = DefaultShell()
	}
	cmdline := shell
	if strings.ContainsAny(shell, " \t") && !strings.HasPrefix(shell, `"`) {
		cmdline = `"` + shell + `"`
	}
	for _, a := range o.Args {
		if strings.ContainsAny(a, " \t\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		cmdline += " " + a
	}
	cols, rows := req.Cols, req.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	opts := []conpty.ConPtyOption{conpty.ConPtyDimensions(cols, rows)}
	if o.Cwd != "" {
		opts = append(opts, conpty.ConPtyWorkDir(expandHome(o.Cwd)))
	} else if home, err := os.UserHomeDir(); err == nil {
		opts = append(opts, conpty.ConPtyWorkDir(home))
	}
	if len(o.Env) > 0 {
		env := os.Environ()
		for k, v := range o.Env {
			env = append(env, k+"="+v)
		}
		opts = append(opts, conpty.ConPtyEnv(env))
	}
	c, err := conpty.Start(cmdline, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot start %s: %w", shell, err)
	}
	return &localTransport{cpty: c, cmd: cmdline}, nil
}

func (t *localTransport) Read(p []byte) (int, error)  { return t.cpty.Read(p) }
func (t *localTransport) Write(p []byte) (int, error) { return t.cpty.Write(p) }

func (t *localTransport) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return t.cpty.Resize(cols, rows)
}

func (t *localTransport) Close() error {
	var err error
	t.once.Do(func() { err = t.cpty.Close() })
	return err
}

func (t *localTransport) Info() map[string]string {
	return map[string]string{"command": t.cmd, "pid": fmt.Sprint(t.cpty.Pid())}
}
