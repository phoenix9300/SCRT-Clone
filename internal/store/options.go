// Package store persists Hawser's configuration: the session tree, global
// options, the SSH host key database and the encrypted credential vault.
package store

// SessionOptions is the complete, effective configuration of a session.
// Sessions are stored sparsely (only the values that differ from the default
// session), see Store.Session.
type SessionOptions struct {
	Protocol    string `json:"protocol"` // ssh2 | telnet | telnet-tls | rlogin | raw | serial | local
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Description string `json:"description"`
	// SavePassword records that a password for this session lives in the vault.
	SavePassword bool `json:"savePassword"`
	// Firewall selects how the transport is reached:
	//   "" / "none"        direct connection
	//   "session:<path>"   through another SSH2 session (jump host)
	//   "proxy"            through the proxy configured in Proxy
	Firewall string `json:"firewall"`

	Proxy      ProxyOptions      `json:"proxy"`
	SSH        SSHOptions        `json:"ssh"`
	Telnet     TelnetOptions     `json:"telnet"`
	Rlogin     RloginOptions     `json:"rlogin"`
	Raw        RawOptions        `json:"raw"`
	Serial     SerialOptions     `json:"serial"`
	Local      LocalOptions      `json:"local"`
	Terminal   TerminalOptions   `json:"terminal"`
	Appearance AppearanceOptions `json:"appearance"`
	Logon      LogonOptions      `json:"logon"`
	Log        LogOptions        `json:"log"`
	Connection ConnectionOptions `json:"connection"`
	Transfer   TransferOptions   `json:"transfer"`
}

type ProxyOptions struct {
	Type     string `json:"type"` // none | socks4 | socks4a | socks5 | http
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	// the proxy password is kept in the vault under "proxy:<session path>"
}

type PortForward struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // local | remote | dynamic
	BindAddr string `json:"bindAddr"`
	BindPort int    `json:"bindPort"`
	DestHost string `json:"destHost"`
	DestPort int    `json:"destPort"`
	Enabled  bool   `json:"enabled"`
}

type SSHOptions struct {
	// Auth lists authentication methods in the order they are attempted:
	// publickey | keyboard-interactive | password | agent
	Auth              []string `json:"auth"`
	IdentityFiles     []string `json:"identityFiles"`
	UseAgent          bool     `json:"useAgent"`
	AgentForwarding   bool     `json:"agentForwarding"`
	X11Forwarding     bool     `json:"x11Forwarding"`
	X11Display        string   `json:"x11Display"`
	KexAlgorithms     []string `json:"kexAlgorithms"`
	Ciphers           []string `json:"ciphers"`
	MACs              []string `json:"macs"`
	HostKeyAlgorithms []string `json:"hostKeyAlgorithms"`
	// HostKeyPolicy: ask | accept-new | strict | off
	HostKeyPolicy     string            `json:"hostKeyPolicy"`
	UseOpenSSHHosts   bool              `json:"useOpenSSHKnownHosts"`
	RemoteCommand     string            `json:"remoteCommand"`
	Subsystem         string            `json:"subsystem"`
	RequestPty        bool              `json:"requestPty"`
	Env               map[string]string `json:"env"`
	Forwards          []PortForward     `json:"forwards"`
	KeepAliveInterval int               `json:"keepAliveInterval"` // seconds, 0 = off
	ConnectTimeout    int               `json:"connectTimeout"`    // seconds
	SFTPSubsystem     string            `json:"sftpSubsystem"`
	ShowBanner        bool              `json:"showBanner"`
}

type TelnetOptions struct {
	NAWS          bool              `json:"naws"`
	TerminalSpeed string            `json:"terminalSpeed"`
	ForceCharMode bool              `json:"forceCharMode"`
	Env           map[string]string `json:"env"`
	TLSVerify     bool              `json:"tlsVerify"`
	// BreakSends: "telnet" (IAC BRK) or "ctrl-c"
	BreakSends string `json:"breakSends"`
}

type RloginOptions struct {
	LocalUser     string `json:"localUser"`
	TerminalSpeed string `json:"terminalSpeed"`
}

type RawOptions struct {
	TLS       bool `json:"tls"`
	TLSVerify bool `json:"tlsVerify"`
	// LocalEcho/CRLF translation are terminal options
}

type SerialOptions struct {
	Port     string `json:"port"`
	Baud     int    `json:"baud"`
	DataBits int    `json:"dataBits"`
	Parity   string `json:"parity"`   // none | odd | even | mark | space
	StopBits string `json:"stopBits"` // 1 | 1.5 | 2
	Flow     string `json:"flow"`     // none | rtscts | xonxoff | dtrdsr
	DTR      bool   `json:"dtr"`
	RTS      bool   `json:"rts"`
}

