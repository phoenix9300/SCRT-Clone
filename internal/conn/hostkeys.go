package conn

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/phoenix9300/SCRT-Clone/internal/keys"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// HostKeyStatus is the result of looking a host key up in the database.
type HostKeyStatus int

const (
	HostKeyMatch HostKeyStatus = iota
	HostKeyUnknown
	HostKeyChanged
	// HostKeyInvalid: the database is unreadable or lists the key as revoked
	HostKeyInvalid
)

// HostKeyDB is Hawser's SSH host key database. It is an OpenSSH-format
// known_hosts file, optionally consulted together with ~/.ssh/known_hosts
// (which is never modified).
type HostKeyDB struct {
	path    string
	openssh bool
}

var hostKeyMu sync.Mutex

// NewHostKeyDB opens the database at path.
func NewHostKeyDB(path string, includeOpenSSH bool) *HostKeyDB {
	return &HostKeyDB{path: path, openssh: includeOpenSSH}
}

func (db *HostKeyDB) files() []string {
	if _, err := os.Stat(db.path); err != nil {
		_ = os.WriteFile(db.path, nil, 0o600)
	}
	out := []string{db.path}
	if db.openssh {
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, ".ssh", "known_hosts")
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

func (db *HostKeyDB) callback() (ssh.HostKeyCallback, error) {
	files := db.files()
	cb, err := knownhosts.New(files...)
	if err != nil && len(files) > 1 {
		// a malformed ~/.ssh/known_hosts must not break our own database
		cb, err = knownhosts.New(files[0])
	}
	return cb, err
}

var dummyAddr = &net.TCPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 22}

// Check looks key up for hostname ("host:port").
func (db *HostKeyDB) Check(hostname string, remote net.Addr, key ssh.PublicKey) (HostKeyStatus, []ssh.PublicKey) {
	status, old, _ := db.CheckErr(hostname, remote, key)
	return status, old
}

// CheckErr is Check plus the reason when the database itself cannot vouch
// for the answer: it is unreadable, or the key is revoked. In those cases the
// status is HostKeyInvalid and the key must not be accepted automatically.
func (db *HostKeyDB) CheckErr(hostname string, remote net.Addr, key ssh.PublicKey) (HostKeyStatus, []ssh.PublicKey, error) {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	cb, err := db.callback()
	if err != nil {
		return HostKeyInvalid, nil, fmt.Errorf("the host key database cannot be read: %w", err)
	}
	if remote == nil {
		remote = dummyAddr
	}
	err = cb(hostname, remote, key)
	if err == nil {
		return HostKeyMatch, nil, nil
	}
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) {
		return HostKeyInvalid, nil, err
	}
	if len(ke.Want) == 0 {
		return HostKeyUnknown, nil, nil
	}
	var old []ssh.PublicKey
	status := HostKeyUnknown
	for _, w := range ke.Want {
		if w.Key.Type() == key.Type() {
			status = HostKeyChanged
			old = append([]ssh.PublicKey{w.Key}, old...)
		} else {
			old = append(old, w.Key)
		}
	}
	return status, old, nil
}

// KnownTypes returns the key types stored for hostname.
func (db *HostKeyDB) KnownTypes(hostname string) []string {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	cb, err := db.callback()
	if err != nil {
		return nil
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	probe, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil
	}
	var ke *knownhosts.KeyError
	if err := cb(hostname, dummyAddr, probe); errors.As(err, &ke) {
		var out []string
		for _, w := range ke.Want {
			out = append(out, w.Key.Type())
		}
		return out
	}
	return nil
}

// Add records key for hostname.
func (db *HostKeyDB) Add(hostname string, key ssh.PublicKey) error {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	f, err := os.OpenFile(db.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// make sure we start on a fresh line
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		if b, err := os.ReadFile(db.path); err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
			_, _ = f.WriteString("\n")
		}
	}
	_, err = f.WriteString(line + "\n")
	return err
}

// HostKeyEntry is one line of the database.
type HostKeyEntry struct {
	Hosts     string `json:"hosts"`
	Type      string `json:"type"`
	Algorithm string `json:"algorithm"`
	Bits      int    `json:"bits"`
	SHA256    string `json:"sha256"`
	MD5       string `json:"md5"`
	Hashed    bool   `json:"hashed"`
}

// List returns the entries of Hawser's own database file.
func (db *HostKeyDB) List() ([]HostKeyEntry, error) {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	b, err := os.ReadFile(db.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []HostKeyEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		hosts, key, ok := parseKnownHostsLine(sc.Text())
		if !ok {
			continue
		}
		name, bits := keys.KeyTypeDisplay(key)
		out = append(out, HostKeyEntry{
			Hosts: hosts, Type: key.Type(), Algorithm: name, Bits: bits,
			SHA256: keys.FingerprintSHA256(key), MD5: keys.FingerprintMD5(key),
			Hashed: strings.HasPrefix(hosts, "|1|"),
		})
	}
	return out, nil
}

func parseKnownHostsLine(line string) (hosts string, key ssh.PublicKey, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil, false
	}
	fields := strings.Fields(line)
	if strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	if len(fields) < 3 {
		return "", nil, false
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(fields[1] + " " + fields[2]))
	if err != nil {
		return "", nil, false
	}
	return fields[0], k, true
}

// Remove deletes the entries for hostname (all key types when keyType is "").
// hostname may be "host:port" or the hosts field exactly as listed.
func (db *HostKeyDB) Remove(hostname, keyType string) error {
	hostKeyMu.Lock()
	defer hostKeyMu.Unlock()
	b, err := os.ReadFile(db.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	norm := knownhosts.Normalize(hostname)
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		hosts, key, ok := parseKnownHostsLine(line)
		if ok && (keyType == "" || key.Type() == keyType) {
			match := hosts == hostname
			var keep []string
			for _, h := range strings.Split(hosts, ",") {
				if h == norm || h == hostname {
					match = true
				} else {
					keep = append(keep, h)
				}
			}
			if match {
				if len(keep) > 0 && hosts != hostname {
					out = append(out, knownhosts.Line(keep, key))
				}
				continue
			}
		}
		out = append(out, line)
	}
	return store.WriteFileAtomic(db.path, []byte(strings.TrimRight(strings.Join(out, "\n"), "\n")+"\n"), 0o600)
}
