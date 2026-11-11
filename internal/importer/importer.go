// Package importer converts session definitions from other tools (OpenSSH
// config, PuTTY, CSV, other terminals' session stores) into Hawser session
// overrides. Everything here is pure parsing: nothing touches the store.
package importer

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Item is one session to create. Overrides follow the JSON shape of
// store.SessionOptions and hold only the values that differ from defaults.
type Item struct {
	Path      string         `json:"path"`
	Overrides map[string]any `json:"overrides"`
}

// Result is what an importer found.
type Result struct {
	Items    []Item   `json:"items"`
	Warnings []string `json:"warnings"`
}

func (r *Result) warn(format string, a ...any) {
	r.Warnings = append(r.Warnings, sprintf(format, a...))
}

// fields is the common intermediate form shared by the importers.
type fields struct {
	name, folder, protocol, host, user, desc, emulation string
	port                                                int
	serialPort                                          string
	baud                                                int
	identity                                            string
}

func (f fields) item() Item {
	p := f.name
	if p == "" {
		p = f.host
	}
	if f.folder != "" {
		p = strings.Trim(f.folder, "/\\") + "/" + p
	}
	proto := normProtocol(f.protocol)
	ov := map[string]any{"protocol": proto}
	if f.host != "" {
		ov["host"] = f.host
	}
	if f.port > 0 {
		ov["port"] = f.port
	}
	if f.user != "" {
		ov["username"] = f.user
	}
	if f.desc != "" {
		ov["description"] = f.desc
	}
	if f.emulation != "" {
		ov["terminal"] = map[string]any{"emulation": strings.ToLower(f.emulation)}
	}
	if proto == "serial" {
		s := map[string]any{}
		if f.serialPort != "" {
			s["port"] = f.serialPort
		}
		if f.baud > 0 {
			s["baud"] = f.baud
		}
		ov["serial"] = s
	}
	if f.identity != "" {
		ov["ssh"] = map[string]any{"identityFiles": []string{f.identity}}
	}
	return Item{Path: p, Overrides: ov}
}

func normProtocol(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "ssh", "ssh1", "ssh2":
		return "ssh2"
	case "telnet":
		return "telnet"
	case "telnet/ssl", "telnet/tls", "telnet-tls", "telnet-ssl", "telnets":
		return "telnet-tls"
	case "rlogin":
		return "rlogin"
	case "raw":
		return "raw"
	case "serial":
		return "serial"
	case "local", "local shell", "local-shell":
		return "local"
	}
	return "ssh2"
}

// cleanName makes a session path component safe (no separators at the ends).
func cleanName(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	return strings.Trim(s, "/")
}

func unescapeName(s string) string {
	if u, err := url.PathUnescape(s); err == nil {
		return u
	}
	return s
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func hexInt(s string) int {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 16, 64)
	if err != nil {
		return 0
	}
	return int(n)
}

func sortItems(it []Item) {
	sort.SliceStable(it, func(i, j int) bool { return strings.ToLower(it[i].Path) < strings.ToLower(it[j].Path) })
}
