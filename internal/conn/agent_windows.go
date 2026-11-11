//go:build windows

package conn

import (
	"io"
	"os"

	"golang.org/x/crypto/ssh/agent"
)

// systemAgent connects to the Windows OpenSSH agent service.
func systemAgent() (agent.ExtendedAgent, io.Closer, error) {
	f, err := os.OpenFile(`\\.\pipe\openssh-ssh-agent`, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	return agent.NewClient(f), f, nil
}
