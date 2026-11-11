package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/phoenix9300/SCRT-Clone/internal/keys"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

func describePublic(pub ssh.PublicKey, comment string) map[string]any {
	name, bits := keys.KeyTypeDisplay(pub)
	return map[string]any{
		"type":      pub.Type(),
		"algorithm": name,
		"bits":      bits,
		"sha256":    keys.FingerprintSHA256(pub),
		"md5":       keys.FingerprintMD5(pub),
		"randomArt": keys.RandomArt(pub),
		"openssh":   strings.TrimSpace(string(keys.MarshalAuthorizedKey(pub, comment))),
		"rfc4716":   string(keys.MarshalRFC4716(pub, comment)),
		"comment":   comment,
	}
}

func (s *Server) registerKeys() {
	// keys.generate creates a key pair and writes it to disk.
	s.on("keys.generate", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Type       string `json:"type"`
			Bits       int    `json:"bits"`
			Comment    string `json:"comment"`
			Passphrase string `json:"passphrase"`
			Format     string `json:"format"` // openssh | pem | ppk
			Path       string `json:"path"`
			Overwrite  bool   `json:"overwrite"`
		}](raw)
		if err != nil {
			return nil, err
		}
		k, err := keys.Generate(keys.Type(p.Type), p.Bits)
		if err != nil {
			return nil, err
		}
		k.Comment = p.Comment
		format := keys.Format(p.Format)
		if p.Format == "" {
			format = keys.FormatOpenSSH
		}
		data, err := k.Marshal(format, []byte(p.Passphrase))
		if err != nil {
			return nil, err
		}
		path := p.Path
		if path == "" {
			path = filepath.Join(s.Store.KeysDir(), "id_"+p.Type)
			if format == keys.FormatPPK {
				path += ".ppk"
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.Store.KeysDir(), path)
		}
		if _, err := os.Stat(path); err == nil && !p.Overwrite {
			return nil, fmt.Errorf("%s already exists", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := store.WriteFileAtomic(path, data, 0o600); err != nil {
			return nil, err
		}
		pub, err := k.PublicKey()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path+".pub", keys.MarshalAuthorizedKey(pub, p.Comment), 0o644); err != nil {
			return nil, err
		}
		out := describePublic(pub, p.Comment)
		out["path"] = path
		out["publicPath"] = path + ".pub"
		return out, nil
	})

	// keys.inspect describes a key file without decrypting it.
	s.on("keys.inspect", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Path string `json:"path"`
		}](raw)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"path": p.Path}
		if info, ierr := keys.Inspect(data); ierr == nil {
			out["format"] = info.Format
			out["keyType"] = info.Type
			out["encrypted"] = info.Encrypted
			out["comment"] = info.Comment
			out["bits"] = info.Bits
		} else {
			out["inspectError"] = ierr.Error()
		}
		if pub, comment, perr := keys.ParsePublicKey(data); perr == nil {
			out["public"] = describePublic(pub, comment)
		}
		if cert, info, cerr := keys.ParseCertificate(data); cerr == nil && cert != nil {
			out["certificate"] = info
		}
		return out, nil
	})

	// keys.convert rewrites a private key in another container format.
	s.on("keys.convert", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Path          string `json:"path"`
			Passphrase    string `json:"passphrase"`
			Format        string `json:"format"`
			NewPassphrase string `json:"newPassphrase"`
			Out           string `json:"out"`
			PPKVersion    int    `json:"ppkVersion"`
			Overwrite     bool   `json:"overwrite"`
		}](raw)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, err
		}
		k, err := keys.ParsePrivateKey(data, []byte(p.Passphrase))
		if err != nil {
			switch {
			case errors.Is(err, keys.ErrPassphraseRequired):
				return map[string]any{"needPassphrase": true}, nil
			case errors.Is(err, keys.ErrWrongPassphrase):
				return map[string]any{"needPassphrase": true, "wrong": true}, nil
			}
			return nil, err
		}
		var outData []byte
		if p.Format == "ppk" && p.PPKVersion == 2 {
			outData, err = k.MarshalPPK(2, []byte(p.NewPassphrase))
		} else {
			outData, err = k.Marshal(keys.Format(p.Format), []byte(p.NewPassphrase))
		}
		if err != nil {
			return nil, err
		}
		if p.Out == "" {
			return nil, errors.New("no output file given")
		}
		if _, err := os.Stat(p.Out); err == nil && !p.Overwrite {
			return nil, fmt.Errorf("%s already exists", p.Out)
		}
		if err := store.WriteFileAtomic(p.Out, outData, 0o600); err != nil {
			return nil, err
		}
		return map[string]any{"path": p.Out}, nil
	})

	// ---- built-in agent
	s.on("agent.list", func(c *client, _ json.RawMessage) (any, error) {
		ks, err := s.Mgr.Keyring.List()
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, k := range ks {
			pub, err := ssh.ParsePublicKey(k.Blob)
			if err != nil {
				continue
			}
			out = append(out, describePublic(pub, k.Comment))
		}
		return out, nil
	})
	s.on("agent.add", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			Path       string `json:"path"`
			Passphrase string `json:"passphrase"`
		}](raw)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, err
		}
		k, err := keys.ParsePrivateKey(data, []byte(p.Passphrase))
		if err != nil {
			switch {
			case errors.Is(err, keys.ErrPassphraseRequired):
				return map[string]any{"needPassphrase": true}, nil
			case errors.Is(err, keys.ErrWrongPassphrase):
				return map[string]any{"needPassphrase": true, "wrong": true}, nil
			}
			return nil, err
		}
		comment := k.Comment
		if comment == "" {
			comment = filepath.Base(p.Path)
		}
		if err := s.Mgr.Keyring.Add(agent.AddedKey{PrivateKey: k.Signer, Comment: comment}); err != nil {
			return nil, err
		}
		s.broadcast("agent.changed", nil)
		return map[string]any{"added": true}, nil
	})
	s.on("agent.remove", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			OpenSSH string `json:"openssh"`
			All     bool   `json:"all"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.All {
			err = s.Mgr.Keyring.RemoveAll()
		} else {
			pub, _, _, _, perr := ssh.ParseAuthorizedKey([]byte(p.OpenSSH))
			if perr != nil {
				return nil, perr
			}
			err = s.Mgr.Keyring.Remove(pub)
		}
		s.broadcast("agent.changed", nil)
		return err == nil, err
	})
}
