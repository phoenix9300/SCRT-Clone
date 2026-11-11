package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode/utf16"
)

func byPath(r Result) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, it := range r.Items {
		m[it.Path] = it.Overrides
	}
	return m
}

func TestOpenSSHConfig(t *testing.T) {
	r, _ := OpenSSHConfig([]byte(`
Host *
  ServerAliveInterval 5
Host jump
  HostName 10.0.0.1
  User ops
Host web db *.wild
  HostName=10.0.0.2
  User admin
  Port 2222
  IdentityFile ~/.ssh/id_ed25519
  ProxyJump jump
  ForwardAgent yes
  ForwardX11 no
  LocalForward 8080 localhost:80
  RemoteForward 127.0.0.1:9000 app:9000
  DynamicForward 1080
  ServerAliveInterval 30
`))
	m := byPath(r)
	if len(m) != 3 {
		t.Fatalf("got %d items: %v", len(m), m)
	}
	w := m["web"]
	if w["host"] != "10.0.0.2" || w["username"] != "admin" || w["port"] != 2222 || w["firewall"] != "session:jump" {
		t.Errorf("web: %v", w)
	}
	ssh := w["ssh"].(map[string]any)
	if ssh["agentForwarding"] != true || ssh["x11Forwarding"] != nil || ssh["keepAliveInterval"] != 30 {
		t.Errorf("ssh: %v", ssh)
	}
	if !reflect.DeepEqual(ssh["identityFiles"], []string{"~/.ssh/id_ed25519"}) {
		t.Errorf("identity: %v", ssh["identityFiles"])
	}
	f := ssh["forwards"].([]map[string]any)
	if len(f) != 3 || f[0]["type"] != "local" || f[0]["bindPort"] != 8080 || f[0]["destHost"] != "localhost" || f[1]["bindAddr"] != "127.0.0.1" || f[2]["type"] != "dynamic" {
		t.Errorf("forwards: %v", f)
	}
	if m["jump"]["host"] != "10.0.0.1" {
		t.Errorf("jump: %v", m["jump"])
	}
}

func TestPuTTYDir(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "my%20server"), []byte("HostName=h.example\nPortNumber=2200\nUserName=bob\nProtocol=ssh\nPublicKeyFile=/k.ppk\n"), 0o600)
	os.WriteFile(filepath.Join(d, "tty"), []byte("Protocol=serial\nSerialLine=/dev/ttyS0\nSerialSpeed=115200\n"), 0o600)
	os.WriteFile(filepath.Join(d, "Default%20Settings"), []byte("HostName=x\n"), 0o600)
	r, err := PuTTY(d)
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(r)
	if len(m) != 2 {
		t.Fatalf("%v", m)
	}
	s := m["my server"]
	if s["host"] != "h.example" || s["port"] != 2200 || s["username"] != "bob" || s["protocol"] != "ssh2" {
		t.Errorf("%v", s)
	}
	if sr := m["tty"]["serial"].(map[string]any); sr["port"] != "/dev/ttyS0" || sr["baud"] != 115200 || m["tty"]["protocol"] != "serial" {
		t.Errorf("%v", m["tty"])
	}
}

func TestPuTTYReg(t *testing.T) {
	txt := "Windows Registry Editor Version 5.00\r\n\r\n" +
		"[HKEY_CURRENT_USER\\Software\\SimonTatham\\PuTTY\\Sessions\\Prod%20Box]\r\n" +
		"\"HostName\"=\"prod.example\"\r\n\"PortNumber\"=dword:00000016\r\n\"Protocol\"=\"telnet\"\r\n\"UserName\"=\"a\\\\b\"\r\n\r\n" +
		"[HKEY_CURRENT_USER\\Software\\SimonTatham\\PuTTY\\Sessions\\Default%20Settings]\r\n\"HostName\"=\"\"\r\n"
	u := utf16.Encode([]rune(txt))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	for name, in := range map[string][]byte{"utf16": b, "utf8": []byte(txt)} {
		r, _ := PuTTYReg(in)
		if len(r.Items) != 1 {
			t.Fatalf("%s: %v", name, r.Items)
		}
		o := r.Items[0].Overrides
		if r.Items[0].Path != "Prod Box" || o["host"] != "prod.example" || o["port"] != 22 || o["protocol"] != "telnet" || o["username"] != `a\b` {
			t.Errorf("%s: %v %v", name, r.Items[0].Path, o)
		}
	}
}

