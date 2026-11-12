package conn

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Telnet protocol constants (RFC 854 and friends).
const (
	tnIAC  = 255
	tnDONT = 254
	tnDO   = 253
	tnWONT = 252
	tnWILL = 251
	tnSB   = 250
	tnGA   = 249
	tnEL   = 248
	tnEC   = 247
	tnAYT  = 246
	tnAO   = 245
	tnIP   = 244
	tnBRK  = 243
	tnDM   = 242
	tnNOP  = 241
	tnSE   = 240
	tnEOR  = 239

	optBinary     = 0
	optEcho       = 1
	optSGA        = 3
	optStatus     = 5
	optTiming     = 6
	optTType      = 24
	optEOR        = 25
	optNAWS       = 31
	optTSpeed     = 32
	optLFlow      = 33
	optLinemode   = 34
	optXDispLoc   = 35
	optOldEnviron = 36
	optNewEnviron = 39

	tnIS       = 0
	tnSEND     = 1
	tnINFO     = 2
	envVar     = 0
	envValue   = 1
	envEsc     = 2
	envUserVar = 3
)

type telnetTransport struct {
	conn net.Conn
	br   *bufio.Reader

	wmu sync.Mutex // serialises writes

	mu         sync.Mutex
	local      [256]bool // options we have enabled (WILL)
	remote     [256]bool // options the peer has enabled (we sent DO)
	sentWill   [256]bool
	sentDo     [256]bool
	cols, rows int
	termTypes  []string
	ttypeIdx   int
	speed      string
	env        map[string]string
	user       string
	naws       bool
	breakCtrlC bool
	notify     func(event string, v any)

	// reader state
	pending []byte // decoded data not yet returned
	sb      []byte
	state   int
	info    map[string]string
}

const (
	tsData = iota
	tsIAC
	tsOpt // after WILL/WONT/DO/DONT
	tsSB
	tsSBIAC
	tsCR
)

func dialTelnet(ctx context.Context, req *DialRequest) (Transport, error) {
	o := req.Opts
	if strings.TrimSpace(o.Host) == "" {
		return nil, errors.New("no hostname specified")
	}
	port := o.Port
	if port == 0 {
		if o.Protocol == "telnet-tls" {
			port = 992
		} else {
			port = 23
		}
	}
	addr := net.JoinHostPort(o.Host, strconv.Itoa(port))
	timeout := 30 * time.Second
	c, parent, err := dialThrough(ctx, req, addr, timeout)
	if err != nil {
		return nil, err
	}
	var closer io.Closer = c
	if parent != nil {
		closer = &jumpCloser{Conn: c, sc: parent}
		c = closer.(net.Conn)
	}
	info := map[string]string{"remoteAddress": c.RemoteAddr().String()}
	if o.Protocol == "telnet-tls" {
		tc := tls.Client(c, &tls.Config{ServerName: o.Host, InsecureSkipVerify: !o.Telnet.TLSVerify})
		hctx, cancel := context.WithTimeout(ctx, timeout)
		err := tc.HandshakeContext(hctx)
		cancel()
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("TLS handshake failed: %w", err)
		}
		cs := tc.ConnectionState()
		info["tlsVersion"] = tls.VersionName(cs.Version)
		info["tlsCipher"] = tls.CipherSuiteName(cs.CipherSuite)
		c = tc
	}
	t := &telnetTransport{
		conn: c, br: bufio.NewReaderSize(c, 32*1024),
		cols: req.Cols, rows: req.Rows,
		termTypes:  []string{strings.ToUpper(req.TermType())},
		speed:      o.Telnet.TerminalSpeed,
		env:        o.Telnet.Env,
		user:       o.Username,
		naws:       o.Telnet.NAWS,
		breakCtrlC: o.Telnet.BreakSends == "ctrl-c",
		info:       info,
	}
	if t.speed == "" {
		t.speed = "38400,38400"
	}
	if tt := t.termTypes[0]; strings.HasPrefix(tt, "XTERM") {
		t.termTypes = append(t.termTypes, "XTERM", "VT100")
	}
	// Open negotiation ourselves on the well-known ports; elsewhere stay
	// passive so plain TCP services are not confused by IAC bytes.
	if port == 23 || port == 992 || o.Telnet.ForceCharMode {
		t.mu.Lock()
		out := t.requestDo(nil, optSGA)
		out = t.requestWill(out, optTType)
		if t.naws {
			out = t.requestWill(out, optNAWS)
		}
		if o.Telnet.ForceCharMode {
			out = t.requestDo(out, optEcho)
		}
		t.mu.Unlock()
		_ = t.rawWrite(out)
	}
	return t, nil
}

