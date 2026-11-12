package store

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// Vault stores secrets (passwords, passphrases, hidden logon strings)
// encrypted with XChaCha20-Poly1305.
//
// Without a master passphrase the data key is a random key kept next to the
// vault in "vault.key" (mode 0600): secrets are protected by file permissions
// and never appear in session files or backups of the session tree. With a
// master passphrase the data key is derived with Argon2id and the vault must
// be unlocked once per run.
type Vault struct {
	path string

	mu      sync.Mutex
	doc     vaultDoc
	key     []byte // nil while locked
	loadErr error
}

type vaultKDF struct {
	Salt    string `json:"salt"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memoryKiB"`
	Threads uint8  `json:"threads"`
}

type vaultDoc struct {
	Version int `json:"version"`
	// KDF is set when a master passphrase protects the vault.
	KDF *vaultKDF `json:"kdf,omitempty"`
	// Check is an encrypted known value used to verify the key.
	Check   string            `json:"check"`
	Entries map[string]string `json:"entries"`
}

const vaultCheck = "hawser-vault-v1"

// ErrLocked is returned while a passphrase-protected vault is locked.
var ErrLocked = errors.New("the credential vault is locked")

// ErrBadPassphrase is returned when unlocking fails.
var ErrBadPassphrase = errors.New("incorrect master passphrase")

// OpenVault loads (or initialises) the vault file.
func OpenVault(path string) (*Vault, error) {
	v := &Vault{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		v.doc = vaultDoc{Version: 1, Entries: map[string]string{}}
		key, err := v.machineKey(true)
		if err != nil {
			return nil, err
		}
		v.key = key
		v.doc.Check = v.seal(key, []byte(vaultCheck))
		if err := v.saveLocked(); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &v.doc); err != nil {
			return nil, err
		}
		if v.doc.Entries == nil {
			v.doc.Entries = map[string]string{}
		}
		if v.doc.KDF == nil {
			key, err := v.machineKey(false)
			if err != nil {
				// key file lost: secrets are unrecoverable, start an empty vault
				v.loadErr = err
				key, err = v.machineKey(true)
				if err != nil {
					return nil, err
				}
				v.doc.Entries = map[string]string{}
				v.doc.Check = v.seal(key, []byte(vaultCheck))
				_ = v.saveLocked()
			}
			v.key = key
		}
	}
	return v, nil
}

func (v *Vault) keyFile() string { return filepath.Join(filepath.Dir(v.path), "vault.key") }

func (v *Vault) machineKey(create bool) ([]byte, error) {
	b, err := os.ReadFile(v.keyFile())
	if err == nil {
		key, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if derr == nil && len(key) == chacha20poly1305.KeySize {
			return key, nil
		}
		err = errors.New("vault.key is corrupt")
	}
	if !create {
		return nil, err
	}
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(v.keyFile(), []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (v *Vault) seal(key, plain []byte) string {
	aead, _ := chacha20poly1305.NewX(key)
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plain, nil))
}

func (v *Vault) open(key []byte, enc string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	aead, _ := chacha20poly1305.NewX(key)
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("vault entry is truncated")
	}
	return aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
}

func (v *Vault) saveLocked() error {
	b, err := json.MarshalIndent(v.doc, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(v.path, append(b, '\n'), 0o600)
}

func deriveKey(pass string, k *vaultKDF) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil {
		return nil, err
	}
	return argon2.IDKey([]byte(pass), salt, k.Time, k.Memory, k.Threads, chacha20poly1305.KeySize), nil
}

// HasPassphrase reports whether a master passphrase protects the vault.
func (v *Vault) HasPassphrase() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.doc.KDF != nil
}

// Locked reports whether secrets are currently inaccessible.
func (v *Vault) Locked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.key == nil
}

// Count returns the number of stored secrets.
func (v *Vault) Count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.doc.Entries)
}

// Unlock derives the key from the master passphrase.
func (v *Vault) Unlock(pass string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.doc.KDF == nil {
		return nil
	}
	key, err := deriveKey(pass, v.doc.KDF)
	if err != nil {
		return err
	}
	plain, err := v.open(key, v.doc.Check)
	if err != nil || subtle.ConstantTimeCompare(plain, []byte(vaultCheck)) != 1 {
		return ErrBadPassphrase
	}
	v.key = key
	return nil
}

// Lock forgets the key of a passphrase-protected vault.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.doc.KDF != nil {
		v.key = nil
	}
}

