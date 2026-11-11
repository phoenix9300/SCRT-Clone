//go:build windows

package vbs

import (
	"context"
	"os/exec"
	"syscall"
)

// shellCommand builds the command that runs cmdline through the system
// shell (WshShell.Run / WshShell.Exec). The command line is handed to
// cmd.exe verbatim so that its own quoting rules apply.
func shellCommand(ctx context.Context, cmdline string) *exec.Cmd {
	sh, flag := hostShell()
	cmd := exec.CommandContext(ctx, sh)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + sh + `" ` + flag + " " + cmdline}
	return cmd
}
