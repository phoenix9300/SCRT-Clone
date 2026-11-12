package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store is the on-disk configuration rooted at Dir.
//
//	Dir/global.json            global options (free-form JSON owned by the UI)
//	Dir/default.json           overrides applied to every session ("Default session")
//	Dir/sessions/**/NAME.json  one sparse file per session; folders are directories
//	Dir/known_hosts            SSH host key database (OpenSSH format)
//	Dir/vault.json             encrypted credentials
type Store struct {
	Dir   string
	Vault *Vault

	mu sync.Mutex
}

// DefaultDir returns the per-user configuration directory.
func DefaultDir() string {
	if d := os.Getenv("HAWSER_CONFIG"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hawser")
}

// Open prepares the configuration directory.
func Open(dir string) (*Store, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	for _, d := range []string{dir, filepath.Join(dir, "sessions"), filepath.Join(dir, "logs"), filepath.Join(dir, "scripts"), filepath.Join(dir, "keys")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	v, err := OpenVault(filepath.Join(dir, "vault.json"))
	if err != nil {
		return nil, err
	}
	return &Store{Dir: dir, Vault: v}, nil
}

func (s *Store) SessionsDir() string    { return filepath.Join(s.Dir, "sessions") }
func (s *Store) KnownHostsPath() string { return filepath.Join(s.Dir, "known_hosts") }
func (s *Store) LogsDir() string        { return filepath.Join(s.Dir, "logs") }
func (s *Store) ScriptsDir() string     { return filepath.Join(s.Dir, "scripts") }
func (s *Store) KeysDir() string        { return filepath.Join(s.Dir, "keys") }

// ---------------------------------------------------------------- paths

// ErrBadPath is returned for session paths that are empty or try to escape the tree.
var ErrBadPath = errors.New("invalid session path")

// CleanPath normalises a session path ("Folder/Sub/Name").
func CleanPath(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, seg := range parts {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if seg == "." || seg == ".." || strings.ContainsAny(seg, "<>:\"|?*\x00") || strings.HasPrefix(seg, ".") {
			return "", ErrBadPath
		}
		for _, r := range seg {
			if r < 0x20 {
				return "", ErrBadPath
			}
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return "", ErrBadPath
	}
	return strings.Join(out, "/"), nil
}

func (s *Store) sessionFile(path string) (string, error) {
	p, err := CleanPath(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.SessionsDir(), filepath.FromSlash(p)+".json"), nil
}

func (s *Store) folderDir(path string) (string, error) {
	if strings.Trim(path, "/ ") == "" {
		return s.SessionsDir(), nil
	}
	p, err := CleanPath(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.SessionsDir(), filepath.FromSlash(p)), nil
}

// ---------------------------------------------------------------- tree

// Summary is the part of a session shown in the session manager.
type Summary struct {
	Protocol    string `json:"protocol"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Description string `json:"description"`
	TabColor    string `json:"tabColor,omitempty"`
}

// Node is a folder or session in the session tree.
type Node struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Folder   bool     `json:"folder"`
	Children []*Node  `json:"children,omitempty"`
	Summary  *Summary `json:"summary,omitempty"`
}

// Tree returns the whole session tree, folders first, sorted by name.
func (s *Store) Tree() (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	def, _ := s.defaultsMapLocked()
	root := &Node{Name: "Sessions", Path: "", Folder: true}
	err := s.walk(s.SessionsDir(), "", root, def)
	return root, err
}

func (s *Store) walk(dir, rel string, node *Node, def map[string]any) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		if e.IsDir() {
			child := &Node{Name: name, Path: childRel, Folder: true}
			if err := s.walk(filepath.Join(dir, name), childRel, child, def); err != nil {
				return err
			}
			node.Children = append(node.Children, child)
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		base := strings.TrimSuffix(name, ".json")
		p := strings.TrimSuffix(childRel, ".json")
		child := &Node{Name: base, Path: p}
		if ov, err := readJSONMap(filepath.Join(dir, name)); err == nil {
			eff := mergedCopy(def, ov)
			child.Summary = summarise(eff)
		} else {
			child.Summary = &Summary{Description: "unreadable: " + err.Error()}
		}
		node.Children = append(node.Children, child)
	}
	sort.SliceStable(node.Children, func(i, j int) bool {
		a, b := node.Children[i], node.Children[j]
		if a.Folder != b.Folder {
			return a.Folder
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return nil
}

func summarise(m map[string]any) *Summary {
	sum := &Summary{}
	sum.Protocol, _ = m["protocol"].(string)
	sum.Host, _ = m["host"].(string)
	if f, ok := m["port"].(float64); ok {
		sum.Port = int(f)
	}
	sum.Username, _ = m["username"].(string)
	sum.Description, _ = m["description"].(string)
	if ap, ok := m["appearance"].(map[string]any); ok {
		sum.TabColor, _ = ap["tabColor"].(string)
	}
	switch sum.Protocol {
	case "serial":
		if se, ok := m["serial"].(map[string]any); ok {
			sum.Host, _ = se["port"].(string)
		}
	case "local":
		if lo, ok := m["local"].(map[string]any); ok {
			sum.Host, _ = lo["shell"].(string)
		}
	}
	return sum
}

// ---------------------------------------------------------------- sessions

func factoryMap() map[string]any {
	b, _ := json.Marshal(FactoryDefaults())
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (s *Store) defaultsMapLocked() (map[string]any, error) {
	m := factoryMap()
	ov, err := readJSONMap(filepath.Join(s.Dir, "default.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return m, err
	}
	deepMerge(m, ov)
	return m, nil
}

// Defaults returns the effective default session (factory defaults overlaid
// with the user's "Default session") as a JSON object.
func (s *Store) Defaults() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaultsMapLocked()
}

// DefaultOverrides returns the user's changes to the default session.
func (s *Store) DefaultOverrides() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ov, err := readJSONMap(filepath.Join(s.Dir, "default.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	return ov, err
}

// SaveDefault replaces the default-session overrides.
func (s *Store) SaveDefault(ov map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(filepath.Join(s.Dir, "default.json"), ov)
}

// Overrides returns the sparse settings stored for a session.
func (s *Store) Overrides(path string) (map[string]any, error) {
	f, err := s.sessionFile(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return readJSONMap(f)
}

// Exists reports whether a session with that path exists.
func (s *Store) Exists(path string) bool {
	f, err := s.sessionFile(path)
	if err != nil {
		return false
	}
	_, err = os.Stat(f)
	return err == nil
}

// Resolve overlays overrides on the default session and decodes the result.
func (s *Store) Resolve(overrides map[string]any) (SessionOptions, error) {
	s.mu.Lock()
	def, err := s.defaultsMapLocked()
	s.mu.Unlock()
	if err != nil {
		return SessionOptions{}, err
	}
	deepMerge(def, overrides)
	return decodeOptions(def)
}

// Session returns the effective options of a stored session.
func (s *Store) Session(path string) (SessionOptions, error) {
	ov, err := s.Overrides(path)
	if err != nil {
		return SessionOptions{}, err
	}
	return s.Resolve(ov)
}

func decodeOptions(m map[string]any) (SessionOptions, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return SessionOptions{}, err
	}
	o := FactoryDefaults()
	if err := json.Unmarshal(b, &o); err != nil {
		return SessionOptions{}, fmt.Errorf("invalid session options: %w", err)
	}
	if o.Port == 0 {
		o.Port = DefaultPort(o.Protocol)
	}
	return o, nil
}

// Save writes a session's overrides, creating folders as needed.
func (s *Store) Save(path string, overrides map[string]any) error {
	f, err := s.sessionFile(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	return writeJSON(f, overrides)
}

// Delete removes a session and its stored secrets.
func (s *Store) Delete(path string) error {
	f, err := s.sessionFile(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(f); err != nil {
		return err
	}
	p, _ := CleanPath(path)
	s.Vault.DeletePrefix("session:" + p + ":")
	return nil
}

// Mkdir creates a session folder.
func (s *Store) Mkdir(path string) error {
	d, err := s.folderDir(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(d, 0o700)
}

// Rmdir removes a folder and everything in it.
func (s *Store) Rmdir(path string) error {
	p, err := CleanPath(path)
	if err != nil {
		return err
	}
	d := filepath.Join(s.SessionsDir(), filepath.FromSlash(p))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(d); err != nil {
		return err
	}
	s.Vault.DeletePrefix("session:" + p + "/")
	return nil
}

// Rename moves a session or a folder. Stored secrets follow the session.
func (s *Store) Rename(from, to string, folder bool) error {
	fp, err := CleanPath(from)
	if err != nil {
		return err
	}
	tp, err := CleanPath(to)
	if err != nil {
		return err
	}
	ext := ".json"
	if folder {
		ext = ""
	}
	src := filepath.Join(s.SessionsDir(), filepath.FromSlash(fp)+ext)
	dst := filepath.Join(s.SessionsDir(), filepath.FromSlash(tp)+ext)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%q already exists", tp)
	}
	if folder && (tp == fp || strings.HasPrefix(tp, fp+"/")) {
		return errors.New("cannot move a folder into itself")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	if folder {
		s.Vault.RenamePrefix("session:"+fp+"/", "session:"+tp+"/")
	} else {
		s.Vault.RenamePrefix("session:"+fp+":", "session:"+tp+":")
	}
	return nil
}

// Copy duplicates a session (including stored secrets).
func (s *Store) Copy(from, to string) error {
	ov, err := s.Overrides(from)
	if err != nil {
		return err
	}
	if s.Exists(to) {
		return fmt.Errorf("%q already exists", to)
	}
	if err := s.Save(to, ov); err != nil {
		return err
	}
	fp, _ := CleanPath(from)
	tp, _ := CleanPath(to)
	s.Vault.CopyPrefix("session:"+fp+":", "session:"+tp+":")
	return nil
}

// SessionPaths lists every session path in the tree.
func (s *Store) SessionPaths() ([]string, error) {
	root, err := s.Tree()
	if err != nil {
		return nil, err
	}
	var out []string
	var rec func(n *Node)
	rec = func(n *Node) {
		for _, c := range n.Children {
			if c.Folder {
				rec(c)
			} else {
				out = append(out, c.Path)
			}
		}
	}
	rec(root)
	return out, nil
}

// SecretKey is the vault key for a per-session secret ("password",
// "passphrase", "proxy", "logon:<n>").
func SecretKey(sessionPath, kind string) string {
	p, err := CleanPath(sessionPath)
	if err != nil {
		p = sessionPath
	}
	return "session:" + p + ":" + kind
}

// ---------------------------------------------------------------- global

// Global returns the global options document.
func (s *Store) Global() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := readJSONMap(filepath.Join(s.Dir, "global.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	return m, err
}

// SetGlobal merges patch into the global options (null values delete keys).
func (s *Store) SetGlobal(patch map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := filepath.Join(s.Dir, "global.json")
	m, err := readJSONMap(f)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	for k, v := range patch {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return m, writeJSON(f, m)
}

// GlobalString reads a string value from the global options.
func (s *Store) GlobalString(key, def string) string {
	m, err := s.Global()
	if err != nil {
		return def
	}
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------- helpers

func readJSONMap(file string) (map[string]any, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if len(strings.TrimSpace(string(b))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	return m, nil
}

func writeJSON(file string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(file, append(b, '\n'), 0o600)
}

// WriteFileAtomic writes data to a temporary file and renames it into place.
func WriteFileAtomic(file string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		return errors.Join(werr, cerr)
	}
	_ = os.Chmod(name, perm)
	if err := os.Rename(name, file); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// deepMerge overlays src onto dst. Objects merge recursively; everything else
// (including arrays) is replaced.
func deepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				deepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func mergedCopy(base, over map[string]any) map[string]any {
	out := deepCopy(base)
	deepMerge(out, over)
	return out
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if mm, ok := v.(map[string]any); ok {
			out[k] = deepCopy(mm)
		} else {
			out[k] = v
		}
	}
	return out
}