// jumpCloser releases the jump host when the tunnelled connection closes.
type jumpCloser struct {
	net.Conn
	sc   *sshConn
	once sync.Once
}

func (j *jumpCloser) Close() error {
	err := j.Conn.Close()
	j.once.Do(j.sc.release)
	return err
}

func (t *telnetTransport) rawWrite(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err := t.conn.Write(p)
	return err
}

func (t *telnetTransport) requestWill(out []byte, opt byte) []byte {
	if !t.local[opt] && !t.sentWill[opt] {
		t.sentWill[opt] = true
		out = append(out, tnIAC, tnWILL, opt)
	}
	return out
}

func (t *telnetTransport) requestDo(out []byte, opt byte) []byte {
	if !t.remote[opt] && !t.sentDo[opt] {
		t.sentDo[opt] = true
		out = append(out, tnIAC, tnDO, opt)
	}
	return out
}

// weSupport reports whether we are willing to enable opt on our side.
func (t *telnetTransport) weSupport(opt byte) bool {
	switch opt {
	case optBinary, optSGA, optTType, optTSpeed, optNewEnviron:
		return true
	case optNAWS:
		return t.naws
	}
	return false
}

// theyMay reports whether we let the peer enable opt.
func (t *telnetTransport) theyMay(opt byte) bool {
	switch opt {
	case optBinary, optEcho, optSGA, optEOR:
		return true
	}
	return false
}

func (t *telnetTransport) negotiate(cmd, opt byte) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []byte
	switch cmd {
	case tnDO:
		if t.weSupport(opt) {
			if !t.local[opt] {
				t.local[opt] = true
				if !t.sentWill[opt] {
					out = append(out, tnIAC, tnWILL, opt)
				}
				t.sentWill[opt] = false
				if opt == optNAWS {
					out = t.appendNAWS(out)
				}
			}
		} else {
			out = append(out, tnIAC, tnWONT, opt)
		}
	case tnDONT:
		if t.local[opt] || t.sentWill[opt] {
			t.local[opt] = false
			t.sentWill[opt] = false
			out = append(out, tnIAC, tnWONT, opt)
		}
	case tnWILL:
		if t.theyMay(opt) {
			if !t.remote[opt] {
				t.remote[opt] = true
				if !t.sentDo[opt] {
					out = append(out, tnIAC, tnDO, opt)
				}
				t.sentDo[opt] = false
				if opt == optEcho && t.notify != nil {
					go t.notify("remoteEcho", true)
				}
			}
		} else {
			out = append(out, tnIAC, tnDONT, opt)
		}
	case tnWONT:
		if t.remote[opt] || t.sentDo[opt] {
			was := t.remote[opt]
			t.remote[opt] = false
			t.sentDo[opt] = false
			out = append(out, tnIAC, tnDONT, opt)
			if opt == optEcho && was && t.notify != nil {
				go t.notify("remoteEcho", false)
			}
		}
	}
	return out
}

func (t *telnetTransport) appendNAWS(out []byte) []byte {
	out = append(out, tnIAC, tnSB, optNAWS)
	for _, v := range []int{t.cols >> 8, t.cols & 0xff, t.rows >> 8, t.rows & 0xff} {
		out = append(out, byte(v))
		if byte(v) == tnIAC {
			out = append(out, tnIAC)
		}
	}
	return append(out, tnIAC, tnSE)
}

func escapeIAC(out []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		out = append(out, s[i])
		if s[i] == tnIAC {
			out = append(out, tnIAC)
		}
	}
	return out
}

func (t *telnetTransport) subnegotiation(sb []byte) []byte {
	if len(sb) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []byte
	switch sb[0] {
	case optTType:
		if len(sb) >= 2 && sb[1] == tnSEND {
			tt := t.termTypes[t.ttypeIdx]
			if t.ttypeIdx < len(t.termTypes)-1 {
				t.ttypeIdx++
			}
			out = append(out, tnIAC, tnSB, optTType, tnIS)
			out = escapeIAC(out, tt)
			out = append(out, tnIAC, tnSE)
		}
	case optTSpeed:
		if len(sb) >= 2 && sb[1] == tnSEND {
			out = append(out, tnIAC, tnSB, optTSpeed, tnIS)
			out = escapeIAC(out, t.speed)
			out = append(out, tnIAC, tnSE)
		}
	case optNewEnviron:
		if len(sb) >= 2 && sb[1] == tnSEND {
			out = append(out, tnIAC, tnSB, optNewEnviron, tnIS)
			if t.user != "" {
				out = append(out, envVar)
				out = escapeIAC(out, "USER")
				out = append(out, envValue)
				out = escapeIAC(out, t.user)
			}
			for k, v := range t.env {
				out = append(out, envUserVar)
				out = escapeIAC(out, k)
				out = append(out, envValue)
				out = escapeIAC(out, v)
			}
			out = append(out, tnIAC, tnSE)
		}
	}
	return out
}

