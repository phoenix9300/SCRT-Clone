//go:build !linux && !darwin

package conn

import "errors"

func applyFlowControl(device, flow string) error {
	if flow == "" || flow == "none" {
		return nil
	}
	return errors.New("flow control selection is not implemented on this platform; the driver default is used")
}
