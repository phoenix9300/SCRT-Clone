package conn

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/phoenix9300/SCRT-Clone/internal/keys"
	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// ClientVersion is the SSH identification string Hawser announces.
const ClientVersion = "SSH-2.0-Hawser_0.1"

// sshConn is one authenticated SSH transport connection. Several channels
// (terminal shells, cloned sessions, SFTP) can share it.
type sshConn struct {
	client *ssh.Client
	parent *sshConn // jump host this connection is tunnelled through
	mgr    *Manager

	mu        sync.Mutex
	refs      int
	closed    bool
	closers   []io.Closer
	forwards  []*ForwardStatus
	info      map[string]string
	fwdAgent  agent.Agent
	agentOnce sync.Once
	x11Once   sync.Once
	x11       *x11State
	stopKeep  chan struct{}
}

func (sc *sshConn) retain() {
	sc.mu.Lock()
	sc.refs++
	sc.mu.Unlock()
}

// release drops one reference and tears the connection down when unused.
func (sc *sshConn) release() {
	sc.mu.Lock()
	sc.refs--
	if sc.refs > 0 || sc.closed {
		sc.mu.Unlock()
		return
	}
	sc.closed = true
	closers := sc.closers
	sc.closers = nil
	if sc.stopKeep != nil {
		close(sc.stopKeep)
	}
	sc.mu.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
	_ = sc.client.Close()
	if sc.parent != nil {
		sc.parent.release()
	}
}

func (sc *sshConn) addCloser(c io.Closer) {
	sc.mu.Lock()
	if sc.closed {
		sc.mu.Unlock()
		_ = c.Close()
		return
	}
	sc.closers = append(sc.closers, c)
	sc.mu.Unlock()
}

// Info returns facts about the connection for the UI.
func (sc *sshConn) Info() map[string]string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make(map[string]string, len(sc.info))
	for k, v := range sc.info {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------- connect

func dialSSH(ctx context.Context, req *DialRequest) (Transport, error) {
	sc, err := sshConnect(ctx, req)
	if err != nil {
		return nil, err
	}
	t, err := sc.openTerminal(req)
	if err != nil {
		sc.release()
		return nil, err
	}
	return t, nil
}

// sshConnect establishes and authenticates an SSH connection (refs = 1).
func sshConnect(ctx context.Context, req *DialRequest) (*sshConn, error) {
	o := &req.Opts
	if strings.TrimSpace(o.Host) == "" {
		return nil, errors.New("no hostname specified")
	}
	if o.Port == 0 {
		o.Port = 22
	}
	if o.Username == "" {
		resp, err := req.Prompter.Prompt(ctx, PromptRequest{
			Kind: "username", Title: "Enter username",
			Message: "Username for " + o.Host,
			Fields:  []PromptField{{Label: "Username", Default: currentUser()}},
		})
		if err != nil {
			return nil, err
		}
		if !resp.OK || len(resp.Values) == 0 || resp.Values[0] == "" {
			return nil, ErrCancelled
		}
		o.Username = resp.Values[0]
	}
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	timeout := time.Duration(o.SSH.ConnectTimeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	netConn, parent, err := dialThrough(ctx, req, addr, timeout)
	if err != nil {
		return nil, err
	}
	var ab *authBuilder
	fail := func(err error) (*sshConn, error) {
		if ab != nil && ab.sysAgentCls != nil {
			_ = ab.sysAgentCls.Close()
		}
		_ = netConn.Close()
		if parent != nil {
			parent.release()
		}
		return nil, err
	}

	ab = newAuthBuilder(ctx, req)
	cfg := &ssh.ClientConfig{
		User:            o.Username,
		Auth:            ab.methods(),
		HostKeyCallback: ab.hostKeyCallback,
		ClientVersion:   ClientVersion,
		Timeout:         timeout,
	}
	if o.SSH.ShowBanner {
		cfg.BannerCallback = func(msg string) error {
			req.Prompter.Notice(msg)
			return nil
		}
	}
	applyAlgorithms(cfg, o.SSH, ab.knownKeyTypes(addr), req.Prompter)

	// abort the handshake when the caller gives up
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = netConn.Close()
		case <-done:
		}
	}()
	_ = netConn.SetDeadline(time.Now().Add(timeout + 5*time.Minute)) // prompts may take a while
	c, chans, reqs, err := ssh.NewClientConn(netConn, addr, cfg)
	close(done)
	if err != nil {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if ab.cancelled {
			return fail(ErrCancelled)
		}
		derr := describeSSHError(err, o)
		if ab.hostKeyRefused || strings.Contains(err.Error(), "unable to authenticate") {
			derr = &noRetryError{derr}
		}
		return fail(derr)
	}
	_ = netConn.SetDeadline(time.Time{})
	ab.commit()

	sc := &sshConn{client: ssh.NewClient(c, chans, reqs), parent: parent, mgr: req.Manager, refs: 1, info: map[string]string{}}
	sc.info["remoteAddress"] = c.RemoteAddr().String()
	sc.info["localAddress"] = c.LocalAddr().String()
	sc.info["serverVersion"] = string(c.ServerVersion())
	sc.info["clientVersion"] = string(c.ClientVersion())
	sc.info["username"] = o.Username
	if am, ok := c.(ssh.AlgorithmsConnMetadata); ok {
		a := am.Algorithms()
		sc.info["kex"] = a.KeyExchange
		sc.info["hostKeyAlgorithm"] = a.HostKey
		sc.info["cipherIn"] = a.Read.Cipher
		sc.info["cipherOut"] = a.Write.Cipher
		sc.info["macIn"] = a.Read.MAC
		sc.info["macOut"] = a.Write.MAC
	}
	if ab.hostKey != nil {
		sc.info["hostKeyFingerprint"] = keys.FingerprintSHA256(ab.hostKey)
		sc.info["hostKeyType"] = ab.hostKey.Type()
	}
	if ab.authUsed != "" {
		sc.info["authMethod"] = ab.authUsed
	}
	sc.fwdAgent = ab.forwardAgent()
	if sc.fwdAgent != nil && ab.sysAgentCls != nil {
		sc.addCloser(ab.sysAgentCls) // the forwarded agent socket lives as long as the connection
	}

	if iv := o.SSH.KeepAliveInterval; iv > 0 {
		sc.stopKeep = make(chan struct{})
		go sc.keepAliveLoop(time.Duration(iv) * time.Second)
	}
	for _, f := range o.SSH.Forwards {
		if f.Enabled {
			sc.StartForward(f)
		}
	}
	return sc, nil
}

