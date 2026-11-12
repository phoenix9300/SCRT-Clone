package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

func (s *Server) on(method string, h handler) { s.handlers[method] = h }

type idParam struct {
	ID uint32 `json:"id"`
}

func (s *Server) connOf(raw json.RawMessage) (*conn.Conn, error) {
	p, err := decode[idParam](raw)
	if err != nil {
		return nil, err
	}
	c := s.Mgr.Get(p.ID)
	if c == nil {
		return nil, errors.New("no such connection")
	}
	return c, nil
}

func (s *Server) registerCore() {
	// ------------------------------------------------------------ app
	s.on("app.info", func(c *client, _ json.RawMessage) (any, error) {
		home, _ := os.UserHomeDir()
		host, _ := os.Hostname()
		s.startupMu.Lock()
		startup := s.Startup
		s.Startup = nil
		s.startupMu.Unlock()
		return map[string]any{
			"version":       Version,
			"os":            runtime.GOOS,
			"arch":          runtime.GOARCH,
			"configDir":     s.Store.Dir,
			"home":          home,
			"hostname":      host,
			"pathSep":       string(filepath.Separator),
			"defaultShell":  conn.DefaultShell(),
			"logsDir":       s.Store.LogsDir(),
			"scriptsDir":    s.Store.ScriptsDir(),
			"keysDir":       s.Store.KeysDir(),
			"startup":       startup,
			"sshAlgorithms": conn.SupportedSSHAlgorithms(),
		}, nil
	})
	s.on("app.quit", func(c *client, _ json.RawMessage) (any, error) {
		go func() {
			time.Sleep(150 * time.Millisecond)
			s.Quit()
		}()
		return true, nil
	})

	// --------------------------------------------------------- global
	s.on("global.get", func(c *client, _ json.RawMessage) (any, error) { return s.Store.Global() })
	s.on("global.set", func(c *client, raw json.RawMessage) (any, error) {
		patch, err := decode[map[string]any](raw)
		if err != nil {
			return nil, err
		}
		g, err := s.Store.SetGlobal(patch)
		if err == nil {
			s.broadcastExcept(c, "global.changed", g)
		}
		return g, err
	})

	// ------------------------------------------------------- sessions
	s.on("sessions.tree", func(c *client, _ json.RawMessage) (any, error) { return s.Store.Tree() })
	s.on("sessions.get", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Path string `json:"path"`
		}](raw)
		if err != nil {
			return nil, err
		}
		def, err := s.Store.Defaults()
		if err != nil {
			return nil, err
		}
		var ov map[string]any
		if p.Path == "" {
			// the default session itself: edits apply on top of factory settings
			ov, err = s.Store.DefaultOverrides()
			if err != nil {
				return nil, err
			}
			b, _ := json.Marshal(store.FactoryDefaults())
			var fac map[string]any
			_ = json.Unmarshal(b, &fac)
			return map[string]any{"path": "", "overrides": ov, "defaults": fac, "secrets": map[string]bool{}}, nil
		}
		ov, err = s.Store.Overrides(p.Path)
		if err != nil {
			return nil, err
		}
		secrets := map[string]bool{}
		for _, k := range []string{"password", "proxy"} {
			secrets[k] = s.Store.Vault.Has(store.SecretKey(p.Path, k))
		}
		for _, n := range s.Store.Vault.Names() {
			pre := store.SecretKey(p.Path, "logon:")
			if strings.HasPrefix(n, pre) {
				secrets["logon:"+strings.TrimPrefix(n, pre)] = true
			}
		}
		return map[string]any{"path": p.Path, "overrides": ov, "defaults": def, "secrets": secrets}, nil
	})
	s.on("sessions.save", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Path      string             `json:"path"`
			Overrides map[string]any     `json:"overrides"`
			Secrets   map[string]*string `json:"secrets"` // value = set, null = delete
			MustBeNew bool               `json:"mustBeNew"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.Overrides == nil {
			p.Overrides = map[string]any{}
		}
		if p.Path == "" {
			if err := s.Store.SaveDefault(p.Overrides); err != nil {
				return nil, err
			}
		} else {
			if p.MustBeNew && s.Store.Exists(p.Path) {
				return nil, fmt.Errorf("a session named %q already exists", p.Path)
			}
			if err := s.Store.Save(p.Path, p.Overrides); err != nil {
				return nil, err
			}
			for k, v := range p.Secrets {
				key := store.SecretKey(p.Path, k)
				if v == nil || *v == "" {
					s.Store.Vault.Delete(key)
				} else if err := s.Store.Vault.Set(key, *v); err != nil {
					return nil, err
				}
			}
		}
		s.broadcast("sessions.changed", nil)
		return true, nil
	})
	s.on("sessions.delete", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Path   string `json:"path"`
			Folder bool   `json:"folder"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.Folder {
			err = s.Store.Rmdir(p.Path)
		} else {
			err = s.Store.Delete(p.Path)
		}
		s.broadcast("sessions.changed", nil)
		return err == nil, err
	})
	s.on("sessions.rename", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			From   string `json:"from"`
			To     string `json:"to"`
			Folder bool   `json:"folder"`
		}](raw)
		if err != nil {
			return nil, err
		}
		err = s.Store.Rename(p.From, p.To, p.Folder)
		s.broadcast("sessions.changed", nil)
		return err == nil, err
	})
	s.on("sessions.copy", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			From string `json:"from"`
			To   string `json:"to"`
		}](raw)
		if err != nil {
			return nil, err
		}
		err = s.Store.Copy(p.From, p.To)
		s.broadcast("sessions.changed", nil)
		return err == nil, err
	})
	s.on("sessions.mkdir", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Path string `json:"path"`
		}](raw)
		if err != nil {
			return nil, err
		}
		err = s.Store.Mkdir(p.Path)
		s.broadcast("sessions.changed", nil)
		return err == nil, err
	})

	// ---------------------------------------------------- connections
	s.on("conn.open", func(c *client, raw json.RawMessage) (any, error) {
		req, err := decode[conn.OpenRequest](raw)
		if err != nil {
			return nil, err
		}
		cn, err := s.Mgr.Open(req, c)
		if err != nil {
			return nil, err
		}
		return cn.Info(true), nil
	})
	s.on("conn.list", func(c *client, _ json.RawMessage) (any, error) {
		out := []conn.Info{}
		for _, cn := range s.Mgr.List() {
			out = append(out, cn.Info(true))
		}
		return out, nil
	})
	s.on("conn.attach", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		replay := cn.Attach(c)
		for len(replay) > 0 {
			n := len(replay)
			if n > 256*1024 {
				n = 256 * 1024
			}
			c.Data(cn.ID, replay[:n])
			replay = replay[n:]
		}
		return cn.Info(true), nil
	})
	s.on("conn.info", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		return cn.Info(true), nil
	})
	s.on("conn.close", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[idParam](raw)
		if err != nil {
			return nil, err
		}
		s.Mgr.Close(p.ID)
		return true, nil
	})
	s.on("conn.disconnect", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		cn.Disconnect()
		return true, nil
	})
	s.on("conn.reconnect", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		cn.Attach(c)
		cn.Reconnect()
		return true, nil
	})
	s.on("conn.resize", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID   uint32 `json:"id"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if cn := s.Mgr.Get(p.ID); cn != nil {
			cn.Resize(p.Cols, p.Rows)
		}
		return true, nil
	})
	s.on("conn.ack", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID    uint32 `json:"id"`
			Bytes int64  `json:"bytes"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if cn := s.Mgr.Get(p.ID); cn != nil {
			cn.Ack(p.Bytes)
		}
		return nil, nil
	})
	// conn.send: text with optional pacing (paste, button bar, command window)
	s.on("conn.send", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID        uint32   `json:"id"`
			IDs       []uint32 `json:"ids"`
			Text      string   `json:"text"`
			Escapes   bool     `json:"escapes"`
			CharDelay int      `json:"charDelay"`
			LineDelay int      `json:"lineDelay"`
		}](raw)
		if err != nil {
			return nil, err
		}
		ids := p.IDs
		if p.ID != 0 {
			ids = append(ids, p.ID)
		}
		for _, id := range ids {
			cn := s.Mgr.Get(id)
			if cn == nil {
				continue
			}
			// queued in call order; never blocks the window's message loop
			if p.Escapes {
				cn.QueueString(p.Text)
			} else {
				cn.QueueSend([]byte(p.Text), time.Duration(p.CharDelay)*time.Millisecond, time.Duration(p.LineDelay)*time.Millisecond)
			}
		}
		return true, nil
	})
	s.on("conn.break", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		return true, cn.SendBreak()
	})
	s.on("conn.log.start", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID uint32 `json:"id"`
			conn.LogRequest
		}](raw)
		if err != nil {
			return nil, err
		}
		cn := s.Mgr.Get(p.ID)
		if cn == nil {
			return nil, errors.New("no such connection")
		}
		return cn.StartLog(p.LogRequest)
	})
	s.on("conn.log.stop", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		cn.StopLog()
		return true, nil
	})
	s.on("conn.record.start", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		cn.StartRecording()
		return true, nil
	})
	s.on("conn.record.stop", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID   uint32 `json:"id"`
			Lang string `json:"lang"`
		}](raw)
		if err != nil {
			return nil, err
		}
		cn := s.Mgr.Get(p.ID)
		if cn == nil {
			return nil, errors.New("no such connection")
		}
		steps := cn.StopRecording()
		return map[string]any{"steps": steps, "script": conn.GenerateScript(steps, p.Lang)}, nil
	})
	s.on("conn.forwards", func(c *client, raw json.RawMessage) (any, error) {
		cn, err := s.connOf(raw)
		if err != nil {
			return nil, err
		}
		f := cn.Forwards()
		if f == nil {
			f = []conn.ForwardStatus{}
		}
		return f, nil
	})
	s.on("conn.forward.add", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID   uint32            `json:"id"`
			Spec store.PortForward `json:"spec"`
		}](raw)
		if err != nil {
			return nil, err
		}
		cn := s.Mgr.Get(p.ID)
		if cn == nil {
			return nil, errors.New("no such connection")
		}
		st, err := cn.AddForward(p.Spec)
		if err != nil {
			return nil, err
		}
		return st, nil
	})
	s.on("conn.forward.remove", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID   uint32 `json:"id"`
			Name string `json:"name"`
		}](raw)
		if err != nil {
			return nil, err
		}
		cn := s.Mgr.Get(p.ID)
		if cn == nil {
			return nil, errors.New("no such connection")
		}
		return cn.RemoveForward(p.Name), nil
	})

	// ------------------------------------------------- in-band transfers
	s.on("xfer.start", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID uint32 `json:"id"`
			conn.TransferRequest
		}](raw)
		if err != nil {
			return nil, err
		}
		cn := s.Mgr.Get(p.ID)
		if cn == nil {
			return nil, errors.New("no such connection")
		}
		if p.Protocol == "ascii" {
			if len(p.Files) == 0 {
				return nil, errors.New("no file selected")
			}
			return true, cn.SendASCII(p.Files[0])
		}
		return true, cn.StartTransfer(p.TransferRequest)
	})
	s.on("xfer.cancel", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Transfer string `json:"transfer"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return conn.CancelTransfer(p.Transfer), nil
	})
	s.on("xfer.answerUpload", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID    uint32   `json:"id"`
			Files []string `json:"files"`
		}](raw)
		if err != nil {
			return nil, err
		}
		cn := s.Mgr.Get(p.ID)
		if cn == nil {
			return nil, errors.New("no such connection")
		}
		return cn.AnswerUpload(p.Files), nil
	})

	// ------------------------------------------------------------- SFTP
	s.on("sftp.open", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Conn uint32 `json:"conn"`
			conn.OpenRequest
		}](raw)
		if err != nil {
			return nil, err
		}
		var ss *conn.SFTPSession
		if p.Conn != 0 {
			cn := s.Mgr.Get(p.Conn)
			if cn == nil {
				return nil, errors.New("no such connection")
			}
			ss, err = s.Mgr.OpenSFTPOn(cn)
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			ss, err = s.Mgr.OpenSFTP(ctx, p.OpenRequest, c)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": ss.ID, "title": ss.Title, "home": ss.Home, "session": ss.Session}, nil
	})
	sftpOf := func(id uint32) (*conn.SFTPSession, error) {
		ss := s.Mgr.GetSFTP(id)
		if ss == nil {
			return nil, errors.New("the SFTP session is closed")
		}
		return ss, nil
	}
	type sftpPath struct {
		ID   uint32 `json:"id"`
		Path string `json:"path"`
		To   string `json:"to"`
		Mode uint32 `json:"mode"`
		Data string `json:"data"`
	}
	s.on("sftp.close", func(c *client, raw json.RawMessage) (any, error) {
		p, _ := decode[sftpPath](raw)
		if ss := s.Mgr.GetSFTP(p.ID); ss != nil {
			ss.Close()
		}
		return true, nil
	})
	s.on("sftp.list", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[sftpPath](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		dir, entries, err := ss.List(p.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": dir, "entries": entries}, nil
	})
	s.on("sftp.mkdir", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[sftpPath](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		return true, ss.Mkdir(p.Path)
	})
	s.on("sftp.remove", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[sftpPath](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		return true, ss.Remove(p.Path)
	})
	s.on("sftp.rename", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[sftpPath](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		return true, ss.Rename(p.Path, p.To)
	})
	s.on("sftp.chmod", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[sftpPath](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		return true, ss.Chmod(p.Path, p.Mode)
	})
	s.on("sftp.read", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[sftpPath](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		b, truncated, err := ss.ReadFile(p.Path, 2<<20)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": string(b), "truncated": truncated}, nil
	})
	s.on("sftp.write", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[sftpPath](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		return true, ss.WriteFile(p.Path, []byte(p.Data))
	})
	s.on("sftp.transfer", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID        uint32   `json:"id"`
			Direction string   `json:"direction"`
			Sources   []string `json:"sources"`
			Dest      string   `json:"dest"`
			Conflict  string   `json:"conflict"`
		}](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		n := 0
		for _, src := range p.Sources {
			if p.Direction == "upload" {
				_, err = ss.Enqueue("upload", src, p.Dest, p.Conflict)
			} else {
				_, err = ss.Enqueue("download", p.Dest, src, p.Conflict)
			}
			if err != nil {
				return nil, err
			}
			n++
		}
		return n, nil
	})
	s.on("sftp.jobs", func(c *client, raw json.RawMessage) (any, error) {
		p, _ := decode[sftpPath](raw)
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		return ss.Jobs(), nil
	})
	s.on("sftp.cancel", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID  uint32 `json:"id"`
			Job string `json:"job"`
		}](raw)
		if err != nil {
			return nil, err
		}
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		ss.CancelJob(p.Job)
		return true, nil
	})
	s.on("sftp.clear", func(c *client, raw json.RawMessage) (any, error) {
		p, _ := decode[sftpPath](raw)
		ss, err := sftpOf(p.ID)
		if err != nil {
			return nil, err
		}
		ss.ClearJobs()
		return ss.Jobs(), nil
	})

	// ---------------------------------------------------------- local fs
	type fsPath struct {
		Path string `json:"path"`
		To   string `json:"to"`
		Data string `json:"data"`
	}
	s.on("fs.list", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[fsPath](raw)
		if err != nil {
			return nil, err
		}
		dir, entries, err := conn.LocalList(p.Path)
		if err != nil {
			return map[string]any{"path": dir, "entries": []conn.FileEntry{}, "error": err.Error()}, nil
		}
		return map[string]any{"path": dir, "entries": entries, "parent": filepath.Dir(dir), "roots": fsRoots()}, nil
	})
	s.on("fs.mkdir", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[fsPath](raw)
		if err != nil {
			return nil, err
		}
		return true, os.MkdirAll(p.Path, 0o755)
	})
	s.on("fs.remove", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[fsPath](raw)
		if err != nil {
			return nil, err
		}
		if p.Path == "" || filepath.Dir(p.Path) == p.Path {
			return nil, errors.New("refusing to remove a root directory")
		}
		return true, os.RemoveAll(p.Path)
	})
	s.on("fs.rename", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[fsPath](raw)
		if err != nil {
			return nil, err
		}
		return true, os.Rename(p.Path, p.To)
	})
	s.on("fs.readText", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[fsPath](raw)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p.Path)
		if err != nil {
			return nil, err
		}
		if st.Size() > 8<<20 {
			return nil, errors.New("file is too large to open here")
		}
		b, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": string(b), "path": p.Path}, nil
	})
	s.on("fs.writeText", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[fsPath](raw)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
			return nil, err
		}
		return true, os.WriteFile(p.Path, []byte(p.Data), 0o644)
	})
	s.on("fs.stat", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[fsPath](raw)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p.Path)
		if err != nil {
			return map[string]any{"exists": false}, nil
		}
		return map[string]any{"exists": true, "dir": st.IsDir(), "size": st.Size(), "mtime": st.ModTime().UnixMilli()}, nil
	})

	// ------------------------------------------------------------- vault
	vaultStatus := func() map[string]any {
		v := s.Store.Vault
		return map[string]any{"hasPassphrase": v.HasPassphrase(), "locked": v.Locked(), "count": v.Count()}
	}
	s.on("vault.status", func(c *client, _ json.RawMessage) (any, error) { return vaultStatus(), nil })
	s.on("vault.unlock", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Passphrase string `json:"passphrase"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if err := s.Store.Vault.Unlock(p.Passphrase); err != nil {
			return nil, err
		}
		s.broadcast("vault.changed", vaultStatus())
		return vaultStatus(), nil
	})
	s.on("vault.lock", func(c *client, _ json.RawMessage) (any, error) {
		s.Store.Vault.Lock()
		s.broadcast("vault.changed", vaultStatus())
		return vaultStatus(), nil
	})
	s.on("vault.setPassphrase", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Passphrase string `json:"passphrase"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if err := s.Store.Vault.SetPassphrase(p.Passphrase); err != nil {
			return nil, err
		}
		s.broadcast("vault.changed", vaultStatus())
		return vaultStatus(), nil
	})

	// ------------------------------------------------- shared credentials
	s.on("credentials.list", func(c *client, _ json.RawMessage) (any, error) {
		out := []map[string]any{}
		for _, n := range s.Store.Vault.CredentialNames() {
			e := map[string]any{"name": n}
			if cr, ok, err := s.Store.Vault.Credential(n); ok && err == nil {
				e["username"] = cr.Username
				e["hasPassword"] = cr.Password != ""
			}
			out = append(out, e)
		}
		return out, nil
	})
	s.on("credentials.save", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Name     string  `json:"name"`
			Rename   string  `json:"rename"` // previous name when renaming
			Username string  `json:"username"`
			Password *string `json:"password"` // nil keeps the stored password
		}](raw)
		if err != nil {
			return nil, err
		}
		cr := store.Credential{Name: strings.TrimSpace(p.Name), Username: p.Username}
		prev := p.Rename
		if prev == "" {
			prev = cr.Name
		}
		if old, ok, gerr := s.Store.Vault.Credential(prev); ok && gerr == nil {
			cr.Password = old.Password
		} else if gerr != nil {
			return nil, gerr
		}
		if p.Password != nil {
			cr.Password = *p.Password
		}
		if err := s.Store.Vault.SetCredential(cr); err != nil {
			return nil, err
		}
		if p.Rename != "" && p.Rename != cr.Name {
			s.Store.Vault.DeleteCredential(p.Rename)
		}
		s.broadcast("credentials.changed", nil)
		return true, nil
	})
	s.on("credentials.delete", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Name string `json:"name"`
		}](raw)
		if err != nil {
			return nil, err
		}
		s.Store.Vault.DeleteCredential(p.Name)
		s.broadcast("credentials.changed", nil)
		return true, nil
	})

	// --------------------------------------------------------- host keys
	s.on("hostkeys.list", func(c *client, _ json.RawMessage) (any, error) {
		l, err := conn.NewHostKeyDB(s.Store.KnownHostsPath(), false).List()
		if l == nil {
			l = []conn.HostKeyEntry{}
		}
		return l, err
	})
	s.on("hostkeys.remove", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Hosts string `json:"hosts"`
			Type  string `json:"type"`
		}](raw)
		if err != nil {
			return nil, err
		}
		return true, conn.NewHostKeyDB(s.Store.KnownHostsPath(), false).Remove(p.Hosts, p.Type)
	})

	s.on("serial.ports", func(c *client, _ json.RawMessage) (any, error) {
		p := conn.SerialPorts()
		if p == nil {
			p = []string{}
		}
		return p, nil
	})
}

// broadcastExcept notifies every window but the originating one.
func (s *Server) broadcastExcept(origin *client, name string, payload any) {
	s.clients.Range(func(k, _ any) bool {
		if cl := k.(*client); cl != origin {
			cl.Event(name, payload)
		}
		return true
	})
}

// fsRoots lists file-system roots for the local browser (drive letters on Windows).
func fsRoots() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var out []string
	for d := 'A'; d <= 'Z'; d++ {
		p := string(d) + `:\`
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}
