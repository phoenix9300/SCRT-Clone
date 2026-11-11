//go:build !windows

package conn

import (
	"errors"
	"io"
	"net"
	"os"

	"golang.org/x/crypto/ssh/agent"
)

// systemAgent connects to the user's running ssh-agent.
func systemAgent() (agent.ExtendedAgent, io.Closer, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, nil, err
	}
	return agent.NewClient(c), c, nil
}