func (sc *sshConn) keepAliveLoop(iv time.Duration) {
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-sc.stopKeep:
			return
		case <-t.C:
			errc := make(chan error, 1)
			go func() {
				_, _, err := sc.client.SendRequest("keepalive@openssh.com", true, nil)
				errc <- err
			}()
			select {
			case err := <-errc:
				if err != nil {
					_ = sc.client.Close()
					return
				}
			case <-time.After(iv + 15*time.Second):
				_ = sc.client.Close()
				return
			case <-sc.stopKeep:
				return
			}
		}
	}
}

func describeSSHError(err error, o *store.SessionOptions) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("authentication failed for %s@%s (%s)", o.Username, o.Host, strings.TrimPrefix(msg, "ssh: handshake failed: "))
	case strings.Contains(msg, "no common algorithm"):
		return fmt.Errorf("no mutually supported algorithm: %s. Enable additional algorithms in Session Options → SSH2 → Advanced", strings.TrimPrefix(msg, "ssh: handshake failed: "))
	}
	return errors.New(strings.TrimPrefix(msg, "ssh: handshake failed: "))
}

// applyAlgorithms copies the user's algorithm preferences into cfg, dropping
// names the library does not implement.
func applyAlgorithms(cfg *ssh.ClientConfig, o store.SSHOptions, knownTypes []string, p Prompter) {
	sup := ssh.SupportedAlgorithms()
	ins := ssh.InsecureAlgorithms()
	filter := func(want []string, lists ...[]string) []string {
		var out []string
		for _, w := range want {
			found := false
			for _, l := range lists {
				for _, s := range l {
					if s == w {
						found = true
					}
				}
			}
			if found {
				out = append(out, w)
			} else if p != nil {
				p.Notice("Ignoring unsupported algorithm: " + w + "\r\n")
			}
		}
		return out
	}
	if len(o.KexAlgorithms) > 0 {
		cfg.KeyExchanges = filter(o.KexAlgorithms, sup.KeyExchanges, ins.KeyExchanges)
	}
	if len(o.Ciphers) > 0 {
		cfg.Ciphers = filter(o.Ciphers, sup.Ciphers, ins.Ciphers)
	}
	if len(o.MACs) > 0 {
		cfg.MACs = filter(o.MACs, sup.MACs, ins.MACs)
	}
	if len(o.HostKeyAlgorithms) > 0 {
		cfg.HostKeyAlgorithms = filter(o.HostKeyAlgorithms, sup.HostKeys, ins.HostKeys)
		return
	}
	// Prefer the key types we already know for this host, like OpenSSH does,
	// so a server offering several host keys does not trigger a false alarm.
	if len(knownTypes) > 0 {
		var pref []string
		for _, t := range knownTypes {
			if t == ssh.KeyAlgoRSA {
				pref = append(pref, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
			} else {
				pref = append(pref, t)
			}
		}
		seen := map[string]bool{}
		var out []string
		for _, a := range append(pref, sup.HostKeys...) {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
		cfg.HostKeyAlgorithms = out
	}
}

// SupportedSSHAlgorithms lists algorithm names for the options dialog.
func SupportedSSHAlgorithms() map[string]any {
	sup := ssh.SupportedAlgorithms()
	ins := ssh.InsecureAlgorithms()
	return map[string]any{
		"kex":              sup.KeyExchanges,
		"ciphers":          sup.Ciphers,
		"macs":             sup.MACs,
		"hostKeys":         sup.HostKeys,
		"insecureKex":      ins.KeyExchanges,
		"insecureCiphers":  ins.Ciphers,
		"insecureMacs":     ins.MACs,
		"insecureHostKeys": ins.HostKeys,
	}
}

// ---------------------------------------------------------- authentication

type authBuilder struct {
	ctx context.Context
	req *DialRequest

	mu        sync.Mutex
	savedPw   string
	havePw    bool
	pwTried   bool
	pwToSave  string
	ppToSave  map[string]string
	cancelled bool
	// hostKeyRefused is set when verification failed (policy or database error)
	hostKeyRefused bool
	authUsed       string
	hostKey        ssh.PublicKey
	sysAgent       agent.ExtendedAgent
	sysAgentCls    io.Closer
}

func newAuthBuilder(ctx context.Context, req *DialRequest) *authBuilder {
	ab := &authBuilder{ctx: ctx, req: req, ppToSave: map[string]string{}}
	if req.SessionPath != "" && req.Store != nil {
		if pw, ok, err := req.Store.Vault.Get(store.SecretKey(req.SessionPath, "password")); ok && err == nil {
			ab.savedPw, ab.havePw = pw, true
		}
	}
	if pw, ok := req.transientPassword(); ok {
		ab.savedPw, ab.havePw = pw, true
	}
	if req.Opts.SSH.UseAgent || req.Opts.SSH.AgentForwarding {
		if a, c, err := systemAgent(); err == nil {
			ab.sysAgent, ab.sysAgentCls = a, c
		}
	}
	return ab
}

// forwardAgent returns the agent to offer for agent forwarding, if any.
func (ab *authBuilder) forwardAgent() agent.Agent {
	if !ab.req.Opts.SSH.AgentForwarding {
		if ab.sysAgentCls != nil {
			_ = ab.sysAgentCls.Close()
		}
		return nil
	}
	if ab.sysAgent != nil {
		return ab.sysAgent
	}
	if ab.req.Manager != nil {
		return ab.req.Manager.Keyring
	}
	return nil
}

// commit persists secrets the user asked to remember, after a successful login.
func (ab *authBuilder) commit() {
	if ab.req.Store == nil || ab.req.SessionPath == "" {
		return
	}
	v := ab.req.Store.Vault
	if ab.pwToSave != "" {
		_ = v.Set(store.SecretKey(ab.req.SessionPath, "password"), ab.pwToSave)
	}
	for file, pp := range ab.ppToSave {
		_ = v.Set("passphrase:"+file, pp)
	}
}

func (ab *authBuilder) methods() []ssh.AuthMethod {
	var out []ssh.AuthMethod
	order := ab.req.Opts.SSH.Auth
	if len(order) == 0 {
		order = []string{"publickey", "keyboard-interactive", "password"}
	}
	seen := map[string]bool{}
	for _, m := range order {
		if seen[m] {
			continue
		}
		seen[m] = true
		switch m {
		case "publickey", "agent":
			if seen["publickey-added"] {
				continue
			}
			seen["publickey-added"] = true
			out = append(out, ssh.PublicKeysCallback(ab.signers))
		case "keyboard-interactive":
			out = append(out, ssh.RetryableAuthMethod(ssh.KeyboardInteractive(ab.keyboardInteractive), 3))
		case "password":
			out = append(out, ssh.RetryableAuthMethod(ssh.PasswordCallback(ab.password), 3))
		}
	}
	return out
}

func (ab *authBuilder) target() string {
	return ab.req.Opts.Username + "@" + ab.req.Opts.Host
}

func (ab *authBuilder) password() (string, error) {
	ab.mu.Lock()
	if ab.havePw && !ab.pwTried {
		ab.pwTried = true
		pw := ab.savedPw
		ab.authUsed = "password"
		ab.mu.Unlock()
		return pw, nil
	}
	ab.mu.Unlock()
	resp, err := ab.req.Prompter.Prompt(ab.ctx, PromptRequest{
		Kind: "password", Title: "Enter password",
		Message:   "Password for " + ab.target(),
		Fields:    []PromptField{{Label: "Password", Secret: true}},
		AllowSave: ab.req.SessionPath != "",
	})
	if err != nil || !resp.OK || len(resp.Values) == 0 {
		ab.cancelled = true
		if err == nil {
			err = ErrCancelled
		}
		return "", err
	}
	ab.mu.Lock()
	ab.authUsed = "password"
	if resp.Save {
		ab.pwToSave = resp.Values[0]
	} else {
		ab.pwToSave = ""
	}
	ab.mu.Unlock()
	return resp.Values[0], nil
}

func (ab *authBuilder) keyboardInteractive(name, instruction string, questions []string, echos []bool) ([]string, error) {
	if len(questions) == 0 {
		if strings.TrimSpace(instruction) != "" {
			ab.req.Prompter.Notice(instruction + "\r\n")
		}
		return nil, nil
	}
	// A lone hidden "Password:" question is plain password auth in disguise.
	if len(questions) == 1 && !echos[0] && strings.Contains(strings.ToLower(questions[0]), "password") {
		ab.mu.Lock()
		if ab.havePw && !ab.pwTried {
			ab.pwTried = true
			pw := ab.savedPw
			ab.authUsed = "keyboard-interactive"
			ab.mu.Unlock()
			return []string{pw}, nil
		}
		ab.mu.Unlock()
	}
	fields := make([]PromptField, len(questions))
	for i, q := range questions {
		fields[i] = PromptField{Label: strings.TrimSpace(q), Secret: !echos[i]}
	}
	title := strings.TrimSpace(name)
	if title == "" {
		title = "Authentication"
	}
	msg := strings.TrimSpace(instruction)
	if msg == "" {
		msg = "Keyboard-interactive authentication for " + ab.target()
	}
	single := len(questions) == 1 && !echos[0]
	resp, err := ab.req.Prompter.Prompt(ab.ctx, PromptRequest{
		Kind: "keyboard-interactive", Title: title, Message: msg, Fields: fields,
		AllowSave: single && ab.req.SessionPath != "",
	})
	if err != nil || !resp.OK {
		ab.cancelled = true
		if err == nil {
			err = ErrCancelled
		}
		return nil, err
	}
	answers := make([]string, len(questions))
	copy(answers, resp.Values)
	ab.mu.Lock()
	ab.authUsed = "keyboard-interactive"
	if single && resp.Save {
		ab.pwToSave = answers[0]
	}
	ab.mu.Unlock()
	return answers, nil
}

// signers gathers public-key identities: agent keys first (no prompts), then
// identity files, decrypted lazily only when the server accepts the key.
func (ab *authBuilder) signers() ([]ssh.Signer, error) {
	var out []ssh.Signer
	seen := map[string]bool{}
	add := func(s ssh.Signer) {
		fp := string(s.PublicKey().Marshal())
		if !seen[fp] {
			seen[fp] = true
			out = append(out, s)
		}
	}
	o := ab.req.Opts.SSH
	if o.UseAgent {
		if ab.req.Manager != nil {
			if ss, err := ab.req.Manager.Keyring.Signers(); err == nil {
				for _, s := range ss {
					add(&trackedSigner{Signer: s, ab: ab, label: "publickey (Hawser agent)"})
				}
			}
		}
		if ab.sysAgent != nil {
			if ss, err := ab.sysAgent.Signers(); err == nil {
				for _, s := range ss {
					add(&trackedSigner{Signer: s, ab: ab, label: "publickey (agent)"})
				}
			}
		}
	}
	files := append([]string(nil), o.IdentityFiles...)
	if len(files) == 0 {
		files = defaultIdentityFiles(ab.req.Store)
	}
	for _, f := range files {
		f = expandHome(f)
		data, err := os.ReadFile(f)
		if err != nil {
			if len(o.IdentityFiles) > 0 {
				ab.req.Prompter.Notice(fmt.Sprintf("Cannot read identity file %s: %v\r\n", f, err))
			}
			continue
		}
		pub, _, err := keys.ParsePublicKey(data)
		if err != nil {
			if side, rerr := os.ReadFile(f + ".pub"); rerr == nil {
				pub, _, err = keys.ParsePublicKey(side)
			}
		}
		ls := &lazySigner{ab: ab, path: f, data: data, pub: pub}
		if err != nil || pub == nil {
			// Public half unknown. For a key the user did not choose for this
			// session (a default ~/.ssh file) asking for its passphrase before
			// any server wanted it would be a nuisance: use it only if it is
			// not encrypted.
			if len(o.IdentityFiles) == 0 {
				if _, perr := keys.ParsePrivateKey(data, nil); perr != nil {
					continue
				}
			}
			// explicitly configured: decrypt now to learn the public key
			s, lerr := ls.load()
			if lerr != nil {
				continue
			}
			ls.pub = s.PublicKey()
		}
		// offer the certificate too when "<key>-cert.pub" exists
		if certData, cerr := os.ReadFile(f + "-cert.pub"); cerr == nil {
			if cert, _, perr := keys.ParseCertificate(certData); perr == nil {
				if cs, serr := ssh.NewCertSigner(cert, ls); serr == nil {
					add(cs)
				}
			}
		}
		add(ls)
	}
	return out, nil
}

func defaultIdentityFiles(st *store.Store) []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			p := filepath.Join(home, ".ssh", n)
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
	}
	if st != nil {
		if entries, err := os.ReadDir(st.KeysDir()); err == nil {
			for _, e := range entries {
				n := e.Name()
				if e.IsDir() || strings.HasSuffix(n, ".pub") || strings.HasPrefix(n, ".") {
					continue
				}
				out = append(out, filepath.Join(st.KeysDir(), n))
			}
		}
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// trackedSigner records which identity ended up authenticating.
type trackedSigner struct {
	ssh.Signer
	ab    *authBuilder
	label string
}

func (t *trackedSigner) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	t.ab.authUsed = t.label
	return t.Signer.Sign(r, data)
}

func (t *trackedSigner) SignWithAlgorithm(r io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	t.ab.authUsed = t.label
	if as, ok := t.Signer.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(r, data, algorithm)
	}
	return t.Signer.Sign(r, data)
}

