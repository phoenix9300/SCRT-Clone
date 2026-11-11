package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
)

// SFTPSession is an SFTP channel, either sharing a terminal session's SSH
// connection or running on a connection of its own.
type SFTPSession struct {
	ID      uint32
	Title   string
	Session string
	Home    string

	mgr    *Manager
	client *sftp.Client
	sc     *sshConn
	once   sync.Once

	queue  chan *SFTPJob
	jobsMu sync.Mutex
	jobs   map[string]*SFTPJob
	closed chan struct{}
}

// FileEntry describes a remote or local file.
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	Perm    uint32 `json:"perm"`
	ModTime int64  `json:"mtime"`
	Dir     bool   `json:"dir"`
	Link    bool   `json:"link"`
	Target  string `json:"target,omitempty"`
	UID     uint32 `json:"uid,omitempty"`
	GID     uint32 `json:"gid,omitempty"`
}

var sftpSeq atomic.Uint32

type sftpRegistry struct {
	mu sync.Mutex
	m  map[uint32]*SFTPSession
}

var sftpSessions = &sftpRegistry{m: map[uint32]*SFTPSession{}}

// GetSFTP returns an open SFTP session.
func (m *Manager) GetSFTP(id uint32) *SFTPSession {
	sftpSessions.mu.Lock()
	defer sftpSessions.mu.Unlock()
	return sftpSessions.m[id]
}

// OpenSFTPOn opens an SFTP channel on the SSH connection of a terminal session.
func (m *Manager) OpenSFTPOn(c *Conn) (*SFTPSession, error) {
	sc := c.SSH()
	if sc == nil {
		return nil, errors.New("SFTP needs a connected SSH2 session")
	}
	sc.retain()
	s, err := m.newSFTP(sc, c.Options().SSH.SFTPSubsystem, c.Title, c.SessionPath)
	if err != nil {
		sc.release()
		return nil, err
	}
	return s, nil
}

// OpenSFTP connects to a session's host for file transfer only.
func (m *Manager) OpenSFTP(ctx context.Context, req OpenRequest, sink Sink) (*SFTPSession, error) {
	var ov map[string]any
	var err error
	if req.SessionPath != "" {
		if ov, err = m.Store.Overrides(req.SessionPath); err != nil {
			return nil, err
		}
		if req.Overrides != nil {
			deepMergeAny(ov, req.Overrides)
		}
	} else {
		ov = req.Overrides
	}
	opts, err := m.Store.Resolve(ov)
	if err != nil {
		return nil, err
	}
	if opts.Protocol != "ssh2" {
		return nil, errors.New("SFTP is only available for SSH2 sessions")
	}
	opts.SSH.Forwards = nil
	dreq := &DialRequest{Opts: opts, SessionPath: req.SessionPath, Store: m.Store, Manager: m, Password: req.Password,
		Prompter: sinkPrompter{sink: sink}}
	sc, err := sshConnect(ctx, dreq)
	if err != nil {
		return nil, err
	}
	title := req.Title
	if title == "" {
		title = Target(opts)
	}
	s, err := m.newSFTP(sc, opts.SSH.SFTPSubsystem, title, req.SessionPath)
	if err != nil {
		sc.release()
		return nil, err
	}
	return s, nil
}

// sinkPrompter routes prompts of connections that have no terminal tab.
type sinkPrompter struct{ sink Sink }

func (p sinkPrompter) Prompt(ctx context.Context, req PromptRequest) (PromptResponse, error) {
	if p.sink == nil {
		return PromptResponse{}, errors.New("no window is attached to answer the prompt")
	}
	return p.sink.Prompt(ctx, 0, req)
}

func (p sinkPrompter) Notice(string) {}

