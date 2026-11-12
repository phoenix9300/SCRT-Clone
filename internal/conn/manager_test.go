package conn

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phoenix9300/SCRT-Clone/internal/store"
)

// testSink records output and answers prompts from a script.
type testSink struct {
	mu      sync.Mutex
	out     map[uint32][]byte
	events  []string
	answers func(req PromptRequest) PromptResponse
	mgr     *Manager
}

func (s *testSink) Data(id uint32, p []byte) {
	s.mu.Lock()
	if s.out == nil {
		s.out = map[uint32][]byte{}
	}
	s.out[id] = append(s.out[id], p...)
	s.mu.Unlock()
	if c := s.mgr.Get(id); c != nil {
		c.Ack(int64(len(p)))
	}
}

func (s *testSink) Event(name string, payload any) {
	s.mu.Lock()
	s.events = append(s.events, name)
	s.mu.Unlock()
}

func (s *testSink) Prompt(ctx context.Context, id uint32, req PromptRequest) (PromptResponse, error) {
	if s.answers != nil {
		return s.answers(req), nil
	}
	return PromptResponse{}, ErrCancelled
}

func (s *testSink) text(id uint32) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.out[id])
}

func newTestManager(t *testing.T) (*Manager, *testSink) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st)
	sink := &testSink{mgr: m}
	m.AddSink(sink)
	t.Cleanup(m.CloseAll)
	return m, sink
}

func waitState(t *testing.T, c *Conn, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c.State() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("state = %s (%s), want %s", c.State(), c.Info(false).Message, want)
}

func expect(t *testing.T, c *Conn, from int64, pattern string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := c.WaitFor(ctx, from, &Matcher{Patterns: []string{pattern}})
	if err != nil || res.Index < 0 {
		t.Fatalf("did not see %q (err=%v); output so far:\n%s", pattern, err, c.TextSlice(0, c.TextEnd()))
	}
	return res.End
}

func TestLocalShell(t *testing.T) {
	m, sink := newTestManager(t)
	c, err := m.Open(OpenRequest{Overrides: map[string]any{"protocol": "local", "local": map[string]any{"shell": "/bin/sh"}}, Cols: 100, Rows: 30}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	c.Input([]byte("echo marker-$((6*7)); stty size\n"))
	pos := expect(t, c, 0, "marker-42")
	expect(t, c, pos, "30 100")
	c.Resize(120, 40)
	c.Input([]byte("stty size\n"))
	expect(t, c, pos, "40 120")
	if !strings.Contains(sink.text(c.ID), "marker-42") {
		t.Fatal("sink did not receive the output")
	}
	c.Input([]byte("exit\n"))
	waitState(t, c, StateDisconnected)
}

func TestLogonActionsAndAntiIdle(t *testing.T) {
	m, sink := newTestManager(t)
	ov := map[string]any{
		"protocol": "local", "local": map[string]any{"shell": "/bin/sh", "env": map[string]any{"PS1": "$ "}},
		"logon": map[string]any{"enabled": true, "timeout": 5, "actions": []any{
			map[string]any{"expect": "$ ", "send": "echo logon-$((3+4))\\r"},
			map[string]any{"expect": "logon-7", "send": "echo second-step\\r"},
		}},
		"terminal": map[string]any{"antiIdle": map[string]any{"enabled": true, "interval": 1, "text": "echo idle-$((1+1))\\r"}},
	}
	c, err := m.Open(OpenRequest{Overrides: ov, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	pos := expect(t, c, 0, "logon-7")
	pos = expect(t, c, pos, "second-step\r\n")
	// with no keyboard input the anti-idle string is sent after the interval
	expect(t, c, pos, "idle-2")
}

func TestRecorderGeneratesScripts(t *testing.T) {
	m, sink := newTestManager(t)
	c, err := m.Open(OpenRequest{Overrides: map[string]any{"protocol": "local", "local": map[string]any{"shell": "/bin/sh", "env": map[string]any{"PS1": "$ "}}}, Cols: 80, Rows: 24}, sink)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, c, StateConnected)
	expect(t, c, 0, "$ ")
	c.StartRecording()
	c.Input([]byte("echo first\r"))
	pos := expect(t, c, 0, "first\r\n")
	expect(t, c, pos, "$ ")
	c.Input([]byte("echo \"hi\"\r"))
	expect(t, c, pos, "hi\r\n")
	steps := c.StopRecording()
	// the first command is sent without waiting: its prompt predates the recording
	if len(steps) != 2 || steps[0].Expect != "" || steps[0].Send != "echo first\r" ||
		!strings.HasSuffix(steps[1].Expect, "$ ") || steps[1].Send != "echo \"hi\"\r" {
		t.Fatalf("recorded steps: %+v", steps)
	}
	py := GenerateScript(steps, "python")
	if !strings.Contains(py, `crt.Screen.WaitForString("$ ")`) || !strings.Contains(py, `crt.Screen.Send("echo \"hi\"\r")`) {
		t.Fatalf("python script:\n%s", py)
	}
	vb := GenerateScript(steps, "vbscript")
	if !strings.Contains(vb, `crt.Screen.Send "echo ""hi""" & vbCr`) {
		t.Fatalf("vbscript:\n%s", vb)
	}
}
