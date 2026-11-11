package importer

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

func puttyFields(name string, m map[string]string) fields {
	f := fields{name: cleanName(name), host: m["HostName"], user: m["UserName"], protocol: m["Protocol"], port: atoi(m["PortNumber"]),
		serialPort: m["SerialLine"], baud: atoi(m["SerialSpeed"]), identity: m["PublicKeyFile"]}
	if f.protocol == "" {
		f.protocol = "ssh"
	}
	return f
}

// PuTTY reads a directory of PuTTY session files (~/.putty/sessions).
func PuTTY(dir string) (Result, error) {
	var res Result
	ents, err := os.ReadDir(dir)
	if err != nil {
		return res, err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := unescapeName(e.Name())
		if name == "Default Settings" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			res.warn("%s: %v", name, err)
			continue
		}
		m := map[string]string{}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if k, v, ok := strings.Cut(strings.TrimRight(sc.Text(), "\r"), "="); ok {
				m[k] = v
			}
		}
		res.Items = append(res.Items, puttyFields(name, m).item())
	}
	sortItems(res.Items)
	return res, nil
}

var (
	regSection = regexp.MustCompile(`(?i)^\[HKEY_[A-Z_]+\\Software\\SimonTatham\\PuTTY\\Sessions\\(.+)\]$`)
	regValue   = regexp.MustCompile(`^"((?:[^"\\]|\\.)*)"=(.*)$`)
)

func decodeUTF16(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		return string(utf16.Decode(u))
	}
	return strings.TrimPrefix(string(b), "\xef\xbb\xbf")
}

func regUnescape(s string) string {
	r := strings.NewReplacer(`\\`, `\`, `\"`, `"`)
	return r.Replace(s)
}

// PuTTYReg parses a Windows registry export of PuTTY's saved sessions.
func PuTTYReg(data []byte) (Result, error) {
	var res Result
	text := strings.ReplaceAll(decodeUTF16(data), "\r\n", "\n")
	var name string
	var cur map[string]string
	flush := func() {
		if cur != nil && name != "Default Settings" {
			res.Items = append(res.Items, puttyFields(name, cur).item())
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			flush()
			if m := regSection.FindStringSubmatch(line); m != nil {
				name, cur = unescapeName(m[1]), map[string]string{}
			}
			continue
		}
		if cur == nil {
			continue
		}
		m := regValue.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := m[2]
		switch {
		case strings.HasPrefix(v, "dword:"):
			n, _ := strconv.ParseUint(strings.TrimPrefix(v, "dword:"), 16, 32)
			v = strconv.FormatUint(n, 10)
		case len(v) >= 2 && v[0] == '"':
			v = regUnescape(v[1 : len(v)-1])
		}
		cur[regUnescape(m[1])] = v
	}
	flush()
	sortItems(res.Items)
	return res, nil
}
