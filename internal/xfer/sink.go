package xfer

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// sanitizeName reduces a file name received from a remote peer to a safe
// base name: directory components are stripped (both '/' and '\' count as
// separators), control characters are replaced by '_' and names that are
// empty or refer to a directory ("." and "..") are rejected.
func sanitizeName(raw string) (string, error) {
	s := raw
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c == 0x7f {
			b[i] = '_'
		}
	}
	s = strings.TrimSpace(string(b))
	if s == "" || s == "." || s == ".." {
		return "", fmt.Errorf("xfer: unsafe file name %q", raw)
	}
	return s, nil
}

// windowsSafeName additionally replaces characters and names that Windows
// file systems reject. It is applied by DirSink when running on Windows.
func windowsSafeName(name string) string {
	b := []byte(name)
	for i, c := range b {
		switch c {
		case '<', '>', ':', '"', '|', '?', '*':
			b[i] = '_'
		}
	}
	name = strings.TrimRight(string(b), " .")
	if name == "" {
		return "_"
	}
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch strings.ToUpper(strings.TrimSpace(stem)) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "_" + name
	}
	return name
}

// parseFileHeader decodes the file information carried by a ZMODEM ZFILE
// subpacket or a YMODEM block 0: the NUL-terminated path name followed by
// the optional, space separated fields length (decimal), modification time
// (octal seconds since the epoch) and mode (octal).
func parseFileHeader(data []byte) (FileInfo, error) {
	info := FileInfo{Size: -1}
	name := data
	var rest []byte
	for i, c := range data {
		if c == 0 {
			name, rest = data[:i], data[i+1:]
			break
		}
	}
	clean, err := sanitizeName(string(name))
	if err != nil {
		return info, err
	}
	info.Name = clean
	for i, c := range rest {
		if c == 0 {
			rest = rest[:i]
			break
		}
	}
	fields := strings.Fields(string(rest))
	if len(fields) > 0 {
		if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil && n >= 0 {
			info.Size = n
		}
	}
	if len(fields) > 1 {
		if t, err := strconv.ParseInt(fields[1], 8, 64); err == nil && t > 0 {
			info.ModTime = time.Unix(t, 0)
		}
	}
	if len(fields) > 2 {
		if m, err := strconv.ParseUint(fields[2], 8, 32); err == nil {
			info.Mode = uint32(m) & 0o7777
		}
	}
	return info, nil
}

// buildFileHeader encodes file information for ZFILE / YMODEM block 0.
// filesLeft and bytesLeft describe the remainder of the batch including this
// file; they are omitted when filesLeft is zero.
func buildFileHeader(info FileInfo, size int64, filesLeft int, bytesLeft int64) []byte {
	name := wireName(info.Name)
	b := make([]byte, 0, len(name)+64)
	b = append(b, name...)
	b = append(b, 0)
	b = strconv.AppendInt(b, size, 10)
	b = append(b, ' ')
	var mtime int64
	if !info.ModTime.IsZero() {
		mtime = max(info.ModTime.Unix(), 0)
	}
	b = strconv.AppendInt(b, mtime, 8)
	b = append(b, ' ')
	var mode uint64
	if info.Mode != 0 {
		mode = 0o100000 | uint64(info.Mode&0o7777) // regular file
	}
	b = strconv.AppendUint(b, mode, 8)
	b = append(b, " 0"...) // serial number
	if filesLeft > 0 {
		b = append(b, ' ')
		b = strconv.AppendInt(b, int64(filesLeft), 10)
		b = append(b, ' ')
		b = strconv.AppendInt(b, bytesLeft, 10)
	}
	return append(b, 0)
}

// wireName is the name announced to the receiver: the base name only.
func wireName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		name = "unnamed"
	}
	return name
}

// DirSink saves files into a directory; Resume enables crash recovery,
// Overwrite controls collisions (otherwise auto-rename "name (1).ext").
//
// With Resume set, an existing regular file that is shorter than the
// announced size is appended to (the sender is asked to continue at its
// length) and an existing file of exactly the announced size is skipped as
// already complete. Resume takes precedence over Overwrite.
//
// Files are created with the transmitted permission bits (default 0644) and
// receive the transmitted modification time when they are closed. Partial
// files of failed transfers are kept so that they can be resumed.
type DirSink struct {
	Dir       string
	Overwrite bool
	Resume    bool
}

// Create implements Sink.
func (d DirSink) Create(info FileInfo) (io.WriteCloser, int64, error) {
	name, err := sanitizeName(info.Name)
	if err != nil {
		return nil, 0, err
	}
	if runtime.GOOS == "windows" {
		name = windowsSafeName(name)
	}
	dir := d.Dir
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, 0, err
	}
	path := filepath.Join(dir, name)

	st, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return &dirFile{File: f, path: path, info: info}, 0, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, 0, err
		}
		// Lost a race with another creator: fall through to renaming.
	case err != nil:
		return nil, 0, err
	case st.Mode().IsRegular():
		if d.Resume && info.Size >= 0 {
			if st.Size() == info.Size {
				return nil, 0, ErrSkip
			}
			if st.Size() < info.Size {
				f, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					return nil, 0, err
				}
				if _, err := f.Seek(st.Size(), io.SeekStart); err != nil {
					f.Close()
					return nil, 0, err
				}
				return &dirFile{File: f, path: path, info: info}, st.Size(), nil
			}
		}
		if d.Overwrite {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
			if err != nil {
				return nil, 0, err
			}
			return &dirFile{File: f, path: path, info: info}, 0, nil
		}
	}

	// The name is taken (by a file we must not replace, a directory, a
	// symlink ...): pick "name (n).ext".
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" { // dot file such as ".profile"
		stem, ext = name, ""
	}
	for i := 1; i < 100000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return &dirFile{File: f, path: p, info: info}, 0, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, 0, err
		}
	}
	return nil, 0, fmt.Errorf("xfer: cannot find a free name for %q", name)
}

// dirFile applies the transmitted metadata when the file is closed.
type dirFile struct {
	*os.File
	path string
	info FileInfo
}

func (f *dirFile) Close() error {
	err := f.File.Close()
	if f.info.Mode&0o777 != 0 && runtime.GOOS != "windows" {
		_ = os.Chmod(f.path, os.FileMode(f.info.Mode&0o777))
	}
	if !f.info.ModTime.IsZero() {
		_ = os.Chtimes(f.path, time.Time{}, f.info.ModTime)
	}
	return err
}