// SetPassphrase sets, changes or (with an empty newPass) removes the master
// passphrase, re-encrypting every entry.
func (v *Vault) SetPassphrase(newPass string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return ErrLocked
	}
	plain := make(map[string][]byte, len(v.doc.Entries))
	for k, enc := range v.doc.Entries {
		p, err := v.open(v.key, enc)
		if err != nil {
			return err
		}
		plain[k] = p
	}
	var key []byte
	var kdf *vaultKDF
	if newPass == "" {
		var err error
		key, err = v.machineKey(true)
		if err != nil {
			return err
		}
	} else {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		kdf = &vaultKDF{Salt: base64.StdEncoding.EncodeToString(salt), Time: 3, Memory: 64 * 1024, Threads: 4}
		var err error
		key, err = deriveKey(newPass, kdf)
		if err != nil {
			return err
		}
	}
	entries := make(map[string]string, len(plain))
	for k, p := range plain {
		entries[k] = v.seal(key, p)
	}
	v.doc.KDF = kdf
	v.doc.Entries = entries
	v.doc.Check = v.seal(key, []byte(vaultCheck))
	v.key = key
	if err := v.saveLocked(); err != nil {
		return err
	}
	if kdf != nil {
		_ = os.Remove(v.keyFile())
	}
	return nil
}

// Get returns a secret; ok is false when it does not exist.
func (v *Vault) Get(name string) (value string, ok bool, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	enc, exists := v.doc.Entries[name]
	if !exists {
		return "", false, nil
	}
	if v.key == nil {
		return "", true, ErrLocked
	}
	p, err := v.open(v.key, enc)
	if err != nil {
		return "", true, err
	}
	return string(p), true, nil
}

// Has reports whether a secret exists (works while locked).
func (v *Vault) Has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.doc.Entries[name]
	return ok
}

// Set stores a secret.
func (v *Vault) Set(name, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key == nil {
		return ErrLocked
	}
	v.doc.Entries[name] = v.seal(v.key, []byte(value))
	return v.saveLocked()
}

// Delete removes a secret.
func (v *Vault) Delete(name string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.doc.Entries[name]; ok {
		delete(v.doc.Entries, name)
		_ = v.saveLocked()
	}
}

// DeletePrefix removes every secret whose name starts with prefix.
func (v *Vault) DeletePrefix(prefix string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	changed := false
	for k := range v.doc.Entries {
		if strings.HasPrefix(k, prefix) {
			delete(v.doc.Entries, k)
			changed = true
		}
	}
	if changed {
		_ = v.saveLocked()
	}
}

// RenamePrefix moves secrets from one name prefix to another.
func (v *Vault) RenamePrefix(from, to string) { v.movePrefix(from, to, true) }

// CopyPrefix duplicates secrets under a new name prefix.
func (v *Vault) CopyPrefix(from, to string) { v.movePrefix(from, to, false) }

func (v *Vault) movePrefix(from, to string, remove bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	changed := false
	for k, enc := range v.doc.Entries {
		if strings.HasPrefix(k, from) {
			v.doc.Entries[to+strings.TrimPrefix(k, from)] = enc
			if remove {
				delete(v.doc.Entries, k)
			}
			changed = true
		}
	}
	if changed {
		_ = v.saveLocked()
	}
}

// Names lists the stored secret names.
func (v *Vault) Names() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, 0, len(v.doc.Entries))
	for k := range v.doc.Entries {
		out = append(out, k)
	}
	return out
}

// Credential is a named user name / password pair shared by several sessions.
type Credential struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
}

const credentialPrefix = "credential:"

// Credential returns a shared credential by name.
func (v *Vault) Credential(name string) (Credential, bool, error) {
	raw, ok, err := v.Get(credentialPrefix + name)
	if !ok || err != nil {
		return Credential{}, ok, err
	}
	var c Credential
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Credential{}, true, err
	}
	c.Name = name
	return c, true, nil
}

// SetCredential stores a shared credential.
func (v *Vault) SetCredential(c Credential) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("a credential needs a name")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return v.Set(credentialPrefix+c.Name, string(b))
}

// DeleteCredential removes a shared credential.
func (v *Vault) DeleteCredential(name string) { v.Delete(credentialPrefix + name) }

// CredentialNames lists the stored credentials (works while locked).
func (v *Vault) CredentialNames() []string {
	var out []string
	for _, n := range v.Names() {
		if strings.HasPrefix(n, credentialPrefix) {
			out = append(out, strings.TrimPrefix(n, credentialPrefix))
		}
	}
	sort.Strings(out)
	return out
}
