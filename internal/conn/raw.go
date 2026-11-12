package conn

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ------------------------------------------------------------------- raw

type rawTransport struct {
	net.Conn
	info map[string]string
}

func (r *rawTransport) Resize(cols, rows int) error { return nil }
func (r *rawTransport) Info() map[string]string     { return r.info }

func dialRaw(ctx context.Context, req *DialRequest) (Transport, error) {
	o := req.Opts
	if strings.TrimSpace(o.Host) == "" {
		return nil, errors.New("no hostname specified")
	}
	if o.Port == 0 {
		return nil, errors.New("no port specified")
	}
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	timeout := 30 * time.Second
	c, parent, err := dialThrough(ctx, req, addr, timeout)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		c = &jumpCloser{Conn: c, sc: parent}
	}
	info := map[string]string{"remoteAddress": c.RemoteAddr().String()}
	if o.Raw.TLS {
		tc := tls.Client(c, &tls.Config{ServerName: o.Host, InsecureSkipVerify: !o.Raw.TLSVerify})
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
	return &rawTransport{Conn: c, info: info}, nil
}

// ---------------------------------------------------------------- rlogin

// rloginTransport implements the BSD rlogin protocol (RFC 1282).
type rloginTransport struct {
	conn net.Conn
	wmu  sync.Mutex
}

func dialRlogin(ctx context.Context, req *DialRequest) (Transport, error) {
	o := req.Opts
	if strings.TrimSpace(o.Host) == "" {
		return nil, errors.New("no hostname specified")
	}
	port := o.Port
	if port == 0 {
		port = 513
	}
	addr := net.JoinHostPort(o.Host, strconv.Itoa(port))
	timeout := 30 * time.Second
	var c net.Conn
	var err error
	fw := strings.TrimSpace(o.Firewall)
	if fw == "" || fw == "none" {
		// rlogind traditionally insists on a privileged source port; try to
		// obtain one and fall back to an ordinary port when not permitted.
		c = dialPrivileged(ctx, addr, timeout)
	}
	if c == nil {
		var parent *sshConn
		c, parent, err = dialThrough(ctx, req, addr, timeout)
		if err != nil {
			return nil, err
		}
		if parent != nil {
			c = &jumpCloser{Conn: c, sc: parent}
		}
	}
	local := o.Rlogin.LocalUser
	if local == "" {
		local = currentUser()
	}
	remote := o.Username
	if remote == "" {
		remote = local
	}
	speed := o.Rlogin.TerminalSpeed
	if speed == "" {
		speed = "38400"
	}
	hello := "\x00" + local + "\x00" + remote + "\x00" + req.TermType() + "/" + speed + "\x00"
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(c, hello); err != nil {
		_ = c.Close()
		return nil, err
	}
	var ack [1]byte
	if _, err := io.ReadFull(c, ack[:]); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("rlogin handshake failed: %w", err)
	}
	_ = c.SetDeadline(time.Time{})
	t := &rloginTransport{conn: c}
	if ack[0] != 0 {
		// the server sent an error message instead of the zero byte
		rest := make([]byte, 512)
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := c.Read(rest)
		_ = c.Close()
		msg := string(rest[:n])
		if ack[0] >= ' ' { // a printable first byte is part of the message; 0x01 is just the error marker
			msg = string(ack[:]) + msg
		}
		return nil, fmt.Errorf("rlogin refused: %s", strings.TrimSpace(msg))
	}
	_ = t.Resize(req.Cols, req.Rows)
	return t, nil
}

func dialPrivileged(ctx context.Context, addr string, timeout time.Duration) net.Conn {
	for port := 1023; port >= 512; port-- {
		d := net.Dialer{Timeout: timeout, LocalAddr: &net.TCPAddr{Port: port}}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			return c
		}
		var oe *net.OpError
		if errors.As(err, &oe) && oe.Op == "dial" && strings.Contains(err.Error(), "permission denied") {
			return nil // not privileged: no point trying the other ports
		}
		if !strings.Contains(err.Error(), "address already in use") {
			return nil
		}
	}
	return nil
}

func (t *rloginTransport) Read(p []byte) (int, error) { return t.conn.Read(p) }

func (t *rloginTransport) Write(p []byte) (int, error) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	return t.conn.Write(p)
}

// Resize sends the window-size control sequence (0xFF 0xFF 's' 's' ...).
func (t *rloginTransport) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	msg := make([]byte, 12)
	msg[0], msg[1], msg[2], msg[3] = 0xff, 0xff, 's', 's'
	binary.BigEndian.PutUint16(msg[4:], uint16(rows))
	binary.BigEndian.PutUint16(msg[6:], uint16(cols))
	t.wmu.Lock()
	defer t.wmu.Unlock()
	_, err := t.conn.Write(msg)
	return err
}

func (t *rloginTransport) Close() error { return t.conn.Close() }

func (t *rloginTransport) Info() map[string]string {
	return map[string]string{"remoteAddress": t.conn.RemoteAddr().String(), "localAddress": t.conn.LocalAddr().String()}
}