// lazySigner defers decrypting a private key until the server has accepted
// its public half, so users are only asked for passphrases that matter.
type lazySigner struct {
	ab   *authBuilder
	path string
	data []byte
	pub  ssh.PublicKey

	once   sync.Once
	signer ssh.Signer
	err    error
}

func (l *lazySigner) PublicKey() ssh.PublicKey { return l.pub }

func (l *lazySigner) load() (ssh.Signer, error) {
	l.once.Do(func() {
		k, err := keys.ParsePrivateKey(l.data, nil)
		if errors.Is(err, keys.ErrPassphraseRequired) {
			// remembered passphrase?
			if l.ab.req.Store != nil {
				if pp, ok, verr := l.ab.req.Store.Vault.Get("passphrase:" + l.path); ok && verr == nil {
					if k2, err2 := keys.ParsePrivateKey(l.data, []byte(pp)); err2 == nil {
						k, err = k2, nil
					}
				}
			}
			for attempt := 0; err != nil && attempt < 3; attempt++ {
				msg := "Passphrase for key " + l.path
				if attempt > 0 {
					msg = "Incorrect passphrase. " + msg
				}
				resp, perr := l.ab.req.Prompter.Prompt(l.ab.ctx, PromptRequest{
					Kind: "passphrase", Title: "Enter passphrase", Message: msg,
					Fields:    []PromptField{{Label: "Passphrase", Secret: true}},
					AllowSave: l.ab.req.Store != nil,
				})
				if perr != nil || !resp.OK || len(resp.Values) == 0 {
					err = ErrCancelled
					break
				}
				k, err = keys.ParsePrivateKey(l.data, []byte(resp.Values[0]))
				if err == nil && resp.Save {
					l.ab.mu.Lock()
					l.ab.ppToSave[l.path] = resp.Values[0]
					l.ab.mu.Unlock()
				}
			}
		}
		if err != nil {
			l.err = err
			return
		}
		l.signer, l.err = k.SSHSigner()
	})
	return l.signer, l.err
}

