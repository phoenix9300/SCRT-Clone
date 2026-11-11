package script

import (
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
)

// optionAliases maps the classic option names to session JSON paths.
var optionAliases = map[string]string{
	"hostname":      "host",
	"username":      "username",
	"port":          "port",
	"[ssh2] port":   "port",
	"protocol name": "protocol",
	"emulation":     "terminal.emulation",
	"description":   "description",
	"scrollback":    "terminal.scrollback",
	"rows":          "terminal.rows",
	"cols":          "terminal.cols",
}

var protocolNames = map[string]string{
	"ssh2": "SSH2", "telnet": "Telnet", "rlogin": "RLogin", "raw": "Raw", "serial": "Serial", "local": "Local",
	"telnet-tls": "Telnet/TLS",
}

func optionPath(name string) []string {
	n := strings.TrimSpace(name)
	if a, ok := optionAliases[strings.ToLower(n)]; ok {
		n = a
	}
	return strings.Split(n, ".")
}

func lookup(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func deepCopyMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func overlay(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				overlay(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

// fromJSON turns decoded JSON values into model values.
func fromJSON(v any) any {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return int64(x)
		}
		return x
	case map[string]any:
		b, _ := json.Marshal(x)
		return string(b)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fromJSON(e)
		}
		return out
	}
	return v
}

// newSessionConfig loads a stored session (or the default session).
func newSessionConfig(e *env, path string) (*object, error) {
	isDefault := path == "" || strings.EqualFold(path, "default")
	st := e.h.Mgr.Store
	var ov, eff map[string]any
	var err error
	if isDefault {
		if ov, err = st.DefaultOverrides(); err != nil {
			return nil, err
		}
		if eff, err = st.Defaults(); err != nil {
			return nil, err
		}
	} else {
		if ov, err = st.Overrides(path); err != nil {
			return nil, err
		}
		if eff, err = st.Defaults(); err != nil {
			return nil, err
		}
	}
	ov = deepCopyMap(ov)
	eff = deepCopyMap(eff)
	if !isDefault {
		overlay(eff, ov)
	}
	o := newObject(e, "SessionConfiguration")
	o.method("GetOption", func(a []any) (any, error) {
		name := argStr(a, 0, "")
		p := optionPath(name)
		v, ok := lookup(ov, p)
		if !ok {
			v, ok = lookup(eff, p)
		}
		if !ok {
			return nil, errors.New("unknown option " + name)
		}
		if len(p) == 1 && p[0] == "protocol" {
			if pn, ok := protocolNames[toStr(v)]; ok {
				return pn, nil
			}
		}
		return fromJSON(v), nil
	})
	o.method("SetOption", func(a []any) (any, error) {
		name := argStr(a, 0, "")
		p := optionPath(name)
		var val any
		if len(a) > 1 {
			val = a[1]
		}
		if cur, ok := lookup(eff, p); ok {
			val = coerceLike(cur, val)
		} else if _, known := optionAliases[strings.ToLower(strings.TrimSpace(name))]; !known && !strings.Contains(name, ".") {
			return nil, errors.New("unknown option " + name)
		}
		if len(p) == 1 && p[0] == "protocol" {
			val = normalizeProtocol(toStr(val))
		}
		m := ov
		for _, k := range p[:len(p)-1] {
			next, ok := m[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[k] = next
			}
			m = next
		}
		m[p[len(p)-1]] = val
		// keep the effective view in step
		em := eff
		for _, k := range p[:len(p)-1] {
			next, ok := em[k].(map[string]any)
			if !ok {
				next = map[string]any{}
				em[k] = next
			}
			em = next
		}
		em[p[len(p)-1]] = val
		return nil, nil
	})
	o.method("Save", func(a []any) (any, error) {
		target := argStr(a, 0, "")
		if target == "" {
			if isDefault {
				return nil, st.SaveDefault(ov)
			}
			target = path
		}
		if strings.EqualFold(target, "default") {
			return nil, st.SaveDefault(ov)
		}
		return nil, st.Save(target, ov)
	})
	o.method("ConnectInTab", func(a []any) (any, error) {
		req := conn.OpenRequest{Overrides: deepCopyMap(ov)}
		if !isDefault {
			req.SessionPath = path
		}
		return e.openTab(req)
	})
	return o, nil
}

func normalizeProtocol(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ssh2", "ssh1", "ssh":
		return "ssh2"
	case "telnet/tls", "telnet-tls":
		return "telnet-tls"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// coerceLike converts v to the JSON kind of cur.
func coerceLike(cur, v any) any {
	switch cur.(type) {
	case float64:
		if isNumber(v) {
			return int64(toFloat(v))
		}
		if s, ok := v.(string); ok {
			return int64(toFloat(s))
		}
	case bool:
		return toBool(v)
	case string:
		return toStr(v)
	}
	return v
}
