package vbs

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// registerBuiltinClasses installs the CreateObject classes implemented by
// this package.
func (in *Interpreter) registerBuiltinClasses() {
	in.factories["scripting.dictionary"] = func() (Object, error) { return NewDictionary(), nil }
	in.factories["scripting.filesystemobject"] = func() (Object, error) { return &fsoObject{in: in}, nil }
	in.factories["vbscript.regexp"] = func() (Object, error) { return &regExp{}, nil }
	in.factories["wscript.shell"] = func() (Object, error) { return &shellObject{in: in}, nil }
	in.factories["wscript.network"] = func() (Object, error) { return &networkObject{}, nil }
}

// outWriter forwards to the interpreter's current output writer, so that a
// stream object stays valid across SetOutput calls.
type outWriter struct{ in *Interpreter }

func (w outWriter) Write(p []byte) (int, error) { return w.in.out.Write(p) }

func (in *Interpreter) stdoutStream() *textStream {
	return &textStream{w: outWriter{in}, eol: "\n", line: 1, col: 1}
}

func (in *Interpreter) stdinStream() *textStream {
	return &textStream{r: in.stdin, eol: "\n", line: 1, col: 1}
}

// wscriptObject is the global WScript object.
type wscriptObject struct {
	in *Interpreter
}

func (w *wscriptObject) TypeName() string { return "IHost" }

// echoText renders the arguments of WScript.Echo.
func echoText(args []Value) (string, error) {
	parts := make([]string, len(args))
	for i, a := range args {
		s, err := concatStr(a)
		if err != nil {
			return "", err
		}
		parts[i] = s
	}
	return strings.Join(parts, " "), nil
}

func (w *wscriptObject) Get(name string, args []Value) (Value, error) {
	in := w.in
	switch name {
	case "echo":
		s, err := echoText(args)
		if err != nil {
			return nil, err
		}
		fmt.Fprintln(in.out, s)
		return nil, nil
	case "sleep":
		ms, err := optInt(args, 0, 0)
		if err != nil {
			return nil, err
		}
		if ms < 0 {
			return nil, newErr(5, "")
		}
		if err := in.checkCtx(); err != nil {
			return nil, err
		}
		if ms == 0 {
			return nil, nil
		}
		t := time.NewTimer(time.Duration(ms) * time.Millisecond)
		defer t.Stop()
		select {
		case <-t.C:
			return nil, nil
		case <-in.done:
			return nil, in.ctx.Err()
		}
	case "quit":
		code, err := optInt(args, 0, 0)
		if err != nil {
			return nil, err
		}
		in.exitCode = int(code)
		return nil, ErrQuit
	case "arguments":
		items := make([]Value, len(in.scriptArgs))
		for i, a := range in.scriptArgs {
			items[i] = a
		}
		c := &argsCollection{collection{typeName: "IArguments2", items: items}}
		if len(args) > 0 {
			return c.Get("item", args)
		}
		return c, nil
	case "scriptname":
		return filepath.Base(in.scriptFile), nil
	case "scriptfullname":
		if in.scriptFile == "" {
			return "", nil
		}
		if abs, err := filepath.Abs(in.scriptFile); err == nil {
			return abs, nil
		}
		return in.scriptFile, nil
	case "stdout", "stderr":
		return in.stdoutStream(), nil
	case "stdin":
		return in.stdinStream(), nil
	case "createobject":
		if len(args) < 1 {
			return nil, newErr(450, "")
		}
		return in.createObject(args)
	case "getobject":
		return in.callFunction("GetObject", args)
	case "name":
		return "Windows Script Host", nil
	case "application":
		return w, nil
	case "fullname":
		exe, _ := os.Executable()
		return exe, nil
	case "path":
		exe, _ := os.Executable()
		return filepath.Dir(exe), nil
	case "version":
		return "5.812", nil
	case "buildversion":
		return int64(10240), nil
	case "interactive":
		return true, nil
	case "timeout":
		return int64(0), nil
	case "connectobject", "disconnectobject":
		return nil, nil
	}
	return nil, newErr(438, "")
}

func (w *wscriptObject) Set(name string, args []Value, v Value) error {
	switch name {
	case "interactive", "timeout":
		return nil
	}
	return newErr(438, "")
}

// argsCollection is WScript.Arguments.
type argsCollection struct {
	collection
}

