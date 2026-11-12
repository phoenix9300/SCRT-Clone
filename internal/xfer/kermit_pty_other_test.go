//go:build !linux

package xfer

import (
	"errors"
	"os"
	"os/exec"
)

func openPTY() (master, slave *os.File, err error) {
	return nil, nil, errors.New("pseudo terminals are only supported on Linux")
}

func ptyChild(cmd *exec.Cmd, slave *os.File) {}
