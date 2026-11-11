//go:build darwin

package conn

import (
	"errors"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TIOCGETA
	ioctlSetTermios = unix.TIOCSETA
)

var errDTRDSR = errors.New("DTR/DSR flow control is not available on this platform")
