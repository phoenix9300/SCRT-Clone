package vbs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readScript(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestE2ELogin(t *testing.T) {
	in, crt, buf := newHost()
	crt.meths["arguments"] = func(a []Value) (Value, error) {
		if len(a) == 0 {
			return NewArray("admin", "secret"), nil
		}
		return []string{"admin", "secret"}[ToInt(a[0])], nil
	}
	crt.screen.responses["ssh admin@10.0.0.1\r"] = "Password: "
	crt.screen.responses["secret\r"] = "Last login: today\r\nadmin@router:~$ "
	crt.screen.responses["uptime\r"] = " 10:00 up 3 days\r"
	if err := in.Run(context.Background(), readScript(t, "login.vbs"), "login.vbs"); err != nil {
		t.Fatal(err)
	}
	if got, want := lines(buf), "logged in as admin\nuptime: 10:00 up 3 days"; got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	wantSent := []string{"ssh admin@10.0.0.1\r", "secret\r", "uptime\r"}
	if strings.Join(crt.screen.sent, "|") != strings.Join(wantSent, "|") {
		t.Errorf("sent %q, want %q", crt.screen.sent, wantSent)
	}
	if v, _ := crt.screen.props["synchronous"]; v != true {
		t.Errorf("Synchronous = %v", v)
	}
}

func TestE2ELoginDenied(t *testing.T) {
	in, crt, buf := newHost()
	crt.meths["arguments"] = func(a []Value) (Value, error) { return "x", nil }
	crt.screen.responses["ssh x@10.0.0.1\r"] = "Permission denied\r\n"
	if err := in.Run(context.Background(), readScript(t, "login.vbs"), "login.vbs"); err != nil {
		t.Fatal(err)
	}
	if lines(buf) != "" || len(crt.screen.sent) != 1 {
		t.Errorf("output %q sent %q", lines(buf), crt.screen.sent)
	}
	var sawDialog bool
	for _, c := range crt.rec.calls {
		if strings.HasPrefix(c, `GET crt.dialog.messagebox("no password prompt")`) {
			sawDialog = true
		}
	}
	if !sawDialog {
		t.Errorf("dialog not shown: %q", crt.rec.calls)
	}
}

func TestE2EHostList(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "hosts.txt")
	content := "# inventory\r\n\r\nalpha, core\r\n  beta , edge  \r\n\r\ngamma\r\ndelta,core\r\n"
	if err := os.WriteFile(list, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	in, crt, buf := newHost()
	in.SetGlobal("hostsFile", vbsPath(list))
	crt.screen.responses["ping alpha\r"] = "alpha is alive\r\n"
	crt.screen.responses["ping beta\r"] = "beta unreachable\r\n"
	crt.screen.responses["ping gamma\r"] = "gamma is alive\r\n"
	// delta never answers.
	if err := in.Run(context.Background(), readScript(t, "hosts.vbs"), "hosts.vbs"); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"alpha (core): up",
		"beta (edge): DOWN",
		"gamma (default): up",
		"delta (core): timeout",
		"summary: 2 up, 2 down of 4",
	}, "\n")
	if got := lines(buf); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	if got := strings.Join(crt.screen.sent, ""); got != "ping alpha\rping beta\rping gamma\rping delta\r" {
		t.Errorf("sent %q", got)
	}
}

func TestE2EParseWithClass(t *testing.T) {
	in, crt, buf := newHost()
	crt.screen.responses["show interfaces\r"] = "Interface Status Address\r\n" +
		"eth0 UP 10.0.0.1/24\r\n" +
		"eth1 DOWN 192.168.1.5/16\r\n" +
		"garbage line\r\n" +
		"wlan0 UP 172.16.0.9/12\r\n" +
		"router#"
	if err := in.Run(context.Background(), readScript(t, "parse.vbs"), "parse.vbs"); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"found 3 interfaces",
		"eth0 -> UP 10.0.0.1/24",
		"eth1 -> DOWN 192.168.1.5/16",
		"wlan0 -> UP 172.16.0.9/12",
		"lo9 -> ?",
	}, "\n")
	if got := lines(buf); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
}
