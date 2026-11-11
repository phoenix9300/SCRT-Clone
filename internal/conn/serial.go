package conn

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.bug.st/serial"
)

type serialTransport struct {
	port serial.Port
	name string
	mode string
}

// SerialPorts lists the serial devices present on this machine.
func SerialPorts() []string {
	ports, err := serial.GetPortsList()
	if err != nil {
		return nil
	}
	sort.Strings(ports)
	return ports
}

func dialSerial(ctx context.Context, req *DialRequest) (Transport, error) {
	o := req.Opts.Serial
	if o.Port == "" {
		return nil, errors.New("no serial port selected")
	}
	mode := &serial.Mode{BaudRate: o.Baud, DataBits: o.DataBits}
	if mode.BaudRate <= 0 {
		mode.BaudRate = 9600
	}
	if mode.DataBits < 5 || mode.DataBits > 8 {
		mode.DataBits = 8
	}
	parityCh := "N"
	switch o.Parity {
	case "odd":
		mode.Parity, parityCh = serial.OddParity, "O"
	case "even":
		mode.Parity, parityCh = serial.EvenParity, "E"
	case "mark":
		mode.Parity, parityCh = serial.MarkParity, "M"
	case "space":
		mode.Parity, parityCh = serial.SpaceParity, "S"
	default:
		mode.Parity = serial.NoParity
	}
	stop := "1"
	switch o.StopBits {
	case "1.5":
		mode.StopBits, stop = serial.OnePointFiveStopBits, "1.5"
	case "2":
		mode.StopBits, stop = serial.TwoStopBits, "2"
	default:
		mode.StopBits = serial.OneStopBit
	}
	mode.InitialStatusBits = &serial.ModemOutputBits{DTR: o.DTR, RTS: o.RTS}
	p, err := serial.Open(o.Port, mode)
	if err != nil {
		return nil, fmt.Errorf("cannot open %s: %w", o.Port, err)
	}
	if err := applyFlowControl(o.Port, o.Flow); err != nil {
		req.Prompter.Notice(fmt.Sprintf("Flow control (%s) could not be applied: %v\r\n", o.Flow, err))
	}
	return &serialTransport{
		port: p, name: o.Port,
		mode: fmt.Sprintf("%d %d%s%s flow=%s", mode.BaudRate, mode.DataBits, parityCh, stop, o.Flow),
	}, nil
}

func (s *serialTransport) Read(p []byte) (int, error) {
	for {
		n, err := s.port.Read(p)
		if n > 0 || err != nil {
			return n, err
		}
		// some drivers return 0 bytes on timeout: keep waiting
	}
}

func (s *serialTransport) Write(p []byte) (int, error) { return s.port.Write(p) }
func (s *serialTransport) Resize(cols, rows int) error { return nil }
func (s *serialTransport) Close() error                { return s.port.Close() }
func (s *serialTransport) SendBreak() error            { return s.port.Break(300 * time.Millisecond) }

func (s *serialTransport) Info() map[string]string {
	out := map[string]string{"port": s.name, "mode": s.mode}
	if st, err := s.port.GetModemStatusBits(); err == nil {
		out["modem"] = fmt.Sprintf("CTS=%v DSR=%v DCD=%v RI=%v", st.CTS, st.DSR, st.DCD, st.RI)
	}
	return out
}
