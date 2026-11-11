package conn

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// x11State holds what is needed to proxy X11 channels to the local display.
type x11State struct {
	network, address string
	display          string
	fakeCookie       []byte
	realCookie       []byte
}

const x11AuthProto = "MIT-MAGIC-COOKIE-1"

func parseDisplay(display string) (network, address string, screen uint32, err error) {
	if display == "" {
		display = os.Getenv("DISPLAY")
	}
	if display == "" {
		return "", "", 0, errors.New("no X display configured (DISPLAY is not set)")
	}
	if strings.HasPrefix(display, "/") { // macOS launchd socket path
		p := display
		if i := strings.LastIndexByte(p, ':'); i >= 0 {
			p = p[:i]
		}
		return "unix", p, 0, nil
	}
	i := strings.LastIndexByte(display, ':')
	if i < 0 {
		return "", "", 0, fmt.Errorf("malformed display %q", display)
	}
	host := display[:i]
	rest := display[i+1:]
	num := rest
	if j := strings.IndexByte(rest, '.'); j >= 0 {
		num = rest[:j]
		if s, err := strconv.Atoi(rest[j+1:]); err == nil {
			screen = uint32(s)
		}
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return "", "", 0, fmt.Errorf("malformed display %q", display)
	}
	if host == "" || host == "unix" {
		return "unix", fmt.Sprintf("/tmp/.X11-unix/X%d", n), screen, nil
	}
	return "tcp", net.JoinHostPort(host, strconv.Itoa(6000+n)), screen, nil
}

// xauthCookie fetches the MIT-MAGIC-COOKIE-1 for display via xauth (best effort).
func xauthCookie(display string) []byte {
	if display == "" {
		display = os.Getenv("DISPLAY")
	}
	out, err := exec.Command("xauth", "list", display).Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[1] == x11AuthProto {
			if b, err := hex.DecodeString(f[2]); err == nil {
				return b
			}
		}
	}
	return nil
}

// requestX11 asks the server to forward X11 connections for sess.
func (sc *sshConn) requestX11(sess *ssh.Session, display string) error {
	network, address, screen, err := parseDisplay(display)
	if err != nil {
		return err
	}
	var setupErr error
	sc.x11Once.Do(func() {
		fake, _ := hex.DecodeString(randomHex(16))
		sc.x11 = &x11State{network: network, address: address, display: display, fakeCookie: fake, realCookie: xauthCookie(display)}
		chans := sc.client.HandleChannelOpen("x11")
		if chans == nil {
			setupErr = errors.New("x11 channel handler already registered")
			return
		}
		go func() {
			for nc := range chans {
				go sc.serveX11(nc)
			}
		}()
	})
	if setupErr != nil {
		return setupErr
	}
	payload := ssh.Marshal(struct {
		SingleConnection bool
		AuthProtocol     string
		AuthCookie       string
		ScreenNumber     uint32
	}{false, x11AuthProto, hex.EncodeToString(sc.x11.fakeCookie), screen})
	ok, err := sess.SendRequest("x11-req", true, payload)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the server refused the request")
	}
	return nil
}

func (sc *sshConn) serveX11(nc ssh.NewChannel) {
	st := sc.x11
	local, err := net.DialTimeout(st.network, st.address, 10*time.Second)
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, "cannot connect to local X display")
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = local.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	// Rewrite the connection setup: swap the fake cookie for the real one.
	if err := x11Handshake(ch, local, st); err != nil {
		_ = ch.Close()
		_ = local.Close()
		return
	}
	pipeBoth(ch, local)
}

func x11Handshake(ch io.Reader, local io.Writer, st *x11State) error {
	var hdr [12]byte
	if _, err := io.ReadFull(ch, hdr[:]); err != nil {
		return err
	}
	var bo binary.ByteOrder
	switch hdr[0] {
	case 'B':
		bo = binary.BigEndian
	case 'l':
		bo = binary.LittleEndian
	default:
		return errors.New("bad X11 byte order")
	}
	nameLen := int(bo.Uint16(hdr[6:8]))
	dataLen := int(bo.Uint16(hdr[8:10]))
	pad := func(n int) int { return (n + 3) &^ 3 }
	if nameLen > 256 || dataLen > 256 {
		return errors.New("oversized X11 authorization")
	}
	body := make([]byte, pad(nameLen)+pad(dataLen))
	if _, err := io.ReadFull(ch, body); err != nil {
		return err
	}
	name := body[:nameLen]
	data := body[pad(nameLen) : pad(nameLen)+dataLen]
	if string(name) != x11AuthProto || !bytes.Equal(data, st.fakeCookie) {
		return errors.New("X11 connection presented wrong authorization")
	}
	if len(st.realCookie) == len(data) {
		copy(data, st.realCookie)
		_, err := local.Write(append(hdr[:], body...))
		return err
	}
	// no local cookie known: connect without authorization (works when the
	// X server allows local connections)
	bo.PutUint16(hdr[6:8], 0)
	bo.PutUint16(hdr[8:10], 0)
	_, err := local.Write(hdr[:])
	return err
}