func (m *Manager) newSFTP(sc *sshConn, subsystem, title, session string) (*SFTPSession, error) {
	_ = subsystem // pkg/sftp always requests the standard "sftp" subsystem
	cl, err := sftp.NewClient(sc.client, sftp.UseConcurrentWrites(true), sftp.MaxPacket(32*1024))
	if err != nil {
		return nil, fmt.Errorf("cannot start the SFTP subsystem: %w", err)
	}
	s := &SFTPSession{
		ID: sftpSeq.Add(1), Title: title, Session: session, mgr: m, client: cl, sc: sc,
		queue: make(chan *SFTPJob, 1024), jobs: map[string]*SFTPJob{}, closed: make(chan struct{}),
	}
	if wd, err := cl.Getwd(); err == nil {
		s.Home = wd
	} else {
		s.Home = "/"
	}
	sftpSessions.mu.Lock()
	sftpSessions.m[s.ID] = s
	sftpSessions.mu.Unlock()
	go s.worker()
	go func() {
		_ = cl.Wait()
		s.Close()
	}()
	return s, nil
}

// Close ends the SFTP session.
func (s *SFTPSession) Close() {
	s.once.Do(func() {
		close(s.closed)
		s.jobsMu.Lock()
		for _, j := range s.jobs {
			if j.cancel != nil {
				j.cancel()
			}
		}
		s.jobsMu.Unlock()
		_ = s.client.Close()
		s.sc.release()
		sftpSessions.mu.Lock()
		delete(sftpSessions.m, s.ID)
		sftpSessions.mu.Unlock()
		s.mgr.Broadcast("sftp.closed", map[string]any{"id": s.ID})
	})
}

func entryFromInfo(dir string, fi fs.FileInfo) FileEntry {
	e := FileEntry{
		Name: fi.Name(), Path: path.Join(dir, fi.Name()), Size: fi.Size(), Mode: fi.Mode().String(),
		Perm: uint32(fi.Mode().Perm()), ModTime: fi.ModTime().UnixMilli(), Dir: fi.IsDir(),
		Link: fi.Mode()&fs.ModeSymlink != 0,
	}
	if st, ok := fi.Sys().(*sftp.FileStat); ok && st != nil {
		e.UID, e.GID = st.UID, st.GID
	}
	return e
}

func sortEntries(es []FileEntry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Dir != es[j].Dir {
			return es[i].Dir
		}
		return strings.ToLower(es[i].Name) < strings.ToLower(es[j].Name)
	})
}

// List reads a remote directory. Symlinks to directories are reported as directories.
func (s *SFTPSession) List(dir string) (string, []FileEntry, error) {
	if dir == "" || dir == "~" {
		dir = s.Home
	}
	real, err := s.client.RealPath(dir)
	if err != nil {
		return "", nil, err
	}
	infos, err := s.client.ReadDir(real)
	if err != nil {
		return real, nil, err
	}
	out := make([]FileEntry, 0, len(infos))
	for _, fi := range infos {
		e := entryFromInfo(real, fi)
		if e.Link {
			if t, err := s.client.ReadLink(e.Path); err == nil {
				e.Target = t
			}
			if st, err := s.client.Stat(e.Path); err == nil && st.IsDir() {
				e.Dir = true
			}
		}
		out = append(out, e)
	}
	sortEntries(out)
	return real, out, nil
}

// Stat describes one remote path.
func (s *SFTPSession) Stat(p string) (FileEntry, error) {
	fi, err := s.client.Stat(p)
	if err != nil {
		return FileEntry{}, err
	}
	e := entryFromInfo(path.Dir(p), fi)
	e.Path = p
	return e, nil
}

func (s *SFTPSession) Mkdir(p string) error              { return s.client.MkdirAll(p) }
func (s *SFTPSession) Rename(from, to string) error      { return s.client.PosixRename(from, to) }
func (s *SFTPSession) Chmod(p string, mode uint32) error { return s.client.Chmod(p, os.FileMode(mode)) }
func (s *SFTPSession) RealPath(p string) (string, error) { return s.client.RealPath(p) }

// Remove deletes a file or (recursively) a directory.
func (s *SFTPSession) Remove(p string) error {
	fi, err := s.client.Lstat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return s.client.RemoveAll(p)
	}
	return s.client.Remove(p)
}

// ReadFile returns the head of a remote file (for quick view / edit).
func (s *SFTPSession) ReadFile(p string, limit int64) ([]byte, bool, error) {
	f, err := s.client.Open(p)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if limit <= 0 {
		limit = 1 << 20
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return b[:limit], true, nil
	}
	return b, false, nil
}

