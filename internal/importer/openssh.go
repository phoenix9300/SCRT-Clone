package importer

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

type sshBlock struct {
	names []string
	opts  [][2]string
}

func splitKV(line string) (string, string) {
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return line, ""
	}
	k := line[:i]
	v := strings.TrimLeft(line[i:], " \t=")
	return k, strings.TrimSpace(v)
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// splitHostPort splits "[bind:]port" style strings ("host:port", "[::1]:80").
func splitHostPort(s string) (string, int) {
	s = strings.TrimSpace(s)
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", atoi(s)
	}
	return strings.Trim(s[:i], "[]"), atoi(s[i+1:])
}

func parseForward(kind, val string, warn func(string, ...any)) (map[string]any, bool) {
	f := strings.Fields(val)
	var bind string
	var bport int
	var dh string
	var dp int
	switch kind {
	case "dynamic":
		if len(f) < 1 {
			return nil, false
		}
		bind, bport = splitHostPort(f[0])
	default:
		if len(f) < 2 {
			return nil, false
		}
		bind, bport = splitHostPort(f[0])
		dh, dp = splitHostPort(f[1])
	}
	if bport == 0 {
		warn("ignored forward %q", val)
		return nil, false
	}
	if bind == "" || bind == "*" {
		bind = "localhost"
	}
	name := fmt.Sprintf("%s-%d", kind, bport)
	m := map[string]any{"name": name, "type": kind, "bindAddr": bind, "bindPort": bport, "enabled": true}
	if kind != "dynamic" {
		m["destHost"], m["destPort"] = dh, dp
	}
	return m, true
}

// OpenSSHConfig parses an OpenSSH client configuration.
func OpenSSHConfig(data []byte) (Result, error) {
	var res Result
	var blocks []*sshBlock
	var cur *sshBlock
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<16), 1<<22)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v := splitKV(line)
		switch strings.ToLower(k) {
		case "host":
			cur = &sshBlock{}
			for _, n := range strings.Fields(v) {
				n = unquote(n)
				if strings.ContainsAny(n, "*?!") {
					continue
				}
				cur.names = append(cur.names, n)
			}
			blocks = append(blocks, cur)
		case "match":
			cur = nil
			res.warn("Match blocks are not imported: %s", line)
		default:
			if cur != nil {
				cur.opts = append(cur.opts, [2]string{strings.ToLower(k), unquote(v)})
			}
		}
	}
	defined := map[string]bool{}
	for _, b := range blocks {
		for _, n := range b.names {
			defined[n] = true
		}
	}
	for _, b := range blocks {
		for _, name := range b.names {
			ov := map[string]any{"protocol": "ssh2", "host": name}
			ssh := map[string]any{}
			var idents []string
			var fwds []map[string]any
			for _, o := range b.opts {
				k, v := o[0], o[1]
				switch k {
				case "hostname":
					ov["host"] = strings.ReplaceAll(v, "%h", name)
				case "user":
					ov["username"] = v
				case "port":
					if n := atoi(v); n > 0 {
						ov["port"] = n
					}
				case "identityfile":
					idents = append(idents, v)
				case "proxyjump":
					first := strings.TrimSpace(strings.Split(v, ",")[0])
					switch {
					case strings.EqualFold(first, "none"):
					case defined[first]:
						ov["firewall"] = "session:" + first
					default:
						res.warn("%s: ProxyJump %q is not a defined host; skipped", name, first)
					}
				case "forwardagent":
					if yes(v) {
						ssh["agentForwarding"] = true
					}
				case "forwardx11":
					if yes(v) {
						ssh["x11Forwarding"] = true
					}
				case "localforward", "remoteforward", "dynamicforward":
					kind := strings.TrimSuffix(k, "forward")
					if m, ok := parseForward(kind, v, func(f string, a ...any) { res.warn(name+": "+f, a...) }); ok {
						fwds = append(fwds, m)
					}
				case "serveraliveinterval":
					if n := atoi(v); n > 0 {
						ssh["keepAliveInterval"] = n
					}
				}
			}
			if len(idents) > 0 {
				ssh["identityFiles"] = idents
			}
			if len(fwds) > 0 {
				ssh["forwards"] = fwds
			}
			if len(ssh) > 0 {
				ov["ssh"] = ssh
			}
			res.Items = append(res.Items, Item{Path: name, Overrides: ov})
		}
	}
	return res, nil
}

func yes(v string) bool {
	v = strings.ToLower(v)
	return v == "yes" || v == "true" || v == "on"
}