func (l *lazySigner) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	s, err := l.load()
	if err != nil {
		return nil, err
	}
	l.ab.authUsed = "publickey (" + filepath.Base(l.path) + ")"
	return s.Sign(r, data)
}

func (l *lazySigner) SignWithAlgorithm(r io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	s, err := l.load()
	if err != nil {
		return nil, err
	}
	l.ab.authUsed = "publickey (" + filepath.Base(l.path) + ")"
	if as, ok := s.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(r, data, algorithm)
	}
	return s.Sign(r, data)
}

// ------------------------------------------------------------- host keys

func (ab *authBuilder) knownKeyTypes(addr string) []string {
	if ab.req.Store == nil || ab.req.Opts.SSH.HostKeyPolicy == "off" {
		return nil
	}
	return ab.db().KnownTypes(addr)
}

func (ab *authBuilder) db() *HostKeyDB {
	return NewHostKeyDB(ab.req.Store.KnownHostsPath(), ab.req.Opts.SSH.UseOpenSSHHosts)
}

func (ab *authBuilder) hostKeyCallback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	ab.hostKey = key
	policy := ab.req.Opts.SSH.HostKeyPolicy
	if policy == "off" || ab.req.Store == nil {
		return nil
	}
	db := ab.db()
	status, old, dbErr := db.CheckErr(hostname, remote, key)
	if status == HostKeyMatch {
		return nil
	}
	if status == HostKeyInvalid {
		// never fall back to "unknown host" (which accept-new would trust)
		ab.hostKeyRefused = true
		return fmt.Errorf("host key for %s cannot be verified: %v", hostname, dbErr)
	}
	name, bits := keys.KeyTypeDisplay(key)
	info := &HostKeyInfo{
		Host: hostname, Algorithm: name, Bits: bits,
		SHA256: keys.FingerprintSHA256(key), MD5: keys.FingerprintMD5(key), RandomArt: keys.RandomArt(key),
		Changed: status == HostKeyChanged,
	}
	if len(old) > 0 {
		info.OldSHA256 = keys.FingerprintSHA256(old[0])
		info.OldAlgo, _ = keys.KeyTypeDisplay(old[0])
	}
	switch policy {
	case "strict":
		ab.hostKeyRefused = true
		if status == HostKeyChanged {
			return fmt.Errorf("HOST KEY MISMATCH for %s: expected %s, server presented %s", hostname, info.OldSHA256, info.SHA256)
		}
		return fmt.Errorf("host key for %s is not in the host key database (strict checking)", hostname)
	case "accept-new":
		if status == HostKeyUnknown {
			if err := db.Add(hostname, key); err != nil {
				return err
			}
			ab.req.Prompter.Notice(fmt.Sprintf("Permanently added host key for %s (%s %s)\r\n", hostname, name, info.SHA256))
			return nil
		}
	}
	msg := "The host key for " + hostname + " is not in the host key database."
	title := "New host key"
	if status == HostKeyChanged {
		title = "HOST KEY HAS CHANGED"
		msg = "The host key presented by " + hostname + " differs from the one stored in the host key database. " +
			"This may mean someone is intercepting the connection, or the host was reinstalled."
	}
	resp, err := ab.req.Prompter.Prompt(ab.ctx, PromptRequest{Kind: "hostkey", Title: title, Message: msg, HostKey: info})
	if err != nil {
		ab.cancelled = true
		return err
	}
	switch resp.Choice {
	case "save":
		if status == HostKeyChanged {
			_ = db.Remove(hostname, "")
		}
		return db.Add(hostname, key)
	case "once":
		return nil
	}
	ab.cancelled = true
	return errors.New("host key rejected by user")
}

