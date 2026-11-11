//go:build !windows

package vbs

import (
	"context"
	"os/exec"
)

// shellCommand builds the command that runs cmdline through the system
// shell (WshShell.Run / WshShell.Exec).
func shellCommand(ctx context.Context, cmdline string) *exec.Cmd {
	sh, flag := hostShell()
	return exec.CommandContext(ctx, sh, flag, cmdline)
}
