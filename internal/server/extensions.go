package server

import (
	"os"
	"sync"
)

// extensions holds optional subsystems (TFTP server, scripting, key tools).
type extensions struct {
	mu    sync.Mutex
	temps []string
	stops []func()
}

func (e *extensions) trackTemp(dir string) {
	e.mu.Lock()
	e.temps = append(e.temps, dir)
	e.mu.Unlock()
}

func (e *extensions) onClose(fn func()) {
	e.mu.Lock()
	e.stops = append(e.stops, fn)
	e.mu.Unlock()
}

func (e *extensions) close() {
	e.mu.Lock()
	temps, stops := e.temps, e.stops
	e.temps, e.stops = nil, nil
	e.mu.Unlock()
	for _, fn := range stops {
		fn()
	}
	for _, d := range temps {
		_ = os.RemoveAll(d)
	}
}

func (s *Server) registerExtensions() {
	s.registerKeys()
}
