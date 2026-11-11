//go:build linux

package conn

import (
	"errors"

	"golang.org/x/sys/unix"
)

const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)

var errDTRDSR = errors.New("DTR/DSR flow control is not supported by the Linux tty layer")
