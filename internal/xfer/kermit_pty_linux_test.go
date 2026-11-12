//go:build linux

package xfer

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

func ptyIoctl(f *os.File, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, arg); e != 0 {
		return e
	}
	return nil
}

// openPTY returns a pseudo terminal whose slave side is in raw mode, so that
// binary protocol data passes through unchanged from the very first byte.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	var n uint32
	if err = ptyIoctl(master, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err == nil {
		err = ptyIoctl(master, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
	}
	if err == nil {
		slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	}
	if err == nil {
		var tio syscall.Termios
		if err = ptyIoctl(slave, syscall.TCGETS, uintptr(unsafe.Pointer(&tio))); err == nil {
			tio.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF
			tio.Oflag &^= syscall.OPOST
			tio.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
			tio.Cflag &^= syscall.CSIZE | syscall.PARENB
			tio.Cflag |= syscall.CS8
			tio.Cc[syscall.VMIN], tio.Cc[syscall.VTIME] = 1, 0
			err = ptyIoctl(slave, syscall.TCSETS, uintptr(unsafe.Pointer(&tio)))
		}
	}
	if err != nil {
		master.Close()
		if slave != nil {
			slave.Close()
		}
		return nil, nil, err
	}
	return master, slave, nil
}

// ptyChild makes the command run with the pseudo terminal as its standard
// input and output and as its controlling terminal.
func ptyChild(cmd *exec.Cmd, slave *os.File) {
	cmd.Stdin, cmd.Stdout = slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}