// --------------------------------------------------------------- terminal

type sshTransport struct {
	sc      *sshConn
	sess    *ssh.Session
	stdin   io.WriteCloser
	out     *io.PipeReader
	closeMu sync.Once
}

func (sc *sshConn) openTerminal(req *DialRequest) (*sshTransport, error) {
	o := req.Opts
	sess, err := sc.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("cannot open session channel: %w", err)
	}
	for k, v := range o.SSH.Env {
		_ = sess.Setenv(k, v) // servers commonly reject unknown variables
	}
	if o.SSH.AgentForwarding && sc.fwdAgent != nil {
		sc.agentOnce.Do(func() { _ = agent.ForwardToAgent(sc.client, sc.fwdAgent) })
		if err := agent.RequestAgentForwarding(sess); err != nil {
			req.Prompter.Notice("Agent forwarding request was refused by the server.\r\n")
		}
	}
	if o.SSH.X11Forwarding {
		if err := sc.requestX11(sess, o.SSH.X11Display); err != nil {
			req.Prompter.Notice("X11 forwarding unavailable: " + err.Error() + "\r\n")
		}
	}
	if o.SSH.RequestPty {
		erase := uint32(127)
		if o.Terminal.Backspace == "bs" {
			erase = 8
		}
		modes := ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 38400,
			ssh.TTY_OP_OSPEED: 38400,
			3:                 erase, // VERASE
		}
		if enc := strings.ToLower(o.Terminal.Encoding); enc == "" || enc == "utf-8" || enc == "utf8" {
			modes[42] = 1 // IUTF8 (RFC 8160)
		}
		cols, rows := req.Cols, req.Rows
		if cols <= 0 {
			cols = 80
		}
		if rows <= 0 {
			rows = 24
		}
		if err := sess.RequestPty(req.TermType(), rows, cols, modes); err != nil {
			_ = sess.Close()
			return nil, fmt.Errorf("pty request failed: %w", err)
		}
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	pr, pw := io.Pipe()
	sess.Stdout = pw
	sess.Stderr = pw
	switch {
	case o.SSH.Subsystem != "":
		err = sess.RequestSubsystem(o.SSH.Subsystem)
	case strings.TrimSpace(o.SSH.RemoteCommand) != "":
		err = sess.Start(o.SSH.RemoteCommand)
	default:
		err = sess.Shell()
	}
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("cannot start shell: %w", err)
	}
	t := &sshTransport{sc: sc, sess: sess, stdin: stdin, out: pr}
	go func() {
		err := sess.Wait()
		var ee *ssh.ExitError
		switch {
		case err == nil:
			_ = pw.Close()
		case errors.As(err, &ee):
			if ee.Signal() != "" {
				_ = pw.CloseWithError(fmt.Errorf("remote process terminated by signal %s", ee.Signal()))
			} else {
				_ = pw.CloseWithError(&ExitStatusError{Code: ee.ExitStatus()})
			}
		default:
			var em *ssh.ExitMissingError
			if errors.As(err, &em) {
				_ = pw.CloseWithError(errors.New("connection closed by remote host"))
			} else {
				_ = pw.CloseWithError(err)
			}
		}
	}()
	return t, nil
}

