package importer

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
)

var csvAliases = map[string]string{
	"name": "name", "session": "name", "sessionname": "name",
	"folder": "folder", "path": "folder",
	"protocol": "protocol",
	"hostname": "host", "host": "host", "address": "host",
	"port":     "port",
	"username": "user", "user": "user",
	"description": "desc", "emulation": "emulation",
}

// CSV imports sessions from a delimited text file with a header row.
func CSV(data []byte) (Result, error) {
	var res Result
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	head := data
	if i := bytes.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	delim, best := ',', -1
	for _, d := range []rune{',', ';', '\t'} {
		if n := strings.Count(string(head), string(d)); n > best {
			delim, best = d, n
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delim
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return res, err
	}
	if len(rows) == 0 {
		return res, errors.New("empty file")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		if k, ok := csvAliases[strings.ToLower(strings.TrimSpace(h))]; ok {
			if _, dup := col[k]; !dup {
				col[k] = i
			}
		}
	}
	if _, ok := col["host"]; !ok {
		if _, ok := col["name"]; !ok {
			return res, errors.New("no hostname or name column in the header row")
		}
	}
	for n, row := range rows[1:] {
		get := func(k string) string {
			if i, ok := col[k]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		f := fields{name: cleanName(get("name")), folder: get("folder"), protocol: get("protocol"), host: get("host"),
			port: atoi(get("port")), user: get("user"), desc: get("desc"), emulation: get("emulation")}
		if f.name == "" && f.host == "" {
			if len(strings.Join(row, "")) > 0 {
				res.warn("row %d: no name or host", n+2)
			}
			continue
		}
		res.Items = append(res.Items, f.item())
	}
	return res, nil
}

// Hawser reads the bundle written by export.sessions.
func Hawser(data []byte) (Result, error) {
	var res Result
	var b struct {
		Version  int    `json:"version"`
		Sessions []Item `json:"sessions"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return res, err
	}
	if b.Version != 1 {
		return res, errors.New("not a Hawser session export (version 1)")
	}
	for _, it := range b.Sessions {
		if it.Path == "" {
			continue
		}
		if it.Overrides == nil {
			it.Overrides = map[string]any{}
		}
		res.Items = append(res.Items, it)
	}
	return res, nil
}