func (t *telnetTransport) Read(p []byte) (int, error) {
	for len(t.pending) == 0 {
		// read whatever is available, at least one byte
		b, err := t.br.ReadByte()
		if err != nil {
			return 0, err
		}
		var reply []byte
		reply = t.feed(b, reply)
		for n := t.br.Buffered(); n > 0; n-- {
			b, _ = t.br.ReadByte()
			reply = t.feed(b, reply)
		}
		if len(reply) > 0 {
			if err := t.rawWrite(reply); err != nil {
				return 0, err
			}
		}
	}
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	if len(t.pending) == 0 {
		t.pending = nil
	}
	return n, nil
}

func (t *telnetTransport) feed(b byte, reply []byte) []byte {
	switch t.state {
	case tsData, tsCR:
		if t.state == tsCR {
			t.state = tsData
			if b == 0 {
				return reply // CR NUL -> CR
			}
		}
		if b == tnIAC {
			t.state = tsIAC
		} else {
			t.pending = append(t.pending, b)
			if b == '\r' && !t.remote[optBinary] {
				t.state = tsCR
			}
		}
	case tsIAC:
		switch b {
		case tnIAC:
			t.pending = append(t.pending, tnIAC)
			t.state = tsData
		case tnWILL, tnWONT, tnDO, tnDONT:
			t.sb = []byte{b}
			t.state = tsOpt
		case tnSB:
			t.sb = t.sb[:0]
			t.state = tsSB
		case tnAYT:
			reply = append(reply, "\r\n[Hawser: yes]\r\n"...)
			t.state = tsData
		default:
			t.state = tsData // NOP, GA, DM, ... are ignored
		}
	case tsOpt:
		reply = append(reply, t.negotiate(t.sb[0], b)...)
		t.state = tsData
	case tsSB:
		if b == tnIAC {
			t.state = tsSBIAC
		} else if len(t.sb) < 4096 {
			t.sb = append(t.sb, b)
		}
	case tsSBIAC:
		if b == tnSE {
			reply = append(reply, t.subnegotiation(t.sb)...)
			t.state = tsData
		} else if b == tnIAC {
			if len(t.sb) < 4096 {
				t.sb = append(t.sb, tnIAC)
			}
			t.state = tsSB
		} else {
			t.state = tsData
		}
	}
	return reply
}

func (t *telnetTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	binary := t.local[optBinary]
	t.mu.Unlock()
	out := make([]byte, 0, len(p)+8)
	for i, b := range p {
		switch {
		case b == tnIAC:
			out = append(out, tnIAC, tnIAC)
		case b == '\r' && !binary:
			// a bare CR must be sent as CR NUL; CR LF passes through
			out = append(out, '\r')
			if i+1 >= len(p) || p[i+1] != '\n' {
				out = append(out, 0)
			}
		default:
			out = append(out, b)
		}
	}
	if err := t.rawWrite(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *telnetTransport) Resize(cols, rows int) error {
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	var out []byte
	if t.local[optNAWS] {
		out = t.appendNAWS(nil)
	}
	t.mu.Unlock()
	return t.rawWrite(out)
}

func (t *telnetTransport) SendBreak() error {
	if t.breakCtrlC {
		return t.rawWrite([]byte{3})
	}
	return t.rawWrite([]byte{tnIAC, tnBRK})
}

func (t *telnetTransport) KeepAlive() error { return t.rawWrite([]byte{tnIAC, tnNOP}) }

func (t *telnetTransport) Info() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]string{}
	for k, v := range t.info {
		out[k] = v
	}
	var opts []string
	names := map[byte]string{optBinary: "BINARY", optEcho: "ECHO", optSGA: "SGA", optTType: "TTYPE", optNAWS: "NAWS", optTSpeed: "TSPEED", optNewEnviron: "NEW-ENVIRON", optEOR: "EOR"}
	for o, n := range names {
		if t.local[o] {
			opts = append(opts, "WILL "+n)
		}
		if t.remote[o] {
			opts = append(opts, "DO "+n)
		}
	}
	out["telnetOptions"] = strings.Join(opts, ", ")
	return out
}

func (t *telnetTransport) Close() error { return t.conn.Close() }

// setNotify lets the manager learn about echo-mode changes.
func (t *telnetTransport) setNotify(fn func(event string, v any)) {
	t.mu.Lock()
	t.notify = fn
	t.mu.Unlock()
}
