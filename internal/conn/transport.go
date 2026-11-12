// Package conn implements Hawser's connection engine: the transports
// (SSH2, Telnet, Rlogin, Raw, Serial, local shell), the live-connection
// manager, session logging and the expect/automation machinery.
package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// Transport is a live byte stream to a remote endpoint or a local process.
type Transport interface {
	io.ReadWriteCloser
	// Resize informs the remote side of a new terminal size.
	Resize(cols, rows int) error
}

// Breaker is implemented by transports that can send a BREAK condition.
type Breaker interface {
	SendBreak() error
}

// Informer exposes connection facts (peer, algorithms, fingerprints ...).
type Informer interface {
	Info() map[string]string
}

// KeepAliver sends a protocol-level NO-OP (used by anti-idle).
type KeepAliver interface {
	KeepAlive() error
}

// PromptField is one input of a prompt dialog.
type PromptField struct {
	Label   string `json:"label"`
	Secret  bool   `json:"secret"`
	Default string `json:"default,omitempty"`
}

// HostKeyInfo describes a host key the user must decide about.
type HostKeyInfo struct {
	Host      string `json:"host"`
	Algorithm string `json:"algorithm"`
	Bits      int    `json:"bits,omitempty"`
	SHA256    string `json:"sha256"`
	MD5       string `json:"md5"`
	RandomArt string `json:"randomArt,omitempty"`
	Changed   bool   `json:"changed"`
	OldSHA256 string `json:"oldSha256,omitempty"`
	OldAlgo   string `json:"oldAlgorithm,omitempty"`
}

// PromptRequest asks the user for input while a connection is being set up.
type PromptRequest struct {
	// Kind: password | passphrase | keyboard-interactive | username | hostkey | confirm
	Kind      string        `json:"kind"`
	Title     string        `json:"title"`
	Message   string        `json:"message"`
	Fields    []PromptField `json:"fields,omitempty"`
	HostKey   *HostKeyInfo  `json:"hostKey,omitempty"`
	AllowSave bool          `json:"allowSave,omitempty"`
}

// PromptResponse is the user's answer.
type PromptResponse struct {
	OK     bool     `json:"ok"`
	Values []string `json:"values,omitempty"`
	Save   bool     `json:"save,omitempty"`
	// Choice is used by host-key prompts: "once" | "save" | "reject"
	Choice string `json:"choice,omitempty"`
}

// Prompter is how transports talk to the user.
type Prompter interface {
	Prompt(ctx context.Context, req PromptRequest) (PromptResponse, error)
	// Notice prints informational text (banners, progress) into the terminal.
	Notice(text string)
}

// errProcessExited marks the normal end of a local shell.
var errProcessExited = errors.New("process exited")

// noRetryError marks connection failures that automatic reconnection must
// not repeat (authentication and host key failures).
type noRetryError struct{ error }

func (e *noRetryError) Unwrap() error { return e.error }

// ErrCancelled is returned when the user dismisses a prompt.
var ErrCancelled = errors.New("cancelled by user")

// DialRequest carries everything a transport needs to connect.
type DialRequest struct {
	Opts        store.SessionOptions
	SessionPath string // "" for ad-hoc (quick connect) sessions
	Store       *store.Store
	Prompter    Prompter
	Cols, Rows  int
	Manager     *Manager
	// Password is a one-off password supplied on the command line or by a
	// script; it is tried first and never stored.
	Password string
	// depth guards against jump-host loops
	depth int
}

func (r *DialRequest) transientPassword() (string, bool) {
	return r.Password, r.Password != ""
}

// TermType returns the terminal type string announced to the host.
func (r *DialRequest) TermType() string {
	if t := strings.TrimSpace(r.Opts.Terminal.TermType); t != "" {
		return t
	}
	switch r.Opts.Terminal.Emulation {
	case "xterm", "":
		return "xterm-256color"
	default:
		return r.Opts.Terminal.Emulation
	}
}

// Dial opens the transport described by req.
func Dial(ctx context.Context, req *DialRequest) (Transport, error) {
	switch req.Opts.Protocol {
	case "ssh2", "ssh":
		return dialSSH(ctx, req)
	case "telnet", "telnet-tls":
		return dialTelnet(ctx, req)
	case "rlogin":
		return dialRlogin(ctx, req)
	case "raw":
		return dialRaw(ctx, req)
	case "serial":
		return dialSerial(ctx, req)
	case "local":
		return dialLocal(ctx, req)
	}
	return nil, fmt.Errorf("unsupported protocol %q", req.Opts.Protocol)
}

// Target describes the endpoint of a session for titles and messages.
func Target(o store.SessionOptions) string {
	switch o.Protocol {
	case "serial":
		return fmt.Sprintf("%s (%d baud)", o.Serial.Port, o.Serial.Baud)
	case "local":
		if o.Local.Shell != "" {
			return o.Local.Shell
		}
		return "local shell"
	}
	host := o.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if o.Port != 0 && o.Port != store.DefaultPort(o.Protocol) {
		host = fmt.Sprintf("%s:%d", host, o.Port)
	}
	if o.Username != "" && o.Protocol != "raw" {
		return o.Username + "@" + host
	}
	return host
}

// Unescape expands the escape notation used in "send string" settings:
// \r \n \t \e \b \\ \xHH \NNN (octal) and ^X control notation is NOT
// interpreted here (use \x03). \p is a one second pause and is returned
// as a 0x00 0xFF marker pair that callers handle via SplitPauses.
func Unescape(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			out = append(out, c)
			continue
		}
		i++
		switch s[i] {
		case 'r':
			out = append(out, '\r')
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case 'e':
			out = append(out, 0x1b)
		case 'b':
			out = append(out, 0x08)
		case 'a':
			out = append(out, 0x07)
		case 'v':
			out = append(out, 0x0b)
		case 'f':
			out = append(out, 0x0c)
		case '\\':
			out = append(out, '\\')
		case 'p':
			out = append(out, pauseMarker...)
		case 'x':
			if i+2 < len(s)+0 && i+2 <= len(s)-1 {
				if v, ok := hexByte(s[i+1], s[i+2]); ok {
					out = append(out, v)
					i += 2
					break
				}
			}
			out = append(out, '\\', 'x')
		default:
			if s[i] >= '0' && s[i] <= '7' {
				v := 0
				j := i
				for ; j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7'; j++ {
					v = v*8 + int(s[j]-'0')
				}
				out = append(out, byte(v))
				i = j - 1
			} else {
				out = append(out, '\\', s[i])
			}
		}
	}
	return out
}

var pauseMarker = []byte{0x00, 0xff, 0x00, 0xfe}

func hexByte(a, b byte) (byte, bool) {
	h := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10, true
		}
		return 0, false
	}
	x, ok1 := h(a)
	y, ok2 := h(b)
	return x<<4 | y, ok1 && ok2
}

// applyCredential resolves a session's shared credential: the user name is
// written into o and the password is returned for one-off use.
func applyCredential(st *store.Store, o *store.SessionOptions) (password string, err error) {
	if st == nil || strings.TrimSpace(o.Credential) == "" {
		return "", nil
	}
	c, ok, err := st.Vault.Credential(o.Credential)
	if err != nil {
		return "", fmt.Errorf("credential %q: %w", o.Credential, err)
	}
	if !ok {
		return "", fmt.Errorf("credential %q does not exist", o.Credential)
	}
	if c.Username != "" {
		o.Username = c.Username
	}
	return c.Password, nil
}