func (a *argsCollection) Get(name string, args []Value) (Value, error) {
	switch name {
	case "named", "unnamed":
		return &collection{typeName: "IArguments2", items: a.items}, nil
	case "showusage":
		return nil, nil
	}
	return a.collection.Get(name, args)
}

// ---- WScript.Network ----

type networkObject struct{}

func (n *networkObject) TypeName() string { return "IWshNetwork2" }

func (n *networkObject) Get(name string, args []Value) (Value, error) {
	switch name {
	case "computername":
		h, _ := os.Hostname()
		if i := strings.IndexByte(h, '.'); i > 0 {
			h = h[:i]
		}
		return h, nil
	case "username":
		if u, err := user.Current(); err == nil && u.Username != "" {
			name := u.Username
			if i := strings.LastIndexByte(name, '\\'); i >= 0 {
				name = name[i+1:]
			}
			return name, nil
		}
		for _, k := range []string{"USER", "USERNAME", "LOGNAME"} {
			if v := os.Getenv(k); v != "" {
				return v, nil
			}
		}
		return "", nil
	case "userdomain":
		if v := os.Getenv("USERDOMAIN"); v != "" {
			return v, nil
		}
		h, _ := os.Hostname()
		return h, nil
	}
	return nil, newErr(438, "")
}

func (n *networkObject) Set(string, []Value, Value) error { return newErr(438, "") }

// ---- WScript.Shell ----

type shellObject struct {
	in *Interpreter
}

func (s *shellObject) TypeName() string { return "IWshShell3" }

var envRef = regexp.MustCompile(`%([^%\r\n]+)%`)

// expandEnv replaces %NAME% references with environment values; unknown
// names are left untouched.
func expandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
}

func (s *shellObject) Get(name string, args []Value) (Value, error) {
	in := s.in
	switch name {
	case "expandenvironmentstrings":
		str, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		return expandEnv(str), nil
	case "environment":
		env := &envObject{}
		if len(args) > 1 {
			return env.Get("item", args[1:])
		}
		return env, nil
	case "currentdirectory":
		wd, err := os.Getwd()
		if err != nil {
			return nil, fsError(err, 76)
		}
		return wd, nil
	case "run":
		cmdline, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		wait, err := optBool(args, 2, false)
		if err != nil {
			return nil, err
		}
		cmdline = expandEnv(cmdline)
		if !wait {
			cmd := shellCommand(context.Background(), cmdline)
			if err := cmd.Start(); err != nil {
				return nil, newErrMsg(-2147024894, "The system cannot find the file specified: "+err.Error())
			}
			go func() { _ = cmd.Wait() }()
			return int64(0), nil
		}
		cmd := shellCommand(in.ctx, cmdline)
		err = cmd.Run()
		if cerr := in.checkCtx(); cerr != nil {
			return nil, cerr
		}
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return int64(ee.ExitCode()), nil
			}
			return nil, newErrMsg(-2147024894, "The system cannot find the file specified: "+err.Error())
		}
		return int64(0), nil
	case "exec":
		cmdline, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		return startExec(in, expandEnv(cmdline))
	case "popup":
		// Popup(text, [secondsToWait], [title], [type]) maps onto MsgBox.
		if len(args) < 1 {
			return nil, newErr(450, "")
		}
		var typ, title Value
		if len(args) > 3 {
			typ = args[3]
		}
		if len(args) > 2 {
			title = args[2]
		}
		return in.callFunction("MsgBox", []Value{args[0], typ, title})
	case "sendkeys", "logevent":
		return nil, nil
	case "appactivate":
		return false, nil
	case "specialfolders":
		home, _ := os.UserHomeDir()
		folders := map[string]string{
			"desktop":     filepath.Join(home, "Desktop"),
			"mydocuments": filepath.Join(home, "Documents"),
			"appdata":     home,
			"startmenu":   home,
			"programs":    home,
			"startup":     home,
			"favorites":   home,
			"templates":   home,
			"sendto":      home,
		}
		if len(args) > 0 {
			key, err := toStr(args[0])
			if err != nil {
				return nil, err
			}
			return folders[strings.ToLower(key)], nil
		}
		keys := make([]string, 0, len(folders))
		for k := range folders {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		items := make([]Value, len(keys))
		for i, k := range keys {
			items[i] = folders[k]
		}
		return &collection{typeName: "IWshCollection", items: items, byKey: func(key string) (Value, bool) {
			v, ok := folders[strings.ToLower(key)]
			return v, ok
		}}, nil
	case "regread", "regwrite", "regdelete":
		return nil, newErrMsg(-2147024894, "The Windows registry is not available")
	case "createshortcut":
		return nil, newErr(445, "")
	}
	return nil, newErr(438, "")
}

