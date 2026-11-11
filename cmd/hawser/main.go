// Command hawser is an open-source terminal emulator and secure remote
// access client: SSH2, Telnet, Rlogin, serial and local shell sessions in a
// tabbed interface with session management, logging, scripting, file
// transfer and port forwarding.
//
// The binary contains the engine and the user interface. Running it starts
// the engine on a loopback port and opens the interface in an application
// window.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/server"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

const usage = `Hawser %s — open-source terminal emulator and SSH client

Usage:
  hawser [options] [target ...]

Targets:
  NAME                     open the stored session NAME (e.g. "Lab/core-sw1")
  ssh://[user@]host[:port] connect with SSH2
  telnet://host[:port]     connect with Telnet
  serial:DEVICE[:BAUD]     open a serial port (e.g. serial:/dev/ttyUSB0:115200)
  local                    open a local shell

Options:
  --listen ADDR        address for the UI server (default 127.0.0.1:0)
  --browser MODE       app (default), default, or none
  --no-browser         same as --browser none
  --config-dir DIR     configuration directory (default %s)
  --allow-remote       accept UI connections from other hosts (token still required)
  --script FILE        run a script once the UI is ready
  --arg VALUE          argument passed to the script (repeatable)
  --ui-dir DIR         serve the UI from DIR instead of the built-in bundle (development)
  --new-instance       do not hand targets to an already running Hawser
  --version            print the version and exit

For convenience the classic switches are accepted as well:
  /S NAME   /SSH2 [/L user] [/P port] [/PASSWORD pw] host   /TELNET host [port]
  /SERIAL port [/BAUD n]   /SCRIPT file   /ARG value   /T   /N tabname   /F dir
`

type options struct {
	listen      string
	browser     string
	configDir   string
	allowRemote bool
	newInstance bool
	uiDir       string
	startup     []map[string]any
}

