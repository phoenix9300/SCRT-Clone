package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// handleUpload stores a file sent by the browser (drag & drop from the
// desktop) in a private temporary directory and returns its path so it can
// be used as the source of an SFTP or ZMODEM upload.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.checkOrigin(r) || !s.checkToken(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := filepath.Base(strings.ReplaceAll(r.URL.Query().Get("name"), "\\", "/"))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return
	}
	dir, err := os.MkdirTemp("", "hawser-upload-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dst := filepath.Join(dir, name)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, 8<<30))
	cerr := f.Close()
	if err != nil || cerr != nil {
		_ = os.RemoveAll(dir)
		http.Error(w, "upload failed", http.StatusInternalServerError)
		return
	}
	s.ext.trackTemp(dir)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"path": dst})
}

// handleFile streams a local file to the browser (used to save log files or
// downloads through the browser when the UI runs on another machine).
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.checkOrigin(r) || !s.checkToken(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	p := r.URL.Query().Get("path")
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(filepath.Base(p)))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, p)
}

func urlEscape(s string) string {
	const hexd = "0123456789ABCDEF"
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("-._~", c) >= 0 {
			sb.WriteByte(c)
		} else {
			sb.WriteByte('%')
			sb.WriteByte(hexd[c>>4])
			sb.WriteByte(hexd[c&15])
		}
	}
	return sb.String()
}

// handleOpen lets a second `hawser` invocation hand its command-line targets
// to this running instance.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.checkToken(r) || r.Header.Get("Origin") != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Startup []map[string]any `json:"startup"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.anyClient() == nil {
		http.Error(w, "no window", http.StatusServiceUnavailable)
		return
	}
	s.anyClient().Event("app.startup", body.Startup)
	w.WriteHeader(http.StatusOK)
}