// WriteFile replaces a remote file's contents.
func (s *SFTPSession) WriteFile(p string, data []byte) error {
	f, err := s.client.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ----------------------------------------------------------------- jobs

// SFTPJob is a queued upload or download (files or whole directory trees).
type SFTPJob struct {
	ID        string `json:"id"`
	Session   uint32 `json:"session"`
	Direction string `json:"direction"` // upload | download
	Local     string `json:"local"`
	Remote    string `json:"remote"`
	Name      string `json:"name"`
	State     string `json:"state"` // queued | running | done | error | cancelled
	Error     string `json:"error,omitempty"`
	Size      int64  `json:"size"`
	Done      int64  `json:"done"`
	Files     int    `json:"files"`
	FilesDone int    `json:"filesDone"`
	Current   string `json:"current,omitempty"`
	Started   int64  `json:"started,omitempty"`
	Finished  int64  `json:"finished,omitempty"`
	// Conflict: overwrite | skip | resume
	Conflict string `json:"conflict"`

	cancel   context.CancelFunc
	lastSent time.Time
	mu       sync.Mutex
}

var jobSeq atomic.Uint64

func (j *SFTPJob) snapshot() SFTPJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return SFTPJob{ID: j.ID, Session: j.Session, Direction: j.Direction, Local: j.Local, Remote: j.Remote, Name: j.Name,
		State: j.State, Error: j.Error, Size: j.Size, Done: j.Done, Files: j.Files, FilesDone: j.FilesDone,
		Current: j.Current, Started: j.Started, Finished: j.Finished, Conflict: j.Conflict}
}

// Enqueue schedules a transfer. local/remote are full paths of the source
// and the destination *directory* (the base name is kept).
func (s *SFTPSession) Enqueue(direction, local, remote, conflict string) (*SFTPJob, error) {
	if direction != "upload" && direction != "download" {
		return nil, errors.New("direction must be upload or download")
	}
	if conflict == "" {
		conflict = "overwrite"
	}
	name := filepath.Base(local)
	if direction == "download" {
		name = path.Base(remote)
	}
	j := &SFTPJob{ID: fmt.Sprintf("j%d", jobSeq.Add(1)), Session: s.ID, Direction: direction, Local: local, Remote: remote,
		Name: name, State: "queued", Conflict: conflict}
	s.jobsMu.Lock()
	s.jobs[j.ID] = j
	s.jobsMu.Unlock()
	select {
	case s.queue <- j:
	default:
		return nil, errors.New("transfer queue is full")
	}
	s.notify(j, true)
	return j, nil
}

