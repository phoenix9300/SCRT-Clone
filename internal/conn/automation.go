package conn

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// antiIdleLoop keeps an idle connection alive by sending a configured string
// (or a protocol-level NO-OP) after a period without keyboard input.
func (c *Conn) antiIdleLoop(t Transport, stop <-chan struct{}) {
	ai := c.Options().Terminal.AntiIdle
	if !ai.Enabled || ai.Interval <= 0 {
		return
	}
	interval := time.Duration(ai.Interval) * time.Second
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			last := time.Unix(0, c.lastInput.Load())
			if time.Since(last) < interval {
				continue
			}
			c.lastInput.Store(time.Now().UnixNano())
			if ai.Text != "" {
				_, _ = t.Write(Unescape(ai.Text))
			} else if ka, ok := t.(KeepAliver); ok {
				_ = ka.KeepAlive()
			} else {
				// no protocol NO-OP available (serial, raw, local): nothing safe to send
			}
		}
	}
}

// runLogonActions performs the session's expect/send pairs after connecting.
func (c *Conn) runLogonActions(stop <-chan struct{}) {
	lo := c.Options().Logon
	if !lo.Enabled || len(lo.Actions) == 0 {
		return
	}
	timeout := time.Duration(lo.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	pos := int64(0)
	for i, a := range lo.Actions {
		if a.Expect != "" {
			wctx, wcancel := context.WithTimeout(ctx, timeout)
			res, err := c.WaitFor(wctx, pos, &Matcher{Patterns: []string{a.Expect}})
			wcancel()
			if err != nil || res.Index < 0 {
				if ctx.Err() == nil {
					connPrompter{c}.Notice(fmt.Sprintf("\r\n[Logon actions: timed out waiting for %q]\r\n", a.Expect))
				}
				return
			}
			pos = res.End
		}
		send := a.Send
		if a.Hide && c.SessionPath != "" {
			if v, ok, err := c.mgr.Store.Vault.Get(store.SecretKey(c.SessionPath, fmt.Sprintf("logon:%d", i))); ok && err == nil {
				send = v
			}
		}
		if send != "" {
			c.SendString(send)
		}
	}
}

// ------------------------------------------------------------- recorder

// RecordedStep is one expect/send pair captured by the script recorder.
type RecordedStep struct {
	Expect string `json:"expect"`
	Send   string `json:"send"`
}

// Recorder captures what the user types together with the prompt that
// preceded it, so the interaction can be replayed as a script.
type Recorder struct {
	mu      sync.Mutex
	steps   []RecordedStep
	pending []byte
	prompt  string
	started time.Time
}

func (r *Recorder) input(c *Conn, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		r.prompt = lastPrompt(c)
	}
	for _, b := range data {
		r.pending = append(r.pending, b)
		if b == '\r' || b == '\n' {
			r.steps = append(r.steps, RecordedStep{Expect: r.prompt, Send: string(r.pending)})
			r.pending = r.pending[:0]
			r.prompt = ""
		}
	}
}

// lastPrompt returns the tail of the current output line (what the host
// printed just before the user started typing).
func lastPrompt(c *Conn) string {
	end := c.text.End()
	from := end - 512
	if from < 0 {
		from = 0
	}
	tail := c.text.Slice(from, end)
	if i := strings.LastIndexAny(tail, "\r\n"); i >= 0 {
		tail = tail[i+1:]
	}
	if len(tail) > 60 {
		tail = tail[len(tail)-60:]
	}
	return tail
}

// StartRecording begins capturing a script.
func (c *Conn) StartRecording() {
	c.mu.Lock()
	c.rec = &Recorder{started: time.Now()}
	c.mu.Unlock()
	c.mgr.Broadcast("conn.state", c.Info(false))
}

// StopRecording ends capture and returns the recorded steps.
func (c *Conn) StopRecording() []RecordedStep {
	c.mu.Lock()
	r := c.rec
	c.rec = nil
	c.mu.Unlock()
	c.mgr.Broadcast("conn.state", c.Info(false))
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	steps := append([]RecordedStep(nil), r.steps...)
	if len(r.pending) > 0 {
		steps = append(steps, RecordedStep{Expect: r.prompt, Send: string(r.pending)})
	}
	// The prompt in front of the first command was printed before recording
	// began; a replay starts at that same prompt and must not wait for it.
	if len(steps) > 0 {
		steps[0].Expect = ""
	}
	return steps
}

// GenerateScript renders recorded steps as a script in the given language
// ("python", "javascript" or "vbscript").
func GenerateScript(steps []RecordedStep, lang string) string {
	var sb strings.Builder
	switch lang {
	case "vbscript", "vbs":
		sb.WriteString("# $language = \"VBScript\"\n# $interface = \"1.0\"\n\n' Recorded by Hawser\n\nSub Main\n\tcrt.Screen.Synchronous = True\n")
		for _, s := range steps {
			if s.Expect != "" {
				fmt.Fprintf(&sb, "\tcrt.Screen.WaitForString %s\n", vbsQuote(s.Expect))
			}
			fmt.Fprintf(&sb, "\tcrt.Screen.Send %s\n", vbsQuote(s.Send))
		}
		sb.WriteString("End Sub\n")
	case "javascript", "js":
		sb.WriteString("// $language = \"JScript\"\n// $interface = \"1.0\"\n\n// Recorded by Hawser\n\nfunction main() {\n\tcrt.Screen.Synchronous = true;\n")
		for _, s := range steps {
			if s.Expect != "" {
				fmt.Fprintf(&sb, "\tcrt.Screen.WaitForString(%s);\n", cQuote(s.Expect))
			}
			fmt.Fprintf(&sb, "\tcrt.Screen.Send(%s);\n", cQuote(s.Send))
		}
		sb.WriteString("}\n\nmain();\n")
	default:
		sb.WriteString("# $language = \"Python\"\n# $interface = \"1.0\"\n\n# Recorded by Hawser\n\ndef main():\n\tcrt.Screen.Synchronous = True\n")
		for _, s := range steps {
			if s.Expect != "" {
				fmt.Fprintf(&sb, "\tcrt.Screen.WaitForString(%s)\n", cQuote(s.Expect))
			}
			fmt.Fprintf(&sb, "\tcrt.Screen.Send(%s)\n", cQuote(s.Send))
		}
		if len(steps) == 0 {
			sb.WriteString("\tpass\n")
		}
		sb.WriteString("\nmain()\n")
	}
	return sb.String()
}

func cQuote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\r':
			sb.WriteString(`\r`)
		case '\n':
			sb.WriteString(`\n`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&sb, `\x%02x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func vbsQuote(s string) string {
	var parts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, `"`+cur.String()+`"`)
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			cur.WriteString(`""`)
		case r == '\r':
			flush()
			parts = append(parts, "vbCr")
		case r == '\n':
			flush()
			parts = append(parts, "vbLf")
		case r == '\t':
			flush()
			parts = append(parts, "vbTab")
		case r < 0x20 || r == 0x7f:
			flush()
			parts = append(parts, fmt.Sprintf("Chr(%d)", r))
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " & ")
}
