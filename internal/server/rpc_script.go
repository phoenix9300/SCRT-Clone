package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/phoenix9300/SCRT-Clone/internal/conn"
	"github.com/phoenix9300/SCRT-Clone/internal/script"
)

// scriptUI implements script.UI by asking the window.
type scriptUI struct{ s *Server }

func (u scriptUI) client() (*client, error) {
	c := u.s.anyClient()
	if c == nil {
		return nil, errors.New("no window is attached")
	}
	return c, nil
}

func (u scriptUI) ask(ctx context.Context, name string, data any, out any) error {
	c, err := u.client()
	if err != nil {
		return err
	}
	raw, err := c.request(ctx, name, data)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 && string(raw) != "null" {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (u scriptUI) ui(ctx context.Context, op string, data map[string]any, out any) error {
	if data == nil {
		data = map[string]any{}
	}
	data["op"] = op
	return u.ask(ctx, "script.ui", data, out)
}

func (u scriptUI) MessageBox(ctx context.Context, msg, title string, buttons int) (int, error) {
	var r struct {
		Value int `json:"value"`
	}
	err := u.ask(ctx, "script.dialog", map[string]any{"kind": "messageBox", "message": msg, "title": title, "buttons": buttons}, &r)
	return r.Value, err
}

func (u scriptUI) Prompt(ctx context.Context, msg, title, def string, password bool) (string, bool, error) {
	var r struct {
		Value string `json:"value"`
		OK    bool   `json:"ok"`
	}
	err := u.ask(ctx, "script.dialog", map[string]any{"kind": "prompt", "message": msg, "title": title, "default": def, "password": password}, &r)
	return r.Value, r.OK, err
}

func (u scriptUI) FileOpen(ctx context.Context, title, button, defName, filter string) (string, error) {
	var r struct {
		Path string `json:"path"`
	}
	err := u.ask(ctx, "script.dialog", map[string]any{"kind": "fileOpen", "title": title, "button": button, "defaultName": defName, "filter": filter}, &r)
	return r.Path, err
}

func (u scriptUI) ScreenText(ctx context.Context, id uint32, r1, c1, r2, c2 int) (string, error) {
	var r struct {
		Text string `json:"text"`
	}
	err := u.ask(ctx, "script.screen", map[string]any{"conn": id, "op": "text", "r1": r1, "c1": c1, "r2": r2, "c2": c2}, &r)
	return r.Text, err
}

func (u scriptUI) ScreenInfo(ctx context.Context, id uint32) (rows, cols, curRow, curCol int, selection string, err error) {
	var r struct {
		Rows      int    `json:"rows"`
		Cols      int    `json:"cols"`
		CurRow    int    `json:"curRow"`
		CurCol    int    `json:"curCol"`
		Selection string `json:"selection"`
	}
	err = u.ask(ctx, "script.screen", map[string]any{"conn": id, "op": "info"}, &r)
	return r.Rows, r.Cols, r.CurRow, r.CurCol, r.Selection, err
}

func (u scriptUI) Tabs(ctx context.Context) ([]uint32, uint32, error) {
	var r struct {
		Tabs   []uint32 `json:"tabs"`
		Active uint32   `json:"active"`
	}
	err := u.ui(ctx, "tabs", nil, &r)
	return r.Tabs, r.Active, err
}

func (u scriptUI) ActivateTab(ctx context.Context, id uint32) error {
	return u.ui(ctx, "activate", map[string]any{"conn": id}, nil)
}

func (u scriptUI) SetCaption(ctx context.Context, id uint32, caption string) error {
	return u.ui(ctx, "setCaption", map[string]any{"conn": id, "caption": caption}, nil)
}

func (u scriptUI) GetCaption(ctx context.Context, id uint32) (string, error) {
	var r struct {
		Caption string `json:"caption"`
	}
	err := u.ui(ctx, "getCaption", map[string]any{"conn": id}, &r)
	return r.Caption, err
}

func (u scriptUI) Clipboard(ctx context.Context) (string, error) {
	var r struct {
		Text string `json:"text"`
	}
	err := u.ui(ctx, "clipboardGet", nil, &r)
	return r.Text, err
}

func (u scriptUI) SetClipboard(ctx context.Context, text string) error {
	return u.ui(ctx, "clipboardSet", map[string]any{"text": text}, nil)
}

func (u scriptUI) Status(text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2e9)
	defer cancel()
	_ = u.ui(ctx, "status", map[string]any{"text": text}, nil)
}

func (u scriptUI) OpenTab(ctx context.Context, req conn.OpenRequest) (*conn.Conn, error) {
	c, err := u.client()
	if err != nil {
		return nil, err
	}
	cn, err := u.s.Mgr.Open(req, c)
	if err != nil {
		return nil, err
	}
	c.Event("conn.opened", cn.Info(true))
	return cn, nil
}

func (u scriptUI) OpenSFTP(ctx context.Context, id uint32) error {
	return u.ui(ctx, "openSftp", map[string]any{"conn": id}, nil)
}

// registerScripts wires the scripting host (crt API) into the RPC layer.
func (s *Server) registerScripts() {
	host := &script.Host{
		Mgr: s.Mgr,
		UI:  scriptUI{s},
		PythonFunc: func() string {
			return s.Store.GlobalString("pythonPath", "")
		},
		OnState: func(ri script.RunInfo) { s.broadcast("script.state", ri) },
	}
	s.ext.onClose(host.StopAll)

	s.Mgr.OnConnected = func(c *conn.Conn) {
		lo := c.Options().Logon
		if strings.TrimSpace(lo.Script) == "" {
			return
		}
		_, _ = host.Start(script.RunRequest{Conn: c.ID, Path: lo.Script, Args: strings.Fields(lo.ScriptArgs)})
	}

	s.on("script.run", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[script.RunRequest](raw)
		if err != nil {
			return nil, err
		}
		r, err := host.Start(p)
		if err != nil {
			return nil, err
		}
		return r.Info(), nil
	})
	s.on("script.stop", func(c *client, raw json.RawMessage) (any, error) {
		p, err := decode[struct {
			ID   string `json:"id"`
			Conn uint32 `json:"conn"`
		}](raw)
		if err != nil {
			return nil, err
		}
		if p.ID != "" {
			host.Stop(p.ID)
		}
		if p.Conn != 0 {
			host.StopConn(p.Conn)
		}
		return nil, nil
	})
	s.on("script.list", func(c *client, raw json.RawMessage) (any, error) {
		return host.Runs(), nil
	})
}