// Jobs lists the session's transfers.
func (s *SFTPSession) Jobs() []SFTPJob {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	out := make([]SFTPJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j.snapshot())
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

// CancelJob stops a queued or running transfer.
func (s *SFTPSession) CancelJob(id string) {
	s.jobsMu.Lock()
	j := s.jobs[id]
	s.jobsMu.Unlock()
	if j == nil {
		return
	}
	j.mu.Lock()
	if j.State == "queued" {
		j.State = "cancelled"
	}
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.notify(j, true)
}

// ClearJobs forgets finished transfers.
func (s *SFTPSession) ClearJobs() {
	s.jobsMu.Lock()
	for id, j := range s.jobs {
		j.mu.Lock()
		fin := j.State == "done" || j.State == "error" || j.State == "cancelled"
		j.mu.Unlock()
		if fin {
			delete(s.jobs, id)
		}
	}
	s.jobsMu.Unlock()
}

func (s *SFTPSession) notify(j *SFTPJob, force bool) {
	j.mu.Lock()
	if !force && time.Since(j.lastSent) < 150*time.Millisecond {
		j.mu.Unlock()
		return
	}
	j.lastSent = time.Now()
	j.mu.Unlock()
	s.mgr.Broadcast("sftp.job", j.snapshot())
}

func (s *SFTPSession) worker() {
	for {
		select {
		case <-s.closed:
			return
		case j := <-s.queue:
			j.mu.Lock()
			if j.State != "queued" {
				j.mu.Unlock()
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			j.cancel = cancel
			j.State = "running"
			j.Started = time.Now().UnixMilli()
			j.mu.Unlock()
			s.notify(j, true)
			var err error
			if j.Direction == "download" {
				err = s.download(ctx, j)
			} else {
				err = s.upload(ctx, j)
			}
			cancel()
			j.mu.Lock()
			j.Finished = time.Now().UnixMilli()
			j.Current = ""
			switch {
			case err == nil:
				j.State = "done"
			case errors.Is(err, context.Canceled):
				j.State = "cancelled"
			default:
				j.State = "error"
				j.Error = err.Error()
			}
			j.mu.Unlock()
			s.notify(j, true)
		}
	}
}

// progress counts transferred bytes and aborts on cancellation.
type progress struct {
	ctx context.Context
	s   *SFTPSession
	j   *SFTPJob
}

func (p *progress) add(n int) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.j.mu.Lock()
	p.j.Done += int64(n)
	p.j.mu.Unlock()
	p.s.notify(p.j, false)
	return nil
}

type progressWriter struct {
	w io.Writer
	p *progress
}

func (w *progressWriter) Write(b []byte) (int, error) {
	n, err := w.w.Write(b)
	if perr := w.p.add(n); perr != nil && err == nil {
		err = perr
	}
	return n, err
}

type progressReader struct {
	r io.Reader
	p *progress
}

func (r *progressReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if perr := r.p.add(n); perr != nil && err == nil {
		err = perr
	}
	return n, err
}

func (s *SFTPSession) download(ctx context.Context, j *SFTPJob) error {
	fi, err := s.client.Stat(j.Remote)
	if err != nil {
		return err
	}
	p := &progress{ctx: ctx, s: s, j: j}
	dest := filepath.Join(j.Local, path.Base(j.Remote))
	if !fi.IsDir() {
		j.mu.Lock()
		j.Size, j.Files = fi.Size(), 1
		j.mu.Unlock()
		return s.downloadFile(p, j.Remote, dest, fi)
	}
	// plan the tree first so progress has a meaningful total
	type item struct {
		remote string
		fi     fs.FileInfo
	}
	var files []item
	var dirs []string
	w := s.client.Walk(j.Remote)
	for w.Step() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if w.Err() != nil {
			continue
		}
		if w.Stat().IsDir() {
			dirs = append(dirs, w.Path())
		} else if w.Stat().Mode().IsRegular() {
			files = append(files, item{w.Path(), w.Stat()})
			j.mu.Lock()
			j.Size += w.Stat().Size()
			j.Files++
			j.mu.Unlock()
		}
	}
	rel := func(r string) string {
		return filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(r, j.Remote), "/")))
	}
	for _, d := range dirs {
		if err := os.MkdirAll(rel(d), 0o755); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := s.downloadFile(p, f.remote, rel(f.remote), f.fi); err != nil {
			return err
		}
	}
	return nil
}

func (s *SFTPSession) downloadFile(p *progress, remote, local string, fi fs.FileInfo) error {
	j := p.j
	j.mu.Lock()
	j.Current = remote
	j.mu.Unlock()
	var offset int64
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if st, err := os.Stat(local); err == nil && !st.IsDir() {
		switch j.Conflict {
		case "skip":
			_ = p.add(int(fi.Size()))
			j.mu.Lock()
			j.FilesDone++
			j.mu.Unlock()
			return nil
		case "resume":
			if st.Size() <= fi.Size() {
				offset = st.Size()
				flags = os.O_WRONLY | os.O_APPEND
			}
		}
	}
	rf, err := s.client.Open(remote)
	if err != nil {
		return err
	}
	defer rf.Close()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(local, flags, 0o644)
	if err != nil {
		return err
	}
	if offset > 0 {
		if _, err := rf.Seek(offset, io.SeekStart); err != nil {
			_ = lf.Close()
			return err
		}
		_ = p.add(int(offset))
	}
	_, err = rf.WriteTo(&progressWriter{w: lf, p: p})
	cerr := lf.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	_ = os.Chtimes(local, time.Now(), fi.ModTime())
	if perm := fi.Mode().Perm(); perm != 0 {
		_ = os.Chmod(local, perm)
	}
	j.mu.Lock()
	j.FilesDone++
	j.mu.Unlock()
	return nil
}

