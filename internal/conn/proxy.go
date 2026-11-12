package conn

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/user"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

func currentUser() string {
	if u, err := user.Current(); err == nil {
		name := u.Username
		if i := strings.LastIndexByte(name, '\\'); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	return ""
}

// dialThrough opens a TCP connection to addr, honouring the session's
// firewall setting (direct, proxy, or an SSH jump host). When a jump host is
// used its connection is returned so the caller can keep it alive.
func dialThrough(ctx context.Context, req *DialRequest, addr string, timeout time.Duration) (net.Conn, *sshConn, error) {
	o := req.Opts
	fw := strings.TrimSpace(o.Firewall)
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch {
	case fw == "" || fw == "none":
		d := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		c, err := d.DialContext(dctx, "tcp", addr)
		return c, nil, friendlyDialError(err, addr)

	case fw == "proxy":
		pw := ""
		if req.Store != nil && req.SessionPath != "" {
			pw, _, _ = req.Store.Vault.Get(store.SecretKey(req.SessionPath, "proxy"))
		}
		c, err := dialProxy(dctx, o.Proxy, pw, addr, timeout)
		return c, nil, err

	case strings.HasPrefix(fw, "session:"):
		if req.depth >= 8 {
			return nil, nil, errors.New("too many nested jump hosts (loop in firewall settings?)")
		}
		if req.Store == nil {
			return nil, nil, errors.New("jump hosts need a session store")
		}
		path := strings.TrimPrefix(fw, "session:")
		jopts, err := req.Store.Session(path)
		if err != nil {
			return nil, nil, fmt.Errorf("jump host session %q: %w", path, err)
		}
		if jopts.Protocol != "ssh2" {
			return nil, nil, fmt.Errorf("jump host session %q is not an SSH2 session", path)
		}
		req.Prompter.Notice(fmt.Sprintf("Connecting through jump host %s ...\r\n", Target(jopts)))
		jreq := &DialRequest{Opts: jopts, SessionPath: path, Store: req.Store, Prompter: req.Prompter, Manager: req.Manager, depth: req.depth + 1}
		if pw, cerr := applyCredential(req.Store, &jreq.Opts); cerr != nil {
			return nil, nil, fmt.Errorf("jump host %s: %w", path, cerr)
		} else {
			jreq.Password = pw
		}
		// port forwards configured on the jump session are not started for tunnels
		jreq.Opts.SSH.Forwards = nil
		sc, err := sshConnect(ctx, jreq)
		if err != nil {
			return nil, nil, fmt.Errorf("jump host %s: %w", path, err)
		}
		c, err := sc.client.DialContext(dctx, "tcp", addr)
		if err != nil {
			sc.release()
			return nil, nil, fmt.Errorf("jump host %s could not reach %s: %w", path, addr, err)
		}
		return c, sc, nil
	}
	return nil, nil, fmt.Errorf("unknown firewall setting %q", fw)
}

func friendlyDialError(err error, addr string) error {
	if err == nil {
		return nil
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("connection to %s timed out", addr)
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return fmt.Errorf("cannot connect to %s: %v", addr, oe.Err)
	}
	return err
}

func dialProxy(ctx context.Context, p store.ProxyOptions, password, addr string, timeout time.Duration) (net.Conn, error) {
	if p.Host == "" {
		return nil, errors.New("proxy host is not configured")
	}
	paddr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	switch p.Type {
	case "socks5":
		var auth *proxy.Auth
		if p.Username != "" {
			auth = &proxy.Auth{User: p.Username, Password: password}
		}
		sd, err := proxy.SOCKS5("tcp", paddr, auth, d)
		if err != nil {
			return nil, err
		}
		c, err := sd.(proxy.ContextDialer).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 proxy %s: %w", paddr, err)
		}
		return c, nil
	case "socks4", "socks4a":
		c, err := d.DialContext(ctx, "tcp", paddr)
		if err != nil {
			return nil, friendlyDialError(err, paddr)
		}
		if err := socks4Connect(c, addr, p.Username, p.Type == "socks4a", timeout); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("SOCKS4 proxy %s: %w", paddr, err)
		}
		return c, nil
	case "http":
		c, err := d.DialContext(ctx, "tcp", paddr)
		if err != nil {
			return nil, friendlyDialError(err, paddr)
		}
		rc, err := httpConnect(c, addr, p.Username, password, timeout)
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("HTTP proxy %s: %w", paddr, err)
		}
		return rc, nil
	}
	return nil, fmt.Errorf("unknown proxy type %q", p.Type)
}