// ExitStatusError reports a remote shell that exited with a non-zero status.
type ExitStatusError struct{ Code int }

func (e *ExitStatusError) Error() string {
	return fmt.Sprintf("remote shell exited with status %d", e.Code)
}

func (t *sshTransport) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *sshTransport) Write(p []byte) (int, error) { return t.stdin.Write(p) }

func (t *sshTransport) Resize(cols, rows int) error {
	return t.sess.WindowChange(rows, cols)
}

func (t *sshTransport) SendBreak() error {
	_, err := t.sess.SendRequest("break", false, ssh.Marshal(struct{ Ms uint32 }{500}))
	return err
}

func (t *sshTransport) KeepAlive() error {
	_, _, err := t.sc.client.SendRequest("keepalive@openssh.com", true, nil)
	return err
}

func (t *sshTransport) Info() map[string]string { return t.sc.Info() }

func (t *sshTransport) Close() error {
	t.closeMu.Do(func() {
		_ = t.sess.Close()
		_ = t.out.CloseWithError(io.EOF)
		t.sc.release()
	})
	return nil
}

// sshShared lets the manager reuse the underlying connection (clone, SFTP).
func (t *sshTransport) shared() *sshConn { return t.sc }

// ------------------------------------------------------------- forwarding

// ForwardStatus is the live state of one port forward.
type ForwardStatus struct {
	Spec      store.PortForward `json:"spec"`
	Listening bool              `json:"listening"`
	Error     string            `json:"error,omitempty"`
	Active    int32             `json:"active"`
	Total     int64             `json:"total"`
	Address   string            `json:"address,omitempty"`

	closer io.Closer
}