func (s *SFTPSession) upload(ctx context.Context, j *SFTPJob) error {
	fi, err := os.Stat(j.Local)
	if err != nil {
		return err
	}
	p := &progress{ctx: ctx, s: s, j: j}
	dest := path.Join(j.Remote, filepath.Base(j.Local))
	if !fi.IsDir() {
		j.mu.Lock()
		j.Size, j.Files = fi.Size(), 1
		j.mu.Unlock()
		return s.uploadFile(p, j.Local, dest, fi)
	}
	type item struct {
		local string
		fi    fs.FileInfo
	}
	var files []item
	var dirs []string
	err = filepath.Walk(j.Local, func(pth string, info fs.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, pth)
		} else if info.Mode().IsRegular() {
			files = append(files, item{pth, info})
			j.mu.Lock()
			j.Size += info.Size()
			j.Files++
			j.mu.Unlock()
		}
		return nil
	})
	if err != nil {
		return err
	}
	rel := func(l string) string {
		r, _ := filepath.Rel(j.Local, l)
		return path.Join(dest, filepath.ToSlash(r))
	}
	for _, d := range dirs {
		if err := s.client.MkdirAll(rel(d)); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := s.uploadFile(p, f.local, rel(f.local), f.fi); err != nil {
			return err
		}
	}
	return nil
}

func (s *SFTPSession) uploadFile(p *progress, local, remote string, fi fs.FileInfo) error {
	j := p.j
	j.mu.Lock()
	j.Current = local
	j.mu.Unlock()
	var offset int64
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if st, err := s.client.Stat(remote); err == nil && !st.IsDir() {
		switch j.Conflict {
		case "skip":
			_ = p.add(int(fi.Size()))
			j.mu.Lock()
			j.FilesDone++
			j.mu.Unlock()
			return nil
		case "resume":
			if st.Size() <= fi.Size() {
				offset = st.Size()
				flags = os.O_WRONLY
			}
		}
	}
	lf, err := os.Open(local)
	if err != nil {
		return err
	}
	defer lf.Close()
	rf, err := s.client.OpenFile(remote, flags)
	if err != nil {
		return err
	}
	if offset > 0 {
		if _, err := lf.Seek(offset, io.SeekStart); err != nil {
			_ = rf.Close()
			return err
		}
		if _, err := rf.Seek(offset, io.SeekStart); err != nil {
			_ = rf.Close()
			return err
		}
		_ = p.add(int(offset))
	}
	_, err = rf.ReadFrom(&progressReader{r: lf, p: p})
	cerr := rf.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	_ = s.client.Chtimes(remote, time.Now(), fi.ModTime())
	if perm := fi.Mode().Perm(); perm != 0 {
		_ = s.client.Chmod(remote, perm)
	}
	j.mu.Lock()
	j.FilesDone++
	j.mu.Unlock()
	return nil
}

// LocalList reads a local directory for the file browser.
func LocalList(dir string) (string, []FileEntry, error) {
	if dir == "" || dir == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		} else {
			dir = string(filepath.Separator)
		}
	}
	dir = expandHome(dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return abs, nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, de := range entries {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		e := FileEntry{
			Name: fi.Name(), Path: filepath.Join(abs, fi.Name()), Size: fi.Size(), Mode: fi.Mode().String(),
			Perm: uint32(fi.Mode().Perm()), ModTime: fi.ModTime().UnixMilli(), Dir: fi.IsDir(),
			Link: fi.Mode()&fs.ModeSymlink != 0,
		}
		if e.Link {
			if t, err := os.Readlink(e.Path); err == nil {
				e.Target = t
			}
			if st, err := os.Stat(e.Path); err == nil && st.IsDir() {
				e.Dir = true
			}
		}
		out = append(out, e)
	}
	sortEntries(out)
	return abs, out, nil
}
