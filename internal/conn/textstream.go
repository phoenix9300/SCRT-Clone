package conn

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"sync"
)

// stripper removes terminal control sequences from a byte stream, leaving the
// text a person would read (plus CR, LF, TAB and BS).
type stripper struct {
	state int
}

const (
	stGround = iota
	stEsc
	stEscInter
	stCSI
	stString // OSC / DCS / SOS / PM / APC until BEL or ST
	stStringEsc
)

// Feed appends the printable part of p to out and returns it.
func (s *stripper) Feed(p []byte, out []byte) []byte {
	for _, c := range p {
		switch s.state {
		case stGround:
			switch {
			case c == 0x1b:
				s.state = stEsc
			case c == '\r' || c == '\n' || c == '\t' || c == 0x08:
				out = append(out, c)
			case c < 0x20 || c == 0x7f:
				// other controls are dropped
			default:
				out = append(out, c)
			}
		case stEsc:
			switch {
			case c == '[':
				s.state = stCSI
			case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
				s.state = stString
			case c >= 0x20 && c <= 0x2f:
				s.state = stEscInter
			case c == 0x1b:
				// stay
			default:
				s.state = stGround
			}
		case stEscInter:
			if c < 0x20 || c > 0x2f {
				s.state = stGround
			}
		case stCSI:
			switch {
			case c >= 0x40 && c <= 0x7e:
				s.state = stGround
			case c == 0x1b:
				s.state = stEsc
			case c == '\r' || c == '\n':
				out = append(out, c)
			}
		case stString:
			switch c {
			case 0x07:
				s.state = stGround
			case 0x1b:
				s.state = stStringEsc
			case 0x18, 0x1a:
				s.state = stGround
			}
		case stStringEsc:
			if c == '\\' {
				s.state = stGround
			} else if c == '[' {
				s.state = stCSI
			} else if c != 0x1b {
				s.state = stGround
			}
		}
	}
	return out
}

// textBuffer keeps a rolling window of stripped output for expect-style
// matching. Positions are absolute offsets into the stripped stream.
type textBuffer struct {
	mu      sync.Mutex
	data    []byte
	base    int64 // absolute offset of data[0]
	waiters map[chan struct{}]struct{}
	closed  bool
}

const textBufferMax = 4 << 20

func newTextBuffer() *textBuffer {
	return &textBuffer{waiters: map[chan struct{}]struct{}{}}
}

func (b *textBuffer) append(p []byte) {
	if len(p) == 0 {
		return
	}
	b.mu.Lock()
	b.data = append(b.data, p...)
	if len(b.data) > textBufferMax {
		drop := len(b.data) - textBufferMax/2
		b.data = append(b.data[:0], b.data[drop:]...)
		b.base += int64(drop)
	}
	for ch := range b.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *textBuffer) close() {
	b.mu.Lock()
	b.closed = true
	for ch := range b.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *textBuffer) reopen() {
	b.mu.Lock()
	b.closed = false
	b.mu.Unlock()
}

// End returns the absolute offset just past the last byte received.
func (b *textBuffer) End() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.base + int64(len(b.data))
}

// Matcher describes what a waiter is looking for.
type Matcher struct {
	Patterns   []string
	IgnoreCase bool
	Regex      bool
	compiled   []*regexp.Regexp
	lowered    [][]byte
}

func (m *Matcher) prepare() error {
	if m.Regex {
		for _, p := range m.Patterns {
			if m.IgnoreCase {
				p = "(?i)" + p
			}
			re, err := regexp.Compile(p)
			if err != nil {
				return err
			}
			m.compiled = append(m.compiled, re)
		}
		return nil
	}
	for _, p := range m.Patterns {
		if m.IgnoreCase {
			m.lowered = append(m.lowered, []byte(strings.ToLower(p)))
		} else {
			m.lowered = append(m.lowered, []byte(p))
		}
	}
	return nil
}

// find returns the pattern index and the [start, end) of the earliest match in hay.
func (m *Matcher) find(hay []byte) (idx, start, end int) {
	idx, start = -1, -1
	if m.Regex {
		for i, re := range m.compiled {
			if loc := re.FindIndex(hay); loc != nil && (start < 0 || loc[0] < start) {
				idx, start, end = i, loc[0], loc[1]
			}
		}
		return
	}
	h := hay
	if m.IgnoreCase {
		h = bytes.ToLower(hay)
	}
	for i, p := range m.lowered {
		if len(p) == 0 {
			continue
		}
		if at := bytes.Index(h, p); at >= 0 && (start < 0 || at < start) {
			idx, start, end = i, at, at+len(p)
		}
	}
	return
}

// MatchResult is the outcome of a wait.
type MatchResult struct {
	Index  int    // index of the matched pattern, -1 on timeout / close
	Before string // text between the start position and the match
	Match  string
	End    int64 // absolute offset just past the match (or current end on timeout)
	Closed bool  // the stream ended before a match
}

// Wait blocks until one of the patterns appears at or after position from.
func (b *textBuffer) Wait(ctx context.Context, from int64, m *Matcher) (MatchResult, error) {
	if err := m.prepare(); err != nil {
		return MatchResult{Index: -1}, err
	}
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.waiters[ch] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiters, ch)
		b.mu.Unlock()
	}()
	for {
		b.mu.Lock()
		if from < b.base {
			from = b.base
		}
		hay := b.data[from-b.base:]
		idx, s, e := m.find(hay)
		if idx >= 0 {
			res := MatchResult{Index: idx, Before: string(hay[:s]), Match: string(hay[s:e]), End: from + int64(e)}
			b.mu.Unlock()
			return res, nil
		}
		end := b.base + int64(len(b.data))
		closed := b.closed
		var before string
		if closed {
			before = string(hay)
		}
		b.mu.Unlock()
		if closed {
			return MatchResult{Index: -1, End: end, Closed: true, Before: before}, nil
		}
		select {
		case <-ctx.Done():
			b.mu.Lock()
			if from < b.base {
				from = b.base
			}
			before = string(b.data[from-b.base:])
			end = b.base + int64(len(b.data))
			b.mu.Unlock()
			return MatchResult{Index: -1, End: end, Before: before}, ctx.Err()
		case <-ch:
		}
	}
}

// Slice returns the text in [from, to).
func (b *textBuffer) Slice(from, to int64) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if from < b.base {
		from = b.base
	}
	end := b.base + int64(len(b.data))
	if to > end {
		to = end
	}
	if to <= from {
		return ""
	}
	return string(b.data[from-b.base : to-b.base])
}