// Forwards returns a snapshot of the connection's port forwards.
func (sc *sshConn) Forwards() []ForwardStatus {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := make([]ForwardStatus, len(sc.forwards))
	for i, f := range sc.forwards {
		out[i] = ForwardStatus{Spec: f.Spec, Listening: f.Listening, Error: f.Error,
			Active: atomic.LoadInt32(&f.Active), Total: atomic.LoadInt64(&f.Total), Address: f.Address}
	}
	return out
}

// StopForward stops the forward with the given name.
func (sc *sshConn) StopForward(name string) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for i, f := range sc.forwards {
		if f.Spec.Name == name {
			if f.closer != nil {
				_ = f.closer.Close()
			}
			sc.forwards = append(sc.forwards[:i], sc.forwards[i+1:]...)
			return true
		}
	}
	return false
}

// StartForward starts a local, remote or dynamic port forward.
func (sc *sshConn) StartForward(spec store.PortForward) *ForwardStatus {
	st := &ForwardStatus{Spec: spec}
	if st.Spec.Name == "" {
		st.Spec.Name = fmt.Sprintf("%s:%d", spec.Type, spec.BindPort)
	}
	bind := spec.BindAddr
	if bind == "" {
		bind = "127.0.0.1"
	}
	listenAddr := net.JoinHostPort(bind, strconv.Itoa(spec.BindPort))
	dest := net.JoinHostPort(spec.DestHost, strconv.Itoa(spec.DestPort))

	var ln net.Listener
	var err error
	switch spec.Type {
	case "local", "dynamic":
		ln, err = net.Listen("tcp", listenAddr)
	case "remote":
		ln, err = sc.client.Listen("tcp", listenAddr)
	default:
		err = fmt.Errorf("unknown forward type %q", spec.Type)
	}
	sc.mu.Lock()
	sc.forwards = append(sc.forwards, st)
	sc.mu.Unlock()
	if err != nil {
		sc.mu.Lock()
		st.Error = err.Error()
		sc.mu.Unlock()
		return st
	}
	sc.mu.Lock()
	st.Listening = true
	st.Address = ln.Addr().String()
	st.closer = ln
	sc.mu.Unlock()
	sc.addCloser(ln)

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				sc.mu.Lock()
				st.Listening = false
				sc.mu.Unlock()
				return
			}
			atomic.AddInt32(&st.Active, 1)
			atomic.AddInt64(&st.Total, 1)
			go func() {
				defer atomic.AddInt32(&st.Active, -1)
				defer c.Close()
				switch spec.Type {
				case "local":
					r, err := sc.client.Dial("tcp", dest)
					if err != nil {
						return
					}
					pipeBoth(c, r)
				case "remote":
					r, err := net.DialTimeout("tcp", dest, 20*time.Second)
					if err != nil {
						return
					}
					pipeBoth(c, r)
				case "dynamic":
					serveSOCKS(c, func(addr string) (net.Conn, error) { return sc.client.Dial("tcp", addr) })
				}
			}()
		}
	}()
	return st
}

// pipeBoth copies in both directions until either side closes.
func pipeBoth(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	cp := func(dst io.WriteCloser, src io.Reader) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	// A side that half-closes after its request must still get the whole
	// response, however long it takes, so wait for both directions.
	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
}

// randomHex returns n random bytes hex-encoded.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	const hexd = "0123456789abcdef"
	out := make([]byte, 0, n*2)
	for _, c := range b {
		out = append(out, hexd[c>>4], hexd[c&15])
	}
	return string(out)
}