type LocalOptions struct {
	Shell string            `json:"shell"`
	Args  []string          `json:"args"`
	Cwd   string            `json:"cwd"`
	Env   map[string]string `json:"env"`
	Login bool              `json:"login"`
}

type AntiIdle struct {
	Enabled  bool   `json:"enabled"`
	Text     string `json:"text"`     // string to send; empty = protocol NO-OP
	Interval int    `json:"interval"` // seconds
}

type MappedKey struct {
	Key    string `json:"key"`    // e.g. "Ctrl+Shift+F5"
	Action string `json:"action"` // send | menu | script | disabled
	Value  string `json:"value"`
}

type TerminalOptions struct {
	Emulation             string      `json:"emulation"`
	TermType              string      `json:"termType"` // overrides the emulation's default TERM
	Encoding              string      `json:"encoding"`
	Scrollback            int         `json:"scrollback"`
	Cols                  int         `json:"cols"`
	Rows                  int         `json:"rows"`
	SizeMode              string      `json:"sizeMode"` // fit | fixed
	Answerback            string      `json:"answerback"`
	NewLineMode           bool        `json:"newLineMode"`
	AutoWrap              bool        `json:"autoWrap"`
	AnsiColor             bool        `json:"ansiColor"`
	Allow132              bool        `json:"allow132"`
	Backspace             string      `json:"backspace"` // del | bs
	DeleteKey             string      `json:"deleteKey"` // vt | del
	AltIsMeta             bool        `json:"altIsMeta"`
	LocalEcho             bool        `json:"localEcho"`
	CursorStyle           string      `json:"cursorStyle"` // block | underline | bar
	CursorBlink           bool        `json:"cursorBlink"`
	Bell                  string      `json:"bell"` // visual | audible | both | none
	ScrollOnOutput        bool        `json:"scrollOnOutput"`
	ScrollOnKey           bool        `json:"scrollOnKey"`
	ScrollToClear         bool        `json:"scrollToClear"`
	CopyOnSelect          bool        `json:"copyOnSelect"`
	RightClick            string      `json:"rightClick"`  // menu | paste
	MiddleClick           string      `json:"middleClick"` // paste | none
	WordDelimiters        string      `json:"wordDelimiters"`
	ConfirmMultilinePaste bool        `json:"confirmMultilinePaste"`
	TrimPaste             bool        `json:"trimPaste"`
	LineSendDelay         int         `json:"lineSendDelay"` // ms
	CharSendDelay         int         `json:"charSendDelay"` // ms
	NewlineOnSend         string      `json:"newlineOnSend"` // cr | lf | crlf
	AllowTitleChange      bool        `json:"allowTitleChange"`
	AllowClipboardWrite   bool        `json:"allowClipboardWrite"`
	AllowMouseReports     bool        `json:"allowMouseReports"`
	C1Controls            bool        `json:"c1Controls"`
	Keymap                []MappedKey `json:"keymap"`
	KeywordSet            string      `json:"keywordSet"`
	AntiIdle              AntiIdle    `json:"antiIdle"`
	Printer               string      `json:"printer"` // dialog | file | ignore
}

type AppearanceOptions struct {
	ColorScheme  string  `json:"colorScheme"`
	FontFamily   string  `json:"fontFamily"`
	FontSize     float64 `json:"fontSize"`
	LineHeight   float64 `json:"lineHeight"`
	BoldAsBright bool    `json:"boldAsBright"`
	BoldFont     bool    `json:"boldFont"`
	Blink        bool    `json:"blink"`
	TabColor     string  `json:"tabColor"`
	Ligatures    bool    `json:"ligatures"`
}

type LogonAction struct {
	Expect string `json:"expect"`
	Send   string `json:"send"`
	// Hide marks Send as a secret (stored in the vault, never shown or logged)
	Hide bool `json:"hide"`
}

type LogonOptions struct {
	Enabled    bool          `json:"enabled"`
	Actions    []LogonAction `json:"actions"`
	Timeout    int           `json:"timeout"` // seconds per expect
	Script     string        `json:"script"`  // script to run after connect
	ScriptArgs string        `json:"scriptArgs"`
}