func parseArgs(args []string) (*options, error) {
	o := &options{listen: "127.0.0.1:0", browser: "app"}
	var scriptArgs []string
	var script string
	var pendingUser, pendingPw, pendingTab string
	pendingPort := 0
	pendingBaud := 0
	next := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	addSession := func(ov map[string]any) {
		if pendingUser != "" {
			ov["username"] = pendingUser
		}
		if pendingPort != 0 {
			ov["port"] = pendingPort
		}
		act := map[string]any{"action": "connect", "overrides": ov}
		if pendingPw != "" {
			act["password"] = pendingPw
		}
		if pendingTab != "" {
			act["title"] = pendingTab
		}
		o.startup = append(o.startup, act)
		pendingUser, pendingPw, pendingTab, pendingPort = "", "", "", 0
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		key := a
		// "/SWITCH" style (only when it cannot be a path)
		if strings.HasPrefix(a, "/") && len(a) > 1 && !strings.ContainsAny(a[1:], "/.") {
			key = "--" + strings.ToLower(a[1:])
		}
		var err error
		switch key {
		case "-h", "--help", "--?":
			fmt.Printf(usage, server.Version, store.DefaultDir())
			os.Exit(0)
		case "--version":
			fmt.Println("hawser", server.Version)
			os.Exit(0)
		case "--listen":
			o.listen, err = next(&i, a)
		case "--browser":
			o.browser, err = next(&i, a)
		case "--no-browser":
			o.browser = "none"
		case "--config-dir", "--f":
			o.configDir, err = next(&i, a)
		case "--allow-remote":
			o.allowRemote = true
		case "--ui-dir":
			o.uiDir, err = next(&i, a)
		case "--new-instance":
			o.newInstance = true
		case "--script":
			script, err = next(&i, a)
		case "--arg":
			var v string
			v, err = next(&i, a)
			scriptArgs = append(scriptArgs, v)
		case "--t", "--nomenu", "--notoolbar", "--max", "--min":
			// accepted for compatibility; tabs are always used
		case "--n":
			pendingTab, err = next(&i, a)
		case "--l":
			pendingUser, err = next(&i, a)
		case "--p":
			var v string
			if v, err = next(&i, a); err == nil {
				pendingPort, err = strconv.Atoi(v)
			}
		case "--password":
			pendingPw, err = next(&i, a)
		case "--baud":
			var v string
			if v, err = next(&i, a); err == nil {
				pendingBaud, err = strconv.Atoi(v)
			}
		case "--s":
			var v string
			if v, err = next(&i, a); err == nil {
				act := map[string]any{"action": "connect", "session": v}
				if pendingTab != "" {
					act["title"] = pendingTab
					pendingTab = ""
				}
				o.startup = append(o.startup, act)
			}
		case "--ssh2", "--ssh":
			// switches may follow: /SSH2 /L user /P 22 host
			for i+1 < len(args) && strings.HasPrefix(args[i+1], "/") {
				sw := strings.ToLower(args[i+1])
				if sw != "/l" && sw != "/p" && sw != "/password" && sw != "/n" {
					break
				}
				i++
				var v string
				if v, err = next(&i, sw); err != nil {
					break
				}
				switch sw {
				case "/l":
					pendingUser = v
				case "/p":
					pendingPort, err = strconv.Atoi(v)
				case "/password":
					pendingPw = v
				case "/n":
					pendingTab = v
				}
			}
			if err == nil {
				var host string
				if host, err = next(&i, a); err == nil {
					ov := map[string]any{"protocol": "ssh2", "host": host}
					if at := strings.LastIndexByte(host, '@'); at > 0 {
						ov["username"], ov["host"] = host[:at], host[at+1:]
					}
					addSession(ov)
				}
			}
		case "--telnet":
			var host string
			if host, err = next(&i, a); err == nil {
				ov := map[string]any{"protocol": "telnet", "host": host}
				if i+1 < len(args) {
					if p, perr := strconv.Atoi(args[i+1]); perr == nil {
						ov["port"] = p
						i++
					}
				}
				addSession(ov)
			}
		case "--serial":
			var dev string
			if dev, err = next(&i, a); err == nil {
				se := map[string]any{"port": dev}
				if i+2 < len(args) && strings.EqualFold(args[i+1], "/baud") {
					if b, berr := strconv.Atoi(args[i+2]); berr == nil {
						pendingBaud = b
						i += 2
					}
				}
				if pendingBaud != 0 {
					se["baud"] = pendingBaud
					pendingBaud = 0
				}
				addSession(map[string]any{"protocol": "serial", "serial": se})
			}
		default:
			if strings.HasPrefix(a, "--") {
				return nil, fmt.Errorf("unknown option %s", a)
			}
			act, perr := parseTarget(a)
			if perr != nil {
				return nil, perr
			}
			if ov, ok := act["overrides"].(map[string]any); ok {
				addSession(ov)
			} else {
				o.startup = append(o.startup, act)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if script != "" {
		o.startup = append(o.startup, map[string]any{"action": "script", "path": script, "args": scriptArgs})
	}
	return o, nil
}

// parseTarget turns a positional argument into a start-up action.
func parseTarget(a string) (map[string]any, error) {
	switch {
	case a == "local":
		return map[string]any{"action": "connect", "overrides": map[string]any{"protocol": "local"}}, nil
	case strings.HasPrefix(a, "serial:"):
		rest := strings.TrimPrefix(a, "serial:")
		se := map[string]any{}
		// a trailing ":<digits>" is the baud rate (COM3:115200, /dev/ttyUSB0:9600)
		if i := strings.LastIndexByte(rest, ':'); i > 0 {
			if b, err := strconv.Atoi(rest[i+1:]); err == nil {
				se["baud"] = b
				rest = rest[:i]
			}
		}
		se["port"] = rest
		return map[string]any{"action": "connect", "overrides": map[string]any{"protocol": "serial", "serial": se}}, nil
	case strings.Contains(a, "://"):
		u, err := url.Parse(a)
		if err != nil {
			return nil, fmt.Errorf("bad URL %q: %w", a, err)
		}
		ov := map[string]any{"host": u.Hostname()}
		switch u.Scheme {
		case "ssh", "ssh2", "sftp":
			ov["protocol"] = "ssh2"
		case "telnet":
			ov["protocol"] = "telnet"
		case "telnets":
			ov["protocol"] = "telnet-tls"
		case "rlogin":
			ov["protocol"] = "rlogin"
		default:
			return nil, fmt.Errorf("unsupported URL scheme %q", u.Scheme)
		}
		if p := u.Port(); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("bad port in %q", a)
			}
			ov["port"] = n
		}
		act := map[string]any{"action": "connect", "overrides": ov}
		if u.User != nil {
			ov["username"] = u.User.Username()
			if pw, ok := u.User.Password(); ok {
				act["password"] = pw
			}
		}
		if u.Scheme == "sftp" {
			act["action"] = "sftp"
		}
		return act, nil
	}
	return map[string]any{"action": "connect", "session": a}, nil
}

// instanceFile records the running instance so later invocations can hand
// their targets to it instead of starting a second engine.
type instanceFile struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func handOff(dir string, startup []map[string]any) bool {
	b, err := os.ReadFile(filepath.Join(dir, "instance.json"))
	if err != nil {
		return false
	}
	var inst instanceFile
	if json.Unmarshal(b, &inst) != nil || inst.URL == "" {
		return false
	}
	body, _ := json.Marshal(map[string]any{"startup": startup})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(inst.URL, "/")+"/api/open", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("X-Hawser-Token", inst.Token)
	req.Header.Set("Content-Type", "application/json")
	cl := &http.Client{Timeout: 3 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hawser:", err)
		fmt.Fprintln(os.Stderr, "Try 'hawser --help'.")
		os.Exit(2)
	}
	st, err := store.Open(opts.configDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hawser: cannot open configuration:", err)
		os.Exit(1)
	}
	if !opts.newInstance && len(opts.startup) > 0 && handOff(st.Dir, opts.startup) {
		fmt.Println("Opened in the running Hawser window.")
		return
	}

	srv := server.New(st)
	srv.AllowRemote = opts.allowRemote
	srv.UIDir = opts.uiDir
	srv.Startup = opts.startup
	base, err := srv.Listen(opts.listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hawser:", err)
		os.Exit(1)
	}
	uiURL := base + "?token=" + srv.Token
	instPath := filepath.Join(st.Dir, "instance.json")
	if b, err := json.Marshal(instanceFile{URL: base, Token: srv.Token, PID: os.Getpid()}); err == nil {
		_ = store.WriteFileAtomic(instPath, b, 0o600)
	}
	defer os.Remove(instPath)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve() }()

	fmt.Printf("Hawser %s\n  UI:     %s\n  Config: %s\n", server.Version, uiURL, st.Dir)

	// The browser is started on a private page that forwards to the UI, so the
	// access token never appears in a process command line that other local
	// users could read.
	launchURL := uiURL
	launchPath := filepath.Join(st.Dir, "launch.html")
	page := fmt.Sprintf("<!doctype html><meta charset=\"utf-8\"><title>Hawser</title><script>location.replace(%q)</script>\n", uiURL)
	if err := store.WriteFileAtomic(launchPath, []byte(page), 0o600); err == nil {
		defer os.Remove(launchPath)
		if abs, aerr := filepath.Abs(launchPath); aerr == nil {
			p := filepath.ToSlash(abs)
			if !strings.HasPrefix(p, "/") {
				p = "/" + p // Windows drive paths
			}
			launchURL = (&url.URL{Scheme: "file", Path: p}).String()
		}
	}

	browserDone := make(chan struct{})
	switch opts.browser {
	case "none":
	case "default":
		if err := openDefaultBrowser(launchURL); err != nil {
			fmt.Fprintln(os.Stderr, "hawser: could not open a browser:", err)
		}
	default:
		wait, err := openAppWindow(launchURL, filepath.Join(st.Dir, "window-profile"))
		if err != nil {
			if !errors.Is(err, errNoAppBrowser) {
				fmt.Fprintln(os.Stderr, "hawser:", err)
			}
			if err := openDefaultBrowser(launchURL); err != nil {
				fmt.Fprintln(os.Stderr, "hawser: could not open a browser; open the UI address above manually.")
			}
		} else {
			go func() {
				wait()
				close(browserDone)
			}()
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-srv.Done():
	case <-browserDone:
	case err := <-errc:
		if err != nil {
			fmt.Fprintln(os.Stderr, "hawser:", err)
		}
	}
	srv.Close()
}