func socks4Connect(c net.Conn, addr, userID string, useA bool, timeout time.Duration) error {
	host, portS, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(portS)
	_ = c.SetDeadline(time.Now().Add(timeout))
	defer c.SetDeadline(time.Time{})
	req := []byte{4, 1, byte(port >> 8), byte(port)}
	ip := net.ParseIP(host).To4()
	domain := ""
	if ip == nil {
		if !useA {
			ips, err := net.LookupIP(host)
			if err != nil {
				return err
			}
			for _, cand := range ips {
				if v4 := cand.To4(); v4 != nil {
					ip = v4
					break
				}
			}
			if ip == nil {
				return errors.New("SOCKS4 needs an IPv4 address")
			}
		} else {
			ip = net.IPv4(0, 0, 0, 1).To4()
			domain = host
		}
	}
	req = append(req, ip...)
	req = append(req, userID...)
	req = append(req, 0)
	if domain != "" {
		req = append(req, domain...)
		req = append(req, 0)
	}
	if _, err := c.Write(req); err != nil {
		return err
	}
	var resp [8]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		return err
	}
	if resp[1] != 90 {
		return fmt.Errorf("request rejected (code %d)", resp[1])
	}
	return nil
}

// bufferedConn returns bytes already buffered by a bufio.Reader before
// reading from the connection again.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func httpConnect(c net.Conn, addr, username, password string, timeout time.Duration) (net.Conn, error) {
	_ = c.SetDeadline(time.Now().Add(timeout))
	defer c.SetDeadline(time.Time{})
	var sb strings.Builder
	fmt.Fprintf(&sb, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n", addr, addr)
	if username != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		fmt.Fprintf(&sb, "Proxy-Authorization: Basic %s\r\n", cred)
	}
	sb.WriteString("\r\n")
	if _, err := io.WriteString(c, sb.String()); err != nil {
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("CONNECT failed: %s", resp.Status)
	}
	return &bufferedConn{Conn: c, r: br}, nil
}

// serveSOCKS implements a minimal SOCKS4/4a/5 server (CONNECT only, no
// authentication) for dynamic port forwarding.
func serveSOCKS(c net.Conn, dial func(addr string) (net.Conn, error)) {
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	ver, err := br.ReadByte()
	if err != nil {
		return
	}
	var target string
	switch ver {
	case 4:
		var hdr [7]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil || hdr[0] != 1 {
			return
		}
		port := binary.BigEndian.Uint16(hdr[1:3])
		ip := net.IP(hdr[3:7])
		if _, err := br.ReadString(0); err != nil { // user id
			return
		}
		host := ip.String()
		if hdr[3] == 0 && hdr[4] == 0 && hdr[5] == 0 && hdr[6] != 0 {
			name, err := br.ReadString(0)
			if err != nil {
				return
			}
			host = strings.TrimSuffix(name, "\x00")
		}
		target = net.JoinHostPort(host, strconv.Itoa(int(port)))
		r, err := dial(target)
		if err != nil {
			_, _ = c.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
			return
		}
		_, _ = c.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0})
		_ = c.SetDeadline(time.Time{})
		pipeBoth(&bufferedConn{Conn: c, r: br}, r)
	case 5:
		n, err := br.ReadByte()
		if err != nil {
			return
		}
		methods := make([]byte, n)
		if _, err := io.ReadFull(br, methods); err != nil {
			return
		}
		if _, err := c.Write([]byte{5, 0}); err != nil {
			return
		}
		var hdr [4]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil || hdr[0] != 5 {
			return
		}
		var host string
		switch hdr[3] {
		case 1:
			var ip [4]byte
			if _, err := io.ReadFull(br, ip[:]); err != nil {
				return
			}
			host = net.IP(ip[:]).String()
		case 3:
			l, err := br.ReadByte()
			if err != nil {
				return
			}
			name := make([]byte, l)
			if _, err := io.ReadFull(br, name); err != nil {
				return
			}
			host = string(name)
		case 4:
			var ip [16]byte
			if _, err := io.ReadFull(br, ip[:]); err != nil {
				return
			}
			host = net.IP(ip[:]).String()
		default:
			_, _ = c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		var pb [2]byte
		if _, err := io.ReadFull(br, pb[:]); err != nil {
			return
		}
		if hdr[1] != 1 {
			_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		target = net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:]))))
		r, err := dial(target)
		if err != nil {
			_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		_ = c.SetDeadline(time.Time{})
		pipeBoth(&bufferedConn{Conn: c, r: br}, r)
	}
}