func (s *shellObject) Set(name string, args []Value, v Value) error {
	if name == "currentdirectory" {
		dir, err := toStr(v)
		if err != nil {
			return err
		}
		return fsError(os.Chdir(fsPath(dir)), 76)
	}
	return newErr(438, "")
}

// envObject is the collection returned by WshShell.Environment. All
// environment types map onto the process environment.
type envObject struct{}

func (e *envObject) TypeName() string { return "IWshEnvironment" }

func (e *envObject) Enumerate() []Value {
	env := os.Environ()
	sort.Strings(env)
	items := make([]Value, len(env))
	for i, kv := range env {
		items[i] = kv
	}
	return items
}

func (e *envObject) Get(name string, args []Value) (Value, error) {
	switch name {
	case "", "item":
		key, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		return os.Getenv(key), nil
	case "count", "length":
		return int64(len(os.Environ())), nil
	case "remove":
		key, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		_ = os.Unsetenv(key)
		return nil, nil
	}
	return nil, newErr(438, "")
}

func (e *envObject) Set(name string, args []Value, v Value) error {
	if name != "" && name != "item" {
		return newErr(438, "")
	}
	key, err := argStrAt(args, 0)
	if err != nil {
		return err
	}
	val, err := toStr(v)
	if err != nil {
		return err
	}
	if err := os.Setenv(key, val); err != nil {
		return newErr(5, "")
	}
	return nil
}

// execObject is the WshScriptExec object returned by WshShell.Exec.
type execObject struct {
	cmd    *exec.Cmd
	stdin  *textStream
	stdout *textStream
	stderr *textStream

	mu   sync.Mutex
	done bool
	code int
}

func (e *execObject) TypeName() string { return "WshExec" }

func startExec(in *Interpreter, cmdline string) (Value, error) {
	cmd := shellCommand(in.ctx, cmdline)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, newErrMsg(HostErrorNumber, err.Error())
	}
	// The output is collected by the process itself into pipes whose write
	// ends are closed as soon as it exits, so readers see EOF reliably.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, newErrMsg(HostErrorNumber, err.Error())
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, newErrMsg(HostErrorNumber, err.Error())
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		for _, c := range []io.Closer{outR, outW, errR, errW} {
			c.Close()
		}
		return nil, newErrMsg(-2147024894, "The system cannot find the file specified: "+err.Error())
	}
	outW.Close()
	errW.Close()
	e := &execObject{
		cmd:    cmd,
		stdin:  &textStream{w: stdin, closer: stdin, eol: lineEnding, line: 1, col: 1},
		stdout: &textStream{r: newReader(outR, false), closer: outR, eol: "\n", line: 1, col: 1},
		stderr: &textStream{r: newReader(errR, false), closer: errR, eol: "\n", line: 1, col: 1},
	}
	go func() {
		err := cmd.Wait()
		e.mu.Lock()
		e.done = true
		if ee, ok := err.(*exec.ExitError); ok {
			e.code = ee.ExitCode()
		}
		e.mu.Unlock()
	}()
	return e, nil
}

func (e *execObject) Get(name string, args []Value) (Value, error) {
	switch name {
	case "stdin":
		return e.stdin, nil
	case "stdout":
		return e.stdout, nil
	case "stderr":
		return e.stderr, nil
	case "status":
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.done {
			return int64(1), nil
		}
		return int64(0), nil
	case "exitcode":
		e.mu.Lock()
		defer e.mu.Unlock()
		return int64(e.code), nil
	case "processid":
		if e.cmd.Process != nil {
			return int64(e.cmd.Process.Pid), nil
		}
		return int64(0), nil
	case "terminate":
		if e.cmd.Process != nil {
			_ = e.cmd.Process.Kill()
		}
		return nil, nil
	}
	return nil, newErr(438, "")
}

func (e *execObject) Set(string, []Value, Value) error { return newErr(438, "") }

// hostShell returns the command interpreter used by Run and Exec.
func hostShell() (string, string) {
	if runtime.GOOS == "windows" {
		if c := os.Getenv("ComSpec"); c != "" {
			return c, "/C"
		}
		return "cmd.exe", "/C"
	}
	return "/bin/sh", "-c"
}
