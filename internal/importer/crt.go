package importer

import (
	"bytes"
	"encoding/xml"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var crtLine = regexp.MustCompile(`^([A-Z]):"(.*?)"=(.*)$`)

func crtFields(name, folder string, s map[string]string, d map[string]int) fields {
	f := fields{name: name, folder: folder, host: s["Hostname"], user: s["Username"], protocol: s["Protocol Name"],
		desc: s["Description"], emulation: s["Emulation"], serialPort: s["Com Port"], baud: d["Baud Rate"]}
	if f.protocol == "" {
		f.protocol = "ssh2"
	}
	f.port = d["Port"]
	if p, ok := d["[SSH2] Port"]; ok && strings.HasPrefix(strings.ToLower(f.protocol), "ssh") {
		f.port = p
	}
	if strings.EqualFold(f.emulation, "xterm") {
		f.emulation = "xterm"
	}
	return f
}

// CRT imports another terminal product's session store: a directory tree of
// *.ini session files, or a single XML export. It only reads the user's own
// session data to create equivalent Hawser sessions.
func CRT(path string) (Result, error) {
	var res Result
	st, err := os.Stat(path)
	if err != nil {
		return res, err
	}
	if !st.IsDir() {
		b, err := os.ReadFile(path)
		if err != nil {
			return res, err
		}
		return crtXML(b)
	}
	err = filepath.WalkDir(path, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.EqualFold(filepath.Ext(p), ".ini") {
			return nil
		}
		base := e.Name()
		if strings.EqualFold(base, "__FolderData__.ini") || strings.EqualFold(base, "Default.ini") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			res.warn("%s: %v", p, rerr)
			return nil
		}
		s, d := map[string]string{}, map[string]int{}
		for _, line := range strings.Split(strings.ReplaceAll(decodeUTF16(b), "\r", ""), "\n") {
			m := crtLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			switch m[1] {
			case "S":
				s[m[2]] = m[3]
			case "D":
				d[m[2]] = hexInt(m[3])
			}
		}
		if s["Hostname"] == "" && s["Protocol Name"] == "" {
			return nil
		}
		rel, _ := filepath.Rel(path, filepath.Dir(p))
		folder := ""
		if rel != "." {
			folder = filepath.ToSlash(rel)
		}
		res.Items = append(res.Items, crtFields(cleanName(strings.TrimSuffix(base, filepath.Ext(base))), folder, s, d).item())
		return nil
	})
	sortItems(res.Items)
	return res, err
}

type xnode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Nodes   []xnode    `xml:",any"`
	Text    string     `xml:",chardata"`
}

func (n xnode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func crtXML(data []byte) (Result, error) {
	var res Result
	var root xnode
	if err := xml.NewDecoder(bytes.NewReader(data)).Decode(&root); err != nil {
		return res, err
	}
	var find func(n xnode) *xnode
	find = func(n xnode) *xnode {
		if n.XMLName.Local == "key" && n.attr("name") == "Sessions" {
			return &n
		}
		for _, c := range n.Nodes {
			if r := find(c); r != nil {
				return r
			}
		}
		return nil
	}
	sess := find(root)
	if sess == nil {
		res.warn("no Sessions key found in the XML file")
		return res, nil
	}
	var walk func(n xnode, folder string)
	walk = func(n xnode, folder string) {
		for _, c := range n.Nodes {
			if c.XMLName.Local != "key" {
				continue
			}
			name := c.attr("name")
			if name == "__FolderData__" || (folder == "" && name == "Default") {
				continue
			}
			s, d := map[string]string{}, map[string]int{}
			for _, v := range c.Nodes {
				switch v.XMLName.Local {
				case "string":
					s[v.attr("name")] = strings.TrimSpace(v.Text)
				case "dword":
					d[v.attr("name")] = hexInt(v.Text)
				}
			}
			if s["Hostname"] != "" || s["Protocol Name"] != "" {
				res.Items = append(res.Items, crtFields(cleanName(name), folder, s, d).item())
				continue
			}
			sub := name
			if folder != "" {
				sub = folder + "/" + name
			}
			walk(c, sub)
		}
	}
	walk(*sess, "")
	return res, nil
}