type LogOptions struct {
	StartOnConnect    bool   `json:"startOnConnect"`
	File              string `json:"file"` // supports %H %S %Y %M %D %h %m %s %t %% substitutions
	Raw               bool   `json:"raw"`
	Append            bool   `json:"append"`
	PromptForFile     bool   `json:"promptForFile"`
	OnConnect         string `json:"onConnect"`
	OnDisconnect      string `json:"onDisconnect"`
	OnEachLine        string `json:"onEachLine"`
	OnlyCustom        bool   `json:"onlyCustom"`
	NewFileAtMidnight bool   `json:"newFileAtMidnight"`
}

type ConnectionOptions struct {
	AutoReconnect     bool `json:"autoReconnect"`
	ReconnectDelay    int  `json:"reconnectDelay"` // seconds
	CloseOnDisconnect bool `json:"closeOnDisconnect"`
}

type TransferOptions struct {
	DownloadDir     string `json:"downloadDir"`
	UploadDir       string `json:"uploadDir"`
	ZmodemAuto      bool   `json:"zmodemAuto"`
	ZmodemOverwrite bool   `json:"zmodemOverwrite"`
	ZmodemResume    bool   `json:"zmodemResume"`
	Xmodem1K        bool   `json:"xmodem1k"`
	XmodemCRC       bool   `json:"xmodemCrc"`
	SFTPInitialDir  string `json:"sftpInitialDir"`
}

// DefaultPort returns the conventional port of a protocol.
func DefaultPort(protocol string) int {
	switch protocol {
	case "ssh2":
		return 22
	case "telnet":
		return 23
	case "telnet-tls":
		return 992
	case "rlogin":
		return 513
	}
	return 0
}

// FactoryDefaults returns the built-in default session.
func FactoryDefaults() SessionOptions {
	return SessionOptions{
		Protocol: "ssh2",
		Port:     22,
		Firewall: "none",
		Proxy:    ProxyOptions{Type: "none", Port: 1080},
		SSH: SSHOptions{
			Auth:              []string{"publickey", "keyboard-interactive", "password"},
			IdentityFiles:     []string{},
			UseAgent:          true,
			KexAlgorithms:     []string{},
			Ciphers:           []string{},
			MACs:              []string{},
			HostKeyAlgorithms: []string{},
			HostKeyPolicy:     "ask",
			UseOpenSSHHosts:   true,
			RequestPty:        true,
			Env:               map[string]string{},
			Forwards:          []PortForward{},
			KeepAliveInterval: 0,
			ConnectTimeout:    30,
			SFTPSubsystem:     "sftp",
			ShowBanner:        true,
		},
		Telnet: TelnetOptions{NAWS: true, TerminalSpeed: "38400,38400", Env: map[string]string{}, TLSVerify: true, BreakSends: "telnet"},
		Rlogin: RloginOptions{TerminalSpeed: "38400"},
		Raw:    RawOptions{TLSVerify: true},
		Serial: SerialOptions{Baud: 9600, DataBits: 8, Parity: "none", StopBits: "1", Flow: "none", DTR: true, RTS: true},
		Local:  LocalOptions{Args: []string{}, Env: map[string]string{}},
		Terminal: TerminalOptions{
			Emulation:             "xterm",
			Encoding:              "utf-8",
			Scrollback:            5000,
			Cols:                  80,
			Rows:                  24,
			SizeMode:              "fit",
			AutoWrap:              true,
			AnsiColor:             true,
			Allow132:              true,
			Backspace:             "del",
			DeleteKey:             "vt",
			AltIsMeta:             true,
			CursorStyle:           "block",
			CursorBlink:           true,
			Bell:                  "visual",
			ScrollOnKey:           true,
			RightClick:            "menu",
			MiddleClick:           "paste",
			WordDelimiters:        " \t\"'`()[]{}<>|;,",
			ConfirmMultilinePaste: true,
			NewlineOnSend:         "cr",
			AllowTitleChange:      true,
			AllowClipboardWrite:   true,
			AllowMouseReports:     true,
			Keymap:                []MappedKey{},
			KeywordSet:            "",
			AntiIdle:              AntiIdle{Text: "", Interval: 60},
			Printer:               "dialog",
		},
		Appearance: AppearanceOptions{
			ColorScheme:  "",
			FontFamily:   "",
			FontSize:     0,
			LineHeight:   1.0,
			BoldAsBright: true,
			BoldFont:     true,
			Blink:        true,
		},
		Logon: LogonOptions{Actions: []LogonAction{}, Timeout: 30},
		Log: LogOptions{
			File:   "%S_%Y-%M-%D_%h%m%s.log",
			Append: true,
		},
		Connection: ConnectionOptions{ReconnectDelay: 5},
		Transfer:   TransferOptions{ZmodemAuto: true, ZmodemResume: true, XmodemCRC: true},
	}
}
