//go:build linux || darwin

package conn

import (
	"golang.org/x/sys/unix"
)

// applyFlowControl sets hardware / software flow control on an open serial
// device. Terminal attributes belong to the device, not the descriptor, so
// adjusting them through a second descriptor affects the port opened by the
// serial library.
func applyFlowControl(device, flow string) error {
	if flow == "" || flow == "none" {
		return nil
	}
	fd, err := unix.Open(device, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return err
	}
	t.Cflag &^= unix.CRTSCTS
	t.Iflag &^= unix.IXON | unix.IXOFF | unix.IXANY
	switch flow {
	case "rtscts":
		t.Cflag |= unix.CRTSCTS
	case "xonxoff":
		t.Iflag |= unix.IXON | unix.IXOFF
	case "dtrdsr":
		return errDTRDSR
	}
	return unix.IoctlSetTermios(fd, ioctlSetTermios, t)
}
