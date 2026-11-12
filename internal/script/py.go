package script

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed shim.py
var shimSource string

var (
	tableOnce   sync.Once
	methodTable map[string][]string
)

// methodNames lists the method names of every object type (sent to the shim).
func methodNames() map[string][]string {
	tableOnce.Do(func() {
		e := &env{}
		objs := []*object{
			newCRT(e), newScreen(e, nil), newSession(e, nil), newDialog(e), newWindow(e),
			newTab(e, nil), newArguments(e), newClipboard(e), newFileTransfer(e), newCommandWindow(e),
		}
		methodTable = map[string][]string{}
		for _, o := range objs {
			methodTable[o.typ] = append([]string{}, o.methodNames...)
		}
		methodTable["SessionConfiguration"] = []string{"GetOption", "SetOption", "Save", "ConnectInTab"}
	})
	return methodTable
}

func findPython(configured string) (string, error) {
	if configured != "" {
		if p, err := exec.LookPath(configured); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("Python interpreter %q not found (check the Python path in the options)", configured)
	}
	for _, n := range []string{"python3", "python"} {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no Python interpreter found: install Python 3 or set the Python path in the options")
}

// handles maps object handles to objects for one Python process.
type handles struct {
	byID  map[int]*object
	byObj map[*object]int
}

func (h *handles) id(o *object) int {
	if id, ok := h.byObj[o]; ok {
		return id
	}
	id := len(h.byID) + 1
	h.byID[id], h.byObj[o] = o, id
	return id
}

func (h *handles) enc(v any) any {
	switch x := v.(type) {
	case *object:
		return map[string]any{"$obj": h.id(x), "type": x.typ}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = h.enc(e)
		}
		return out
	}
	return v
}

func (h *handles) dec(v any) any {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return int64(x)
		}
	case map[string]any:
		if id, ok := x["$obj"].(float64); ok {
			if o := h.byID[int(id)]; o != nil {
				return o
			}
		}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = h.dec(e)
		}
		return out
	}
	return v
}

type pyMsg struct {
	ID    int    `json:"id"`
	Op    string `json:"op"`
	Obj   int    `json:"obj"`
	Name  string `json:"name"`
	Args  []any  `json:"args"`
	Value any    `json:"value"`
	Text  string `json:"text"`
	Error string `json:"error"`
}

func runPython(e *env, src string) error {
	py, err := findPython(e.h.python())
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "hawser-py")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	shim := filepath.Join(dir, "hawser_shim.py")
	if err := os.WriteFile(shim, []byte(shimSource), 0o600); err != nil {
		return err
	}
	cmd := exec.CommandContext(e.ctx, py, "-u", shim)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr carries output of the script's child processes (the shim moves
	// their stdout there); show it as script output and keep it for crashes.
	var stderr limitedBuf
	cmd.Stderr = io.MultiWriter(&stderr, e.run)
	cmd.WaitDelay = 2 * time.Second // do not hang on grandchildren holding the pipes
	if err := cmd.Start(); err != nil {
		return err
	}
	var waitOnce sync.Once
	// finish also kills: a script's non-daemon thread must not keep the run alive.
	finish := func(kill bool) {
		waitOnce.Do(func() {
			_ = stdin.Close()
			if kill {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		})
	}
	defer finish(true)

	hs := &handles{byID: map[int]*object{}, byObj: map[*object]int{}}
	crt := newCRT(e)
	send := func(v any) error {
		b, _ := json.Marshal(v)
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	if err := send(map[string]any{
		"methods": methodNames(), "crt": hs.enc(crt), "path": e.path, "source": src, "args": e.args,
	}); err != nil {
		return err
	}
	rd := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, rerr := rd.ReadBytes('\n')
		if len(line) == 0 && rerr != nil {
			if err := e.ctx.Err(); err != nil {
				return err
			}
			finish(false)
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "the Python process ended unexpectedly"
			}
			if rerr != io.EOF {
				msg += ": " + rerr.Error()
			}
			return errors.New(msg)
		}
		var m pyMsg
		if err := json.Unmarshal(line, &m); err != nil || m.Op == "" {
			// stray output on the protocol stream
			_, _ = e.run.Write(line)
			continue
		}
		switch m.Op {
		case "output":
			_, _ = e.run.Write([]byte(m.Text))
		case "done":
			if m.Error != "" {
				return errors.New(strings.TrimRight(m.Error, "\r\n"))
			}
			return nil
		case "get", "set", "call":
			obj := hs.byID[m.Obj]
			if obj == nil {
				_ = send(map[string]any{"id": m.ID, "ok": false, "error": "unknown object"})
				continue
			}
			var res any
			var cerr error
			switch m.Op {
			case "get":
				res, cerr = obj.getProp(m.Name)
			case "set":
				cerr = obj.setProp(m.Name, hs.dec(m.Value))
			default:
				args := make([]any, len(m.Args))
				for i, a := range m.Args {
					args[i] = hs.dec(a)
				}
				res, cerr = obj.invoke(m.Name, args)
			}
			switch {
			case errors.Is(cerr, errQuit):
				_ = send(map[string]any{"id": m.ID, "ok": false, "quit": true})
			case cerr != nil:
				_ = send(map[string]any{"id": m.ID, "ok": false, "error": cerr.Error()})
			default:
				_ = send(map[string]any{"id": m.ID, "ok": true, "value": hs.enc(res)})
			}
		}
		if err := e.ctx.Err(); err != nil {
			return err
		}
	}
}

type limitedBuf struct {
	mu sync.Mutex
	b  []byte
}

func (l *limitedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	if len(l.b) < 16<<10 {
		l.b = append(l.b, p...)
	}
	l.mu.Unlock()
	return len(p), nil
}

func (l *limitedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}