func TestCSV(t *testing.T) {
	for _, sep := range []string{",", ";", "\t"} {
		in := "Name" + sep + "Folder" + sep + "Protocol" + sep + "Hostname" + sep + "Port" + sep + "Username" + sep + "Description" + sep + "Emulation\n" +
			"a" + sep + "Prod/EU" + sep + "SSH2" + sep + "1.2.3.4" + sep + "22" + sep + "root" + sep + "d" + sep + "VT100\n" +
			"" + sep + "" + sep + "telnet" + sep + "sw1" + sep + "" + sep + "" + sep + "" + sep + "\n"
		r, err := CSV([]byte(in))
		if err != nil || len(r.Items) != 2 {
			t.Fatalf("%q: %v %v", sep, r, err)
		}
		if r.Items[0].Path != "Prod/EU/a" || r.Items[0].Overrides["port"] != 22 || r.Items[0].Overrides["terminal"].(map[string]any)["emulation"] != "vt100" {
			t.Errorf("%q: %v", sep, r.Items[0])
		}
		if r.Items[1].Path != "sw1" || r.Items[1].Overrides["protocol"] != "telnet" {
			t.Errorf("%q: %v", sep, r.Items[1])
		}
	}
	if _, err := CSV([]byte("foo,bar\n1,2\n")); err == nil {
		t.Error("expected header error")
	}
}

func TestHawser(t *testing.T) {
	r, err := Hawser([]byte(`{"version":1,"sessions":[{"path":"a/b","overrides":{"host":"h"}},{"path":"c"}]}`))
	if err != nil || len(r.Items) != 2 || r.Items[0].Overrides["host"] != "h" || r.Items[1].Overrides == nil {
		t.Fatalf("%v %v", r, err)
	}
	if _, err := Hawser([]byte(`{"version":2}`)); err == nil {
		t.Error("expected version error")
	}
}

func TestCRTDir(t *testing.T) {
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "Prod", "EU"), 0o755)
	os.WriteFile(filepath.Join(d, "Default.ini"), []byte(`S:"Hostname"=zzz`), 0o600)
	os.WriteFile(filepath.Join(d, "Prod", "__FolderData__.ini"), []byte(`S:"Hostname"=zzz`), 0o600)
	os.WriteFile(filepath.Join(d, "Prod", "EU", "web 1.ini"), []byte("S:\"Hostname\"=10.0.0.1\r\nS:\"Username\"=admin\r\nS:\"Protocol Name\"=SSH2\r\nD:\"[SSH2] Port\"=00000016\r\nD:\"Port\"=000001bb\r\nS:\"Emulation\"=VT100\r\nS:\"Description\"=hi\r\n"), 0o600)
	os.WriteFile(filepath.Join(d, "tn.ini"), []byte("S:\"Hostname\"=h\nS:\"Protocol Name\"=Telnet/SSL\nD:\"Port\"=000003e0\n"), 0o600)
	r, err := CRT(d)
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(r)
	if len(m) != 2 {
		t.Fatalf("%v", m)
	}
	w := m["Prod/EU/web 1"]
	if w["host"] != "10.0.0.1" || w["port"] != 22 || w["username"] != "admin" || w["protocol"] != "ssh2" || w["description"] != "hi" {
		t.Errorf("%v", w)
	}
	if m["tn"]["protocol"] != "telnet-tls" || m["tn"]["port"] != 992 {
		t.Errorf("%v", m["tn"])
	}
}

func TestCRTXML(t *testing.T) {
	x := `<?xml version="1.0"?><VanDyke version="3.0"><key name="Sessions">
<key name="Default"><string name="Hostname">no</string></key>
<key name="Lab"><key name="__FolderData__"><string name="x">y</string></key>
 <key name="r1"><string name="Hostname">192.168.1.1</string><string name="Protocol Name">Telnet</string><dword name="Port">00000017</dword></key>
 <key name="Deep"><key name="s2"><string name="Hostname">h2</string><string name="Username">u</string><string name="Protocol Name">SSH2</string><dword name="[SSH2] Port">000008ae</dword></key></key>
</key>
<key name="top"><string name="Hostname">t</string></key>
</key></VanDyke>`
	p := filepath.Join(t.TempDir(), "x.xml")
	os.WriteFile(p, []byte(x), 0o600)
	r, err := CRT(p)
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(r)
	if len(m) != 3 || m["Lab/r1"]["protocol"] != "telnet" || m["Lab/r1"]["port"] != 23 || m["Lab/Deep/s2"]["port"] != 2222 || m["top"]["host"] != "t" {
		t.Errorf("%v", m)
	}
}
