//go:build windows

package conn

import "os/exec"

func lookPath(name string) (string, error) { return exec.LookPath(name) }
