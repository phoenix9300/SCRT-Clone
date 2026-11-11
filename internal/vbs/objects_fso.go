package vbs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// lineEnding is what TextStream.WriteLine appends: CRLF on Windows (as the
// real FileSystemObject does) and LF elsewhere.
var lineEnding = "\n"

func init() {
	if runtime.GOOS == "windows" {
		lineEnding = "\r\n"
	}
}

// fsPath converts a script path to a native one. Scripts written for
// Windows build paths with backslashes; on other systems these are treated
// as path separators so that such scripts keep working.
func fsPath(p string) string {
	if runtime.GOOS != "windows" {
		p = strings.ReplaceAll(p, `\`, "/")
	}
	return p
}

// fsError maps a Go file system error to a VBScript error; missing is the
// number to use when the target does not exist (53 file, 76 path).
func fsError(err error, missing int) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return newErr(missing, "")
	case errors.Is(err, fs.ErrPermission):
		return newErr(70, "")
	case errors.Is(err, fs.ErrExist):
		return newErr(58, "")
	}
	return newErrMsg(75, "Path/File access error: "+err.Error())
}

// openError distinguishes a missing file from a missing directory.
func openError(err error, path string) error {
	if errors.Is(err, fs.ErrNotExist) {
		if st, e := os.Stat(filepath.Dir(path)); e != nil || !st.IsDir() {
			return newErr(76, "")
		}
	}
	return fsError(err, 53)
}

func isSep(c byte) bool { return c == '/' || c == '\\' }

// lastComponent splits a script path at its last separator.
func splitPath(p string) (dir, name string) {
	for len(p) > 1 && isSep(p[len(p)-1]) {
		p = p[:len(p)-1]
	}
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		if len(p) == 2 && p[1] == ':' {
			return "", ""
		}
		return "", p
	}
	return p[:i], p[i+1:]
}

func fileTime(t time.Time) time.Time {
	l := t.Local()
	return makeDate(l.Year(), int(l.Month()), l.Day(), l.Hour(), l.Minute(), l.Second())
}

func hasWildcard(p string) bool { return strings.ContainsAny(p, "*?") }

func copyFile(src, dst string, overwrite bool) error {
	in, err := os.Open(src)
	if err != nil {
		return fsError(err, 53)
	}
	defer in.Close()
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags |= os.O_EXCL
	}
	mode := fs.FileMode(0o644)
	if st, err := in.Stat(); err == nil {
		mode = st.Mode().Perm()
	}
	out, err := os.OpenFile(dst, flags, mode)
	if err != nil {
		return openError(err, dst)
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return fsError(err, 53)
	}
	return fsError(out.Close(), 53)
}

func copyTree(src, dst string, overwrite bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fsError(err, 76)
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return fsError(rerr, 76)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return fsError(os.MkdirAll(target, 0o755), 76)
		}
		return copyFile(p, target, overwrite)
	})
}

// destination resolves the target of a copy or move: a trailing separator
// or an existing folder means "into that folder".
func destination(dst, srcName string) string {
	if dst == "" {
		return dst
	}
	if isSep(dst[len(dst)-1]) {
		return filepath.Join(fsPath(dst), srcName)
	}
	p := fsPath(dst)
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		return filepath.Join(p, srcName)
	}
	return p
}

// ---- TextStream ----

type textStream struct {
	r      *bufio.Reader
	w      io.Writer
	closer io.Closer
	utf16  bool // encode writes as UTF-16LE
	eol    string
	line   int
	col    int
	closed bool
}

func (ts *textStream) TypeName() string { return "TextStream" }

func newReader(r io.Reader, forceUTF16 bool) *bufio.Reader {
	br := bufio.NewReader(r)
	head, _ := br.Peek(3)
	switch {
	case len(head) >= 3 && head[0] == 0xEF && head[1] == 0xBB && head[2] == 0xBF:
		_, _ = br.Discard(3)
		return br
	case len(head) >= 2 && head[0] == 0xFF && head[1] == 0xFE:
		_, _ = br.Discard(2)
		return decodeUTF16(br, false)
	case len(head) >= 2 && head[0] == 0xFE && head[1] == 0xFF:
		_, _ = br.Discard(2)
		return decodeUTF16(br, true)
	case forceUTF16:
		return decodeUTF16(br, false)
	}
	return br
}

func decodeUTF16(r io.Reader, bigEndian bool) *bufio.Reader {
	raw, _ := io.ReadAll(r)
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		if bigEndian {
			units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
		} else {
			units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
		}
	}
	return bufio.NewReader(strings.NewReader(string(utf16.Decode(units))))
}

func (ts *textStream) write(s string) error {
	if ts.closed || ts.w == nil {
		return newErr(54, "")
	}
	var err error
	if ts.utf16 {
		units := utf16.Encode([]rune(s))
		buf := make([]byte, 2*len(units))
		for i, u := range units {
			buf[2*i], buf[2*i+1] = byte(u), byte(u>>8)
		}
		_, err = ts.w.Write(buf)
	} else {
		_, err = io.WriteString(ts.w, s)
	}
	if err != nil {
		return newErrMsg(57, "Device I/O error: "+err.Error())
	}
	if n := strings.Count(s, "\n"); n > 0 {
		ts.line += n
		ts.col = 1 + utf8.RuneCountInString(s[strings.LastIndexByte(s, '\n')+1:])
	} else {
		ts.col += utf8.RuneCountInString(s)
	}
	return nil
}

func (ts *textStream) reader() (*bufio.Reader, error) {
	if ts.closed || ts.r == nil {
		return nil, newErr(54, "")
	}
	return ts.r, nil
}

func (ts *textStream) atEnd() (bool, error) {
	r, err := ts.reader()
	if err != nil {
		return false, err
	}
	_, perr := r.Peek(1)
	return perr != nil, nil
}

func (ts *textStream) readLine() (string, error) {
	r, err := ts.reader()
	if err != nil {
		return "", err
	}
	s, rerr := r.ReadString('\n')
	if rerr != nil && s == "" {
		return "", newErr(62, "")
	}
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	ts.line++
	ts.col = 1
	return s, nil
}

// readChars reads up to n characters, keeping bytes that are not valid
// UTF-8 as they are.
func (ts *textStream) readChars(n int64) (string, error) {
	r, err := ts.reader()
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for i := int64(0); i < n; i++ {
		c, size, rerr := r.ReadRune()
		if rerr != nil {
			if i == 0 {
				return "", newErr(62, "")
			}
			break
		}
		if c == utf8.RuneError && size == 1 {
			_ = r.UnreadRune()
			b, _ := r.ReadByte()
			sb.WriteByte(b)
		} else {
			sb.WriteRune(c)
		}
		if c == '\n' {
			ts.line++
			ts.col = 1
		} else {
			ts.col++
		}
	}
	return sb.String(), nil
}

func (ts *textStream) Get(name string, args []Value) (Value, error) {
	switch name {
	case "readline":
		return ts.readLine()
	case "readall":
		r, err := ts.reader()
		if err != nil {
			return nil, err
		}
		data, rerr := io.ReadAll(r)
		if rerr != nil {
			return nil, newErrMsg(57, "Device I/O error: "+rerr.Error())
		}
		s := string(data)
		ts.line += strings.Count(s, "\n")
		return s, nil
	case "read":
		n, err := optInt(args, 0, 0)
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			return "", nil
		}
		return ts.readChars(n)
	case "skip":
		n, err := optInt(args, 0, 0)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			_, err = ts.readChars(n)
		}
		return nil, err
	case "skipline":
		_, err := ts.readLine()
		return nil, err
	case "write":
		s, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		return nil, ts.write(s)
	case "writeline":
		s, err := optStr(args, 0, "")
		if err != nil {
			return nil, err
		}
		return nil, ts.write(s + ts.eol)
	case "writeblanklines":
		n, err := optInt(args, 0, 1)
		if err != nil {
			return nil, err
		}
		if n < 0 || n > 1<<20 {
			return nil, newErr(5, "")
		}
		return nil, ts.write(strings.Repeat(ts.eol, int(n)))
	case "close":
		if !ts.closed {
			ts.closed = true
			if ts.closer != nil {
				_ = ts.closer.Close()
			}
		}
		return nil, nil
	case "atendofstream":
		return ts.atEnd()
	case "atendofline":
		r, err := ts.reader()
		if err != nil {
			return nil, err
		}
		b, perr := r.Peek(1)
		return perr != nil || b[0] == '\n' || b[0] == '\r', nil
	case "line":
		return int64(ts.line), nil
	case "column":
		return int64(ts.col), nil
	}
	return nil, newErr(438, "")
}

func (ts *textStream) Set(string, []Value, Value) error { return newErr(438, "") }

// openTextFile implements OpenTextFile / OpenAsTextStream.
func openTextFile(path string, mode int64, create bool, format int64) (Value, error) {
	p := fsPath(path)
	ts := &textStream{eol: lineEnding, line: 1, col: 1}
	switch mode {
	case 1: // ForReading
		f, err := os.Open(p)
		if err != nil && create && errors.Is(err, fs.ErrNotExist) {
			f, err = os.OpenFile(p, os.O_RDONLY|os.O_CREATE, 0o644)
		}
		if err != nil {
			return nil, openError(err, p)
		}
		if st, serr := f.Stat(); serr == nil && st.IsDir() {
			f.Close()
			return nil, newErr(70, "")
		}
		ts.r, ts.closer = newReader(f, format == -1), f
	case 2, 8: // ForWriting, ForAppending
		flags := os.O_WRONLY
		if mode == 2 {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
		if create {
			flags |= os.O_CREATE
		}
		f, err := os.OpenFile(p, flags, 0o644)
		if err != nil {
			return nil, openError(err, p)
		}
		ts.w, ts.closer = f, f
		if format == -1 {
			ts.utf16 = true
			if st, serr := f.Stat(); serr == nil && st.Size() == 0 {
				_, _ = f.Write([]byte{0xFF, 0xFE})
			}
		}
	default:
		return nil, newErr(5, "")
	}
	return ts, nil
}

func createTextFile(path string, overwrite, unicode bool) (Value, error) {
	p := fsPath(path)
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(p, flags, 0o644)
	if err != nil {
		return nil, openError(err, p)
	}
	ts := &textStream{w: f, closer: f, eol: lineEnding, line: 1, col: 1, utf16: unicode}
	if unicode {
		_, _ = f.Write([]byte{0xFF, 0xFE})
	}
	return ts, nil
}

// ---- File and Folder ----

type fsoFile struct {
	path string
}

func (f *fsoFile) TypeName() string { return "File" }

func newFsoFile(path string) *fsoFile {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return &fsoFile{path: path}
}

func (f *fsoFile) Get(name string, args []Value) (Value, error) {
	switch name {
	case "", "path", "shortpath":
		return f.path, nil
	case "name", "shortname":
		return filepath.Base(f.path), nil
	case "parentfolder":
		return newFsoFolder(filepath.Dir(f.path)), nil
	case "drive":
		return filepath.VolumeName(f.path), nil
	case "type":
		if ext := strings.TrimPrefix(filepath.Ext(f.path), "."); ext != "" {
			return strings.ToUpper(ext) + " File", nil
		}
		return "File", nil
	case "size", "datelastmodified", "datecreated", "datelastaccessed", "attributes":
		st, err := os.Stat(f.path)
		if err != nil {
			return nil, fsError(err, 53)
		}
		switch name {
		case "size":
			return intValue(st.Size()), nil
		case "attributes":
			if st.Mode().Perm()&0o200 == 0 {
				return int64(1), nil
			}
			return int64(0), nil
		}
		return fileTime(st.ModTime()), nil
	case "delete":
		return nil, fsError(os.Remove(f.path), 53)
	case "copy":
		dst, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		overwrite, err := optBool(args, 1, true)
		if err != nil {
			return nil, err
		}
		return nil, copyFile(f.path, destination(dst, filepath.Base(f.path)), overwrite)
	case "move":
		dst, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		target := destination(dst, filepath.Base(f.path))
		if err := os.Rename(f.path, target); err != nil {
			return nil, fsError(err, 53)
		}
		f.path = target
		return nil, nil
	case "openastextstream":
		mode, err := optInt(args, 0, 1)
		if err != nil {
			return nil, err
		}
		format, err := optInt(args, 1, 0)
		if err != nil {
			return nil, err
		}
		return openTextFile(f.path, mode, false, format)
	}
	return nil, newErr(438, "")
}

func (f *fsoFile) Set(name string, args []Value, v Value) error {
	switch name {
	case "name":
		n, err := toStr(v)
		if err != nil {
			return err
		}
		target := filepath.Join(filepath.Dir(f.path), n)
		if err := os.Rename(f.path, target); err != nil {
			return fsError(err, 53)
		}
		f.path = target
		return nil
	case "attributes":
		return nil
	}
	return newErr(438, "")
}

type fsoFolder struct {
	path string
}

func (f *fsoFolder) TypeName() string { return "Folder" }

func newFsoFolder(path string) *fsoFolder {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return &fsoFolder{path: path}
}

// entries lists the files (dirs=false) or sub-folders (dirs=true).
func (f *fsoFolder) entries(dirs bool) (Value, error) {
	list, err := os.ReadDir(f.path)
	if err != nil {
		return nil, fsError(err, 76)
	}
	var items []Value
	names := map[string]Value{}
	for _, e := range list {
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			if st, serr := os.Stat(filepath.Join(f.path, e.Name())); serr == nil {
				isDir = st.IsDir()
			}
		}
		if isDir != dirs {
			continue
		}
		var item Value
		if dirs {
			item = &fsoFolder{path: filepath.Join(f.path, e.Name())}
		} else {
			item = &fsoFile{path: filepath.Join(f.path, e.Name())}
		}
		items = append(items, item)
		names[strings.ToLower(e.Name())] = item
	}
	typ := "Files"
	if dirs {
		typ = "Folders"
	}
	return &collection{typeName: typ, items: items, byKey: func(key string) (Value, bool) {
		v, ok := names[strings.ToLower(key)]
		return v, ok
	}}, nil
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func (f *fsoFolder) Get(name string, args []Value) (Value, error) {
	switch name {
	case "", "path", "shortpath":
		return f.path, nil
	case "name", "shortname":
		return filepath.Base(f.path), nil
	case "files":
		return f.entries(false)
	case "subfolders":
		return f.entries(true)
	case "parentfolder":
		parent := filepath.Dir(f.path)
		if parent == f.path {
			return Nothing, nil
		}
		return newFsoFolder(parent), nil
	case "isrootfolder":
		return filepath.Dir(f.path) == f.path, nil
	case "drive":
		return filepath.VolumeName(f.path), nil
	case "type":
		return "File folder", nil
	case "attributes":
		return int64(16), nil
	case "size":
		return intValue(dirSize(f.path)), nil
	case "datelastmodified", "datecreated", "datelastaccessed":
		st, err := os.Stat(f.path)
		if err != nil {
			return nil, fsError(err, 76)
		}
		return fileTime(st.ModTime()), nil
	case "delete":
		return nil, fsError(os.RemoveAll(f.path), 76)
	case "copy":
		dst, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		overwrite, err := optBool(args, 1, true)
		if err != nil {
			return nil, err
		}
		return nil, copyTree(f.path, fsPath(dst), overwrite)
	case "move":
		dst, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		target := destination(dst, filepath.Base(f.path))
		if err := os.Rename(f.path, target); err != nil {
			return nil, fsError(err, 76)
		}
		f.path = target
		return nil, nil
	case "createtextfile":
		n, err := argStrAt(args, 0)
		if err != nil {
			return nil, err
		}
		overwrite, err := optBool(args, 1, true)
		if err != nil {
			return nil, err
		}
		unicode, err := optBool(args, 2, false)
		if err != nil {
			return nil, err
		}
		return createTextFile(filepath.Join(f.path, n), overwrite, unicode)
	}
	return nil, newErr(438, "")
}

func (f *fsoFolder) Set(name string, args []Value, v Value) error {
	switch name {
	case "name":
		n, err := toStr(v)
		if err != nil {
			return err
		}
		target := filepath.Join(filepath.Dir(f.path), n)
		if err := os.Rename(f.path, target); err != nil {
			return fsError(err, 76)
		}
		f.path = target
		return nil
	case "attributes":
		return nil
	}
	return newErr(438, "")
}

// ---- FileSystemObject ----

type fsoObject struct {
	in *Interpreter
}

func (o *fsoObject) TypeName() string { return "FileSystemObject" }

func (o *fsoObject) Set(string, []Value, Value) error { return newErr(438, "") }

// glob expands a path whose last component may contain wildcards.
func glob(spec string, dirs bool) []string {
	p := fsPath(spec)
	var cands []string
	if hasWildcard(p) {
		cands, _ = filepath.Glob(p)
	} else {
		cands = []string{p}
	}
	var out []string
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() == dirs {
			out = append(out, c)
		}
	}
	return out
}

func (o *fsoObject) Get(name string, args []Value) (Value, error) {
	// Most methods take a path as their first argument.
	var p0 string
	if len(args) > 0 {
		var err error
		if p0, err = toStr(args[0]); err != nil {
			return nil, err
		}
	}
	need := func(n int) error {
		if len(args) < n {
			return newErr(450, "")
		}
		return nil
	}
	switch name {
	case "fileexists":
		if err := need(1); err != nil {
			return nil, err
		}
		st, err := os.Stat(fsPath(p0))
		return err == nil && !st.IsDir(), nil
	case "folderexists":
		if err := need(1); err != nil {
			return nil, err
		}
		st, err := os.Stat(fsPath(p0))
		return err == nil && st.IsDir(), nil
	case "driveexists":
		if err := need(1); err != nil {
			return nil, err
		}
		if runtime.GOOS == "windows" && len(p0) >= 1 {
			_, err := os.Stat(p0[:1] + `:\`)
			return err == nil, nil
		}
		return false, nil
	case "opentextfile":
		if err := need(1); err != nil {
			return nil, err
		}
		mode, err := optInt(args, 1, 1)
		if err != nil {
			return nil, err
		}
		create, err := optBool(args, 2, false)
		if err != nil {
			return nil, err
		}
		format, err := optInt(args, 3, 0)
		if err != nil {
			return nil, err
		}
		return openTextFile(p0, mode, create, format)
	case "createtextfile":
		if err := need(1); err != nil {
			return nil, err
		}
		overwrite, err := optBool(args, 1, true)
		if err != nil {
			return nil, err
		}
		unicode, err := optBool(args, 2, false)
		if err != nil {
			return nil, err
		}
		return createTextFile(p0, overwrite, unicode)
	case "deletefile":
		if err := need(1); err != nil {
			return nil, err
		}
		files := glob(p0, false)
		if len(files) == 0 {
			return nil, newErr(53, "")
		}
		for _, f := range files {
			if err := os.Remove(f); err != nil {
				return nil, fsError(err, 53)
			}
		}
		return nil, nil
	case "deletefolder":
		if err := need(1); err != nil {
			return nil, err
		}
		dirs := glob(p0, true)
		if len(dirs) == 0 {
			return nil, newErr(76, "")
		}
		for _, d := range dirs {
			if err := os.RemoveAll(d); err != nil {
				return nil, fsError(err, 76)
			}
		}
		return nil, nil
	case "copyfile", "movefile":
		if err := need(2); err != nil {
			return nil, err
		}
		dst, err := toStr(args[1])
		if err != nil {
			return nil, err
		}
		overwrite, err := optBool(args, 2, true)
		if err != nil {
			return nil, err
		}
		files := glob(p0, false)
		if len(files) == 0 {
			return nil, newErr(53, "")
		}
		for _, f := range files {
			target := destination(dst, filepath.Base(f))
			if hasWildcard(p0) {
				target = filepath.Join(fsPath(dst), filepath.Base(f))
			}
			if name == "copyfile" {
				if err := copyFile(f, target, overwrite); err != nil {
					return nil, err
				}
				continue
			}
			if _, serr := os.Stat(target); serr == nil {
				return nil, newErr(58, "")
			}
			if err := os.Rename(f, target); err != nil {
				if cerr := copyFile(f, target, false); cerr != nil {
					return nil, cerr
				}
				_ = os.Remove(f)
			}
		}
		return nil, nil
	case "copyfolder", "movefolder":
		if err := need(2); err != nil {
			return nil, err
		}
		dst, err := toStr(args[1])
		if err != nil {
			return nil, err
		}
		overwrite, err := optBool(args, 2, true)
		if err != nil {
			return nil, err
		}
		dirs := glob(p0, true)
		if len(dirs) == 0 {
			return nil, newErr(76, "")
		}
		for _, d := range dirs {
			target := fsPath(dst)
			if hasWildcard(p0) || (dst != "" && isSep(dst[len(dst)-1])) {
				target = filepath.Join(target, filepath.Base(d))
			}
			if name == "copyfolder" {
				if err := copyTree(d, target, overwrite); err != nil {
					return nil, err
				}
				continue
			}
			if err := os.Rename(d, target); err != nil {
				return nil, fsError(err, 76)
			}
		}
		return nil, nil
	case "createfolder":
		if err := need(1); err != nil {
			return nil, err
		}
		p := fsPath(p0)
		if err := os.Mkdir(p, 0o755); err != nil {
			return nil, fsError(err, 76)
		}
		return newFsoFolder(p), nil
	case "getfile":
		if err := need(1); err != nil {
			return nil, err
		}
		p := fsPath(p0)
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return nil, newErr(53, "")
		}
		return newFsoFile(p), nil
	case "getfolder":
		if err := need(1); err != nil {
			return nil, err
		}
		p := fsPath(p0)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return nil, newErr(76, "")
		}
		return newFsoFolder(p), nil
	case "buildpath":
		if err := need(2); err != nil {
			return nil, err
		}
		n, err := toStr(args[1])
		if err != nil {
			return nil, err
		}
		switch {
		case p0 == "":
			return n, nil
		case n == "":
			return p0, nil
		case isSep(p0[len(p0)-1]):
			return p0 + strings.TrimLeft(n, `/\`), nil
		case isSep(n[0]):
			return p0 + n, nil
		}
		return p0 + string(os.PathSeparator) + n, nil
	case "getfilename":
		if err := need(1); err != nil {
			return nil, err
		}
		_, n := splitPath(p0)
		return n, nil
	case "getbasename":
		if err := need(1); err != nil {
			return nil, err
		}
		_, n := splitPath(p0)
		if i := strings.LastIndexByte(n, '.'); i >= 0 {
			n = n[:i]
		}
		return n, nil
	case "getextensionname":
		if err := need(1); err != nil {
			return nil, err
		}
		_, n := splitPath(p0)
		if i := strings.LastIndexByte(n, '.'); i >= 0 {
			return n[i+1:], nil
		}
		return "", nil
	case "getparentfoldername":
		if err := need(1); err != nil {
			return nil, err
		}
		dir, n := splitPath(p0)
		if n == "" {
			return "", nil
		}
		if dir == "" && len(p0) > 0 && isSep(p0[0]) {
			return p0[:1], nil
		}
		if len(dir) == 2 && dir[1] == ':' {
			dir += `\`
		}
		return dir, nil
	case "getabsolutepathname":
		if err := need(1); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(fsPath(p0))
		if err != nil {
			return nil, fsError(err, 76)
		}
		return abs, nil
	case "getdrivename":
		if err := need(1); err != nil {
			return nil, err
		}
		if len(p0) >= 2 && p0[1] == ':' {
			return p0[:2], nil
		}
		return filepath.VolumeName(p0), nil
	case "gettempname":
		return fmt.Sprintf("rad%05X.tmp", rand.Intn(1<<20)), nil
	case "getspecialfolder":
		n, err := optInt(args, 0, 2)
		if err != nil {
			return nil, err
		}
		switch n {
		case 0:
			if root := os.Getenv("SystemRoot"); root != "" {
				return newFsoFolder(root), nil
			}
			return newFsoFolder(string(os.PathSeparator)), nil
		case 1:
			if root := os.Getenv("SystemRoot"); root != "" {
				return newFsoFolder(filepath.Join(root, "System32")), nil
			}
			return newFsoFolder("/usr/bin"), nil
		case 2:
			return newFsoFolder(os.TempDir()), nil
		}
		return nil, newErr(5, "")
	case "getstandardstream":
		n, err := optInt(args, 0, 1)
		if err != nil {
			return nil, err
		}
		switch n {
		case 0:
			return o.in.stdinStream(), nil
		case 1, 2:
			return o.in.stdoutStream(), nil
		}
		return nil, newErr(5, "")
	case "getfileversion":
		return "", nil
	case "drives":
		return &collection{typeName: "Drives"}, nil
	}
	return nil, newErr(438, "")
}
