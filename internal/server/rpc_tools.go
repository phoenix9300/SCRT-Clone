package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/importer"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
	"github.com/phoenix9300/SCRT-Clone/internal/tftp"
)

type tftpEvent struct {
	Time   int64  `json:"time"`
	Kind   string `json:"kind"`
	Remote string `json:"remote"`
	File   string `json:"file"`
	Bytes  int64  `json:"bytes"`
	Error  string `json:"error"`
}

// tftpState is the single TFTP server instance controlled from the UI.
type tftpState struct {
	mu     sync.Mutex
	srv    *tftp.Server
	addr   string
	events []tftpEvent
}

const tftpRing = 200

func (t *tftpState) status() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	ev := append([]tftpEvent{}, t.events...)
	out := map[string]any{"running": t.srv != nil, "addr": t.addr, "events": ev}
	if t.srv != nil {
		out["root"] = t.srv.Root
		out["allowRead"], out["allowWrite"], out["allowOverwrite"] = t.srv.AllowRead, t.srv.AllowWrite, t.srv.AllowOverwrite
	}
	return out
}

func (t *tftpState) stop() {
	t.mu.Lock()
	srv := t.srv
	t.srv, t.addr = nil, ""
	t.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

func (s *Server) registerTools() {
	ts := &tftpState{}
	s.ext.onClose(ts.stop)

	s.on("tftp.start", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Root           string `json:"root"`
			Port           int    `json:"port"`
			AllowRead      bool   `json:"allowRead"`
			AllowWrite     bool   `json:"allowWrite"`
			AllowOverwrite bool   `json:"allowOverwrite"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(p.Root); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("%q is not a directory", p.Root)
		}
		if p.Port <= 0 {
			p.Port = 69
		}
		if p.Port > 65535 {
			return nil, errors.New("invalid port")
		}
		ts.mu.Lock()
		running := ts.srv != nil
		ts.mu.Unlock()
		if running {
			return nil, errors.New("the TFTP server is already running")
		}
		pc, err := net.ListenPacket("udp", ":"+strconv.Itoa(p.Port))
		if err != nil {
			return nil, err
		}
		srv := &tftp.Server{Root: p.Root, AllowRead: p.AllowRead, AllowWrite: p.AllowWrite, AllowOverwrite: p.AllowOverwrite}
		srv.OnEvent = func(e tftp.Event) {
			ev := tftpEvent{Time: e.Time.UnixMilli(), Kind: e.Kind, Remote: e.Remote, File: e.File, Bytes: e.Bytes}
			if ev.Time == 0 {
				ev.Time = time.Now().UnixMilli()
			}
			if e.Err != nil {
				ev.Error = e.Err.Error()
			}
			ts.mu.Lock()
			ts.events = append(ts.events, ev)
			if len(ts.events) > tftpRing {
				ts.events = ts.events[len(ts.events)-tftpRing:]
			}
			ts.mu.Unlock()
			s.broadcast("tftp.event", ev)
		}
		ts.mu.Lock()
		ts.srv, ts.addr = srv, pc.LocalAddr().String()
		ts.mu.Unlock()
		go func() {
			_ = srv.Serve(pc)
			ts.mu.Lock()
			if ts.srv == srv {
				ts.srv, ts.addr = nil, ""
			}
			ts.mu.Unlock()
			s.broadcast("tftp.status", ts.status())
		}()
		st := ts.status()
		s.broadcast("tftp.status", st)
		return st, nil
	})
	s.on("tftp.stop", func(c *client, _ json.RawMessage) (any, error) {
		ts.stop()
		st := ts.status()
		s.broadcast("tftp.status", st)
		return st, nil
	})
	s.on("tftp.status", func(c *client, _ json.RawMessage) (any, error) { return ts.status(), nil })

	s.on("import.preview", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.Path == "~" || strings.HasPrefix(p.Path, "~/") || strings.HasPrefix(p.Path, "~\\") {
			if h, herr := os.UserHomeDir(); herr == nil {
				p.Path = h + p.Path[1:]
			}
		}
		var res importer.Result
		read := func(f func([]byte) (importer.Result, error)) (importer.Result, error) {
			b, err := os.ReadFile(p.Path)
			if err != nil {
				return importer.Result{}, err
			}
			return f(b)
		}
		switch p.Kind {
		case "openssh":
			res, err = read(importer.OpenSSHConfig)
		case "putty":
			if st, serr := os.Stat(p.Path); serr == nil && st.IsDir() {
				res, err = importer.PuTTY(p.Path)
			} else {
				res, err = read(importer.PuTTYReg)
			}
		case "csv":
			res, err = read(importer.CSV)
		case "hawser":
			res, err = read(importer.Hawser)
		case "crt":
			res, err = importer.CRT(p.Path)
		default:
			return nil, fmt.Errorf("unknown import kind %q", p.Kind)
		}
		if err != nil {
			return nil, err
		}
		if res.Items == nil {
			res.Items = []importer.Item{}
		}
		if res.Warnings == nil {
			res.Warnings = []string{}
		}
		return res, nil
	})
	s.on("import.apply", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Items     []importer.Item `json:"items"`
			Folder    string          `json:"folder"`
			Overwrite bool            `json:"overwrite"`
		}](raw)
		if err != nil {
			return nil, err
		}
		created, skipped := 0, 0
		errs := []string{}
		for _, it := range p.Items {
			path := strings.Trim(it.Path, "/")
			if f := strings.Trim(p.Folder, "/"); f != "" {
				path = f + "/" + path
			}
			if s.Store.Exists(path) && !p.Overwrite {
				skipped++
				continue
			}
			if it.Overrides == nil {
				it.Overrides = map[string]any{}
			}
			if err := s.Store.Save(path, it.Overrides); err != nil {
				errs = append(errs, path+": "+err.Error())
				continue
			}
			created++
		}
		s.broadcast("sessions.changed", nil)
		return map[string]any{"created": created, "skipped": skipped, "errors": errs}, nil
	})
	s.on("export.sessions", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			File  string   `json:"file"`
			Paths []string `json:"paths"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.File == "" {
			return nil, errors.New("no file given")
		}
		paths := p.Paths
		if len(paths) == 0 {
			if paths, err = s.Store.SessionPaths(); err != nil {
				return nil, err
			}
		}
		items := make([]importer.Item, 0, len(paths))
		for _, sp := range paths {
			ov, err := s.Store.Overrides(sp)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", sp, err)
			}
			items = append(items, importer.Item{Path: sp, Overrides: ov})
		}
		data, err := json.MarshalIndent(map[string]any{"version": 1, "sessions": items}, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := store.WriteFileAtomic(p.File, data, 0o600); err != nil {
			return nil, err
		}
		return map[string]any{"count": len(items), "file": p.File}, nil
	})
}
