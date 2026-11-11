package keys

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Reference output of "ssh-keygen -lv" and "ssh-keygen -E md5 -l" (OpenSSH
// 9.6) for the public keys under testdata.
var fingerprintVectors = []struct {
	file, sha256, md5, typ string
	bits                   int
	art                    string
}{
	{
		file:   "ed25519.pub",
		sha256: "SHA256:ewf9bkaU0pKemQyUu+UwFyciaPsdyVueAPXKOfcHWnc",
		md5:    "MD5:3b:ba:37:69:3e:32:1d:e6:28:49:d6:a1:05:a1:37:1d",
		typ:    "ED25519", bits: 256,
		art: `+--[ED25519 256]--+
|       . ...     |
|      o o +.o .  |
|     . . = +.* . |
|      .  .X+B +  |
|       .S.*^oOo E|
|        ..++X=.o.|
|        . . o.o .|
|         . . .o. |
|             o.  |
+----[SHA256]-----+
`,
	},
	{
		file:   "rsa.pub",
		sha256: "SHA256:3hjemvNLM1MaZs31wHy7ZjWIQMxnzj3GAemfm5fc+cA",
		md5:    "MD5:02:80:21:8b:f8:19:78:df:67:03:18:e3:59:5f:1d:1a",
		typ:    "RSA", bits: 2048,
		art: `+---[RSA 2048]----+
|        o. .o    |
|        .o + +   |
|         .* o * .|
|          .*.*.+.|
|        S +.*.ooo|
|       o B + +  +|
|        + O   E++|
|        .+ + oo*.|
|        ooo.  . o|
+----[SHA256]-----+
`,
	},
	{
		file:   "ecdsa521.pub",
		sha256: "SHA256:hemT2As6RJLv3ogRwFGaShHttJeIdCQANf1Lapd5PfY",
		md5:    "MD5:df:d1:88:cf:56:a0:42:b8:74:a6:a0:26:c7:05:f0:1f",
		typ:    "ECDSA", bits: 521,
		art: `+---[ECDSA 521]---+
|=*B+             |
|..**.    o       |
|o=B +.. o .      |
|oo B oo+ o       |
|. . +oo+S.       |
|   +o.=..o+      |
|  ..+. ... o     |
|   + +      E    |
|  . o .          |
+----[SHA256]-----+
`,
	},
	{
		file:   "dsa.pub",
		sha256: "SHA256:WME9QfVfEBfwUL6GmHUcQUtjn36jFv8jCuTeQdtMnP4",
		md5:    "MD5:a4:0f:aa:82:78:1c:5c:4b:8b:5c:9a:4a:d2:dc:63:1c",
		typ:    "DSA", bits: 1024,
		art: `+---[DSA 1024]----+
|       ..ooo.o*@+|
|        ..o  .Bo*|
|        .  . ..Bo|
|       o    = =.o|
|      . S. + * =o|
|        o . * = o|
|         o o * . |
|        . o o....|
|         . o. .Eo|
+----[SHA256]-----+
`,
	},
	{
		// A certificate is fingerprinted by the key it certifies, and
		// "[ED25519-CERT 256]" is too wide for the frame.
		file:   "ed25519-cert.pub",
		sha256: "SHA256:ewf9bkaU0pKemQyUu+UwFyciaPsdyVueAPXKOfcHWnc",
		md5:    "MD5:3b:ba:37:69:3e:32:1d:e6:28:49:d6:a1:05:a1:37:1d",
		typ:    "ED25519-CERT", bits: 256,
		art: `+-[ED25519-CERT]--+
|       . ...     |
|      o o +.o .  |
|     . . = +.* . |
|      .  .X+B +  |
|       .S.*^oOo E|
|        ..++X=.o.|
|        . . o.o .|
|         . . .o. |
|             o.  |
+----[SHA256]-----+
`,
	},
	{
		file:   "ecdsa384-cert.pub",
		sha256: "SHA256:xJLpNbg1FAppTbQFEs7Tco8IwULqjs4TOIJqH09mW3k",
		typ:    "ECDSA-CERT", bits: 384,
		art: `+[ECDSA-CERT 384]-+
|.o. +*+.+.       |
|o .+o+.X         |
|....= X B        |
|.  . * O o       |
|o.  . + S        |
|B.      .        |
|+o.. + o E       |
|+o  * o .        |
|.oo. o           |
+----[SHA256]-----+
`,
	},
}

func TestFingerprintVectors(t *testing.T) {
	for _, v := range fingerprintVectors {
		pub := mustParsePublic(t, fixture(t, v.file))
		if got := FingerprintSHA256(pub); got != v.sha256 {
			t.Errorf("%s: FingerprintSHA256 = %s, want %s", v.file, got, v.sha256)
		}
		if got := FingerprintMD5(pub); v.md5 != "" && got != v.md5 {
			t.Errorf("%s: FingerprintMD5 = %s, want %s", v.file, got, v.md5)
		}
		if name, bits := KeyTypeDisplay(pub); name != v.typ || bits != v.bits {
			t.Errorf("%s: KeyTypeDisplay = %s %d, want %s %d", v.file, name, bits, v.typ, v.bits)
		}
		if got := RandomArt(pub); got != v.art {
			t.Errorf("%s: RandomArt =\n%s\nwant\n%s", v.file, got, v.art)
		}
	}
}

func TestRandomArtFrame(t *testing.T) {
	digest := bytes.Repeat([]byte{0x1b}, 32)
	tests := []struct {
		name   string
		bits   int
		header string
	}{
		{"RSA", 16384, "+---[RSA 16384]---+"},
		{"ED25519", 256, "+--[ED25519 256]--+"},
		{"ECDSA-SK", 256, "+-[ECDSA-SK 256]--+"},
		{"ED25519-SK", 256, "+[ED25519-SK 256]-+"},      // 16 characters: fits exactly
		{"ECDSA-SK-CERT", 256, "+-[ECDSA-SK-CERT]-+"},   // 19 characters: size dropped
		{"ED25519-SK-CERT", 256, "+[ED25519-SK-CERT-+"}, // even without the size, cut by the C buffer
		{"SOMETHING-VERY-LONG-INDEED", 1, "+[SOMETHING-VERY--+"},
		{"", 0, "+------[ 0]-------+"},
	}
	for _, tc := range tests {
		art := randomArt(digest, tc.name, tc.bits, "SHA256")
		lines := strings.Split(art, "\n")
		if len(lines) != 12 || lines[11] != "" {
			t.Fatalf("%s: %d lines, want 11 newline-terminated ones:\n%s", tc.name, len(lines)-1, art)
		}
		for i, l := range lines[:11] {
			if len(l) != 19 {
				t.Errorf("%s: line %d is %d wide: %q", tc.name, i, len(l), l)
			}
		}
		if lines[0] != tc.header {
			t.Errorf("%s: header %q, want %q", tc.name, lines[0], tc.header)
		}
		if lines[10] != "+----[SHA256]-----+" {
			t.Errorf("%s: footer %q", tc.name, lines[10])
		}
		// The end marker is always there; the start marker is hidden only
		// if the walk happens to end where it began.
		field := strings.Join(lines[1:10], "\n")
		if strings.Count(field, "E") != 1 || strings.Count(field, "S") > 1 {
			t.Errorf("%s: start or end marker wrong:\n%s", tc.name, art)
		}
	}

	// An all-zero digest walks the bishop up and left into the corner,
	// where it stays; the end marker wins over the saturated counter.
	corner := randomArt(make([]byte, 64), "X", 1, "MD5")
	want := "+------[X 1]------+\n" +
		"|E....            |\n" +
		"|     .           |\n" +
		"|      .          |\n" +
		"|       .         |\n" +
		"|        S        |\n" +
		strings.Repeat("|                 |\n", 4) +
		"+------[MD5]------+\n"
	if corner != want {
		t.Errorf("corner case art:\n%s\nwant\n%s", corner, want)
	}
	// An empty digest leaves the bishop on the start square, which is then
	// drawn as the end.
	empty := strings.Split(randomArt(nil, "X", 1, "SHA256"), "\n")
	if empty[5] != "|        E        |" || strings.ContainsAny(strings.Join(empty[1:10], ""), "S.") {
		t.Errorf("empty digest art:\n%s", strings.Join(empty, "\n"))
	}
}

func TestNilPublicKey(t *testing.T) {
	if FingerprintSHA256(nil) != "" || FingerprintMD5(nil) != "" || RandomArt(nil) != "" {
		t.Error("fingerprints of a nil key should be empty")
	}
	if name, bits := KeyTypeDisplay(nil); name != "" || bits != 0 {
		t.Errorf("KeyTypeDisplay(nil) = %q, %d", name, bits)
	}
	if MarshalAuthorizedKey(nil, "c") != nil || MarshalRFC4716(nil, "c") != nil {
		t.Error("marshalling a nil key should yield nil")
	}
	if IsCertificate(nil) || DescribeCertificate(nil) != nil {
		t.Error("nil is not a certificate")
	}

	// Hand-assembled certificates without a key must not panic either.
	var nilCert *ssh.Certificate
	for _, pub := range []ssh.PublicKey{nilCert, &ssh.Certificate{}, &ssh.Certificate{Key: mustParsePublic(t, fixture(t, "ed25519.pub"))}} {
		_ = FingerprintSHA256(pub)
		_ = FingerprintMD5(pub)
		_ = RandomArt(pub)
		_, _ = KeyTypeDisplay(pub)
		if MarshalAuthorizedKey(pub, "") != nil || MarshalRFC4716(pub, "") != nil {
			t.Errorf("an incomplete certificate was marshalled")
		}
	}
	if info := DescribeCertificate(&ssh.Certificate{}); info == nil || info.SignatureValid {
		t.Errorf("DescribeCertificate of an empty certificate: %+v", info)
	}
	var nilInfo *CertInfo
	if nilInfo.ValidAt(time.Now()) {
		t.Error("a nil CertInfo is never valid")
	}
}

func TestMarshalAuthorizedKey(t *testing.T) {
	pub := mustParsePublic(t, fixture(t, "ed25519.pub"))
	base := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGUFVktBF8awCUjtJGweVdbvqHOldbzly5f7lA+hc7cj"
	tests := []struct{ comment, want string }{
		{"", base + "\n"},
		{"user@host", base + " user@host\n"},
		{"  spaced out  ", base + " spaced out\n"},
		{"two words", base + " two words\n"},
		{"line\nbreak\r\nand nul\x00", base + " line break  and nul\n"},
		{"\n", base + "\n"},
	}
	for _, tc := range tests {
		got := string(MarshalAuthorizedKey(pub, tc.comment))
		if got != tc.want {
			t.Errorf("comment %q: got %q, want %q", tc.comment, got, tc.want)
		}
		back, comment, err := ParsePublicKey([]byte(got))
		if err != nil || !samePublic(back, pub) {
			t.Errorf("comment %q: does not parse back: %v", tc.comment, err)
		}
		if want := strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(tc.want, "\n"), base)); comment != want {
			t.Errorf("comment %q: parsed back as %q, want %q", tc.comment, comment, want)
		}
	}
	// Agrees with x/crypto for the comment-less form.
	if got := MarshalAuthorizedKey(pub, ""); !bytes.Equal(got, ssh.MarshalAuthorizedKey(pub)) {
		t.Errorf("differs from x/crypto: %q", got)
	}
}

func TestRFC4716(t *testing.T) {
	pubs := map[string]ssh.PublicKey{}
	for _, name := range []string{"ed25519", "rsa", "ecdsa521", "dsa"} {
		pubs[name] = mustParsePublic(t, fixture(t, name+".pub"))
	}
	comments := []string{
		"",
		"user@host",
		`quote " and back\slash`,
		`ends with a backslash \`,
		`"fully quoted"`,
		strings.Repeat("x", 61), // exactly 72 bytes on the header line
		strings.Repeat("x", 62), // one more: must fold
		strings.Repeat("long comment ", 30),
		"colon: space: everywhere: " + strings.Repeat("a: b ", 40),
		strings.Repeat("-", 200),
		"dashes ---- inside ---- " + strings.Repeat("-----", 20) + " end",
		"\u00fcml\u00e4ute and \u4e2d\u6587 " + strings.Repeat("\u4e2d", 100),
		strings.Repeat(`\"`, 80),
		strings.Repeat("y", 3000), // longer than the format allows
	}
	for name, pub := range pubs {
		for _, comment := range comments {
			out := MarshalRFC4716(pub, comment)
			lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
			if lines[0] != "---- BEGIN SSH2 PUBLIC KEY ----" || lines[len(lines)-1] != "---- END SSH2 PUBLIC KEY ----" {
				t.Fatalf("%s: bad framing:\n%s", name, out)
			}
			inHeader := false
			for i, l := range lines[1 : len(lines)-1] {
				if len(l) > 72 {
					t.Errorf("%s comment %.20q: line %d is %d bytes long", name, comment, i+1, len(l))
				}
				if inHeader && (strings.Contains(l, ": ") || strings.HasPrefix(l, "----")) {
					t.Errorf("%s comment %.20q: continuation line %q would confuse ssh-keygen", name, comment, l)
				}
				isData := !inHeader && !strings.HasPrefix(l, "Comment: ")
				if isData && len(l) > 70 {
					t.Errorf("%s: key data line %d is %d wide, want at most 70", name, i+1, len(l))
				}
				inHeader = strings.HasSuffix(l, `\`) && !isData
			}
			if (comment == "") != !strings.Contains(string(out), "Comment:") {
				t.Errorf("%s comment %q: Comment header presence is wrong", name, comment)
			}

			back, gotComment, err := ParsePublicKey(out)
			if err != nil {
				t.Fatalf("%s comment %.20q: ParsePublicKey: %v\n%s", name, comment, err, out)
			}
			if !samePublic(back, pub) {
				t.Errorf("%s comment %.20q: key changed", name, comment)
			}
			want := strings.TrimSpace(comment) // surrounding blanks are dropped
			if len(comment) > 1024 {
				// Truncated to the 1024 byte header value limit.
				if !strings.HasPrefix(comment, gotComment) || len(gotComment) < 1000 || len(gotComment) > 1022 {
					t.Errorf("%s: long comment truncated to %d bytes", name, len(gotComment))
				}
				continue
			}
			if gotComment != want {
				t.Errorf("%s: comment %q came back as %q", name, want, gotComment)
			}
		}
	}

	// The key data is wrapped at exactly 70 columns, as ssh-keygen does.
	out := string(MarshalRFC4716(pubs["rsa"], ""))
	if want := string(fixture(t, "rsa.rfc4716")); stripComment(out) != stripComment(want) {
		t.Errorf("RSA export differs from ssh-keygen -e:\n%s\nwant\n%s", out, want)
	}
}

// stripComment removes the Comment header from an RFC 4716 block.
func stripComment(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(l, "Comment:") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestParsePublicKeyVariants(t *testing.T) {
	ed := strings.TrimSpace(string(fixture(t, "ed25519.pub")))
	dsa := strings.TrimSpace(string(fixture(t, "dsa.pub")))
	edPub := mustParsePublic(t, []byte(ed))
	dsaPub := mustParsePublic(t, []byte(dsa))
	edKey := keyFields(ed)

	rfcNoQuotes := "---- BEGIN SSH2 PUBLIC KEY ----\r\nSubject: someone\r\nComment: no quotes here\r\nx-private: 1\r\n" +
		strings.Fields(ed)[1] + "\r\n---- END SSH2 PUBLIC KEY ----\r\n"
	rfcFolded := "junk before\n---- BEGIN SSH2 PUBLIC KEY ----\nComment: \"folded \\\nacross \\\nlines\"\n" +
		strings.Fields(ed)[1][:30] + "\n" + strings.Fields(ed)[1][30:] + "\n---- END SSH2 PUBLIC KEY ----\ntrailer\n"

	tests := []struct {
		name, input, comment string
		want                 ssh.PublicKey
	}{
		{"plain", ed, "ed25519@hawser-test", edPub},
		{"no comment", edKey, "", edPub},
		{"CRLF", ed + "\r\n", "ed25519@hawser-test", edPub},
		{"BOM", "\xef\xbb\xbf" + ed + "\n", "ed25519@hawser-test", edPub},
		{"leading blank and comment lines", "\n# my keys\n\n" + ed + "\n", "ed25519@hawser-test", edPub},
		{"options", `command="/bin/true",no-pty ` + edKey + " with options\n", "with options", edPub},
		{"first of several", ed + "\n" + dsa + "\n", "ed25519@hawser-test", edPub},
		{"dsa", dsa, "dsa@hawser-test", dsaPub},
		{"dsa with options", `no-pty,from="10.*" ` + dsa + "\n", "dsa@hawser-test", dsaPub},
		{"dsa after comment line", "# legacy\n" + keyFields(dsa) + "\n", "", dsaPub},
		{"rfc4716 unquoted comment, CRLF, extra headers", rfcNoQuotes, "no quotes here", edPub},
		{"rfc4716 folded comment and surrounding text", rfcFolded, "folded across lines", edPub},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pub, comment, err := ParsePublicKey([]byte(tc.input))
			if err != nil {
				t.Fatalf("ParsePublicKey: %v", err)
			}
			if !samePublic(pub, tc.want) || comment != tc.comment {
				t.Errorf("got comment %q (want %q), same key: %v", comment, tc.comment, samePublic(pub, tc.want))
			}
		})
	}

	bad := []struct {
		name, input string
		target      error
	}{
		{"empty", "", ErrUnknownFormat},
		{"whitespace", " \n\t\r\n", ErrUnknownFormat},
		{"garbage", "this is not a key", ErrUnknownFormat},
		{"only a comment", "# nothing here\n", ErrUnknownFormat},
		{"bad base64", "ssh-ed25519 !!!! c", ErrUnknownFormat},
		{"truncated blob", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGUFVktBF8aw c", ErrUnknownFormat},
		{"dsa with bad blob", "ssh-dss AAAAB3NzaC1kc3MAAAAB c", ErrMalformed},
		{"rfc4716 without end", "---- BEGIN SSH2 PUBLIC KEY ----\nAAAA\n", ErrMalformed},
		{"rfc4716 bad base64", "---- BEGIN SSH2 PUBLIC KEY ----\n!!!!\n---- END SSH2 PUBLIC KEY ----\n", ErrMalformed},
		{"rfc4716 empty", "---- BEGIN SSH2 PUBLIC KEY ----\n---- END SSH2 PUBLIC KEY ----\n", ErrMalformed},
		{"ssh.com private key", "---- BEGIN SSH2 ENCRYPTED PRIVATE KEY ----\nAAAA\n---- END SSH2 ENCRYPTED PRIVATE KEY ----\n", ErrUnsupported},
		{"certificate PEM", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", ErrUnknownFormat},
		{"bad PKIX", "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n", ErrMalformed},
		{"bad PKCS#1", "-----BEGIN RSA PUBLIC KEY-----\nAAAA\n-----END RSA PUBLIC KEY-----\n", ErrMalformed},
		{"encrypted PEM private key", string(fixture(t, "rsa.pem-enc")), ErrPassphraseRequired},
		{"encrypted PKCS#8 private key", string(fixture(t, "rsa.p8-enc")), ErrPassphraseRequired},
	}
	for _, tc := range bad {
		t.Run("bad/"+tc.name, func(t *testing.T) {
			pub, _, err := ParsePublicKey([]byte(tc.input))
			if err == nil {
				t.Fatalf("parsed %q as a %s key", tc.input, pub.Type())
			}
			if !errors.Is(err, tc.target) {
				t.Errorf("error %v, want %v", err, tc.target)
			}
		})
	}
}

func TestHostKeyAlgorithmDisplay(t *testing.T) {
	tests := map[string]string{
		"ssh-rsa":                                     "RSA",
		"rsa-sha2-256":                                "RSA",
		"rsa-sha2-512":                                "RSA",
		"ssh-dss":                                     "DSA",
		"ecdsa-sha2-nistp256":                         "ECDSA",
		"ecdsa-sha2-nistp384":                         "ECDSA",
		"ecdsa-sha2-nistp521":                         "ECDSA",
		"ssh-ed25519":                                 "ED25519",
		"ssh-ed448":                                   "ED448",
		"sk-ecdsa-sha2-nistp256@openssh.com":          "ECDSA-SK",
		"sk-ssh-ed25519@openssh.com":                  "ED25519-SK",
		"ssh-rsa-cert-v01@openssh.com":                "RSA-CERT",
		"rsa-sha2-512-cert-v01@openssh.com":           "RSA-CERT",
		"ssh-dss-cert-v01@openssh.com":                "DSA-CERT",
		"ecdsa-sha2-nistp384-cert-v01@openssh.com":    "ECDSA-CERT",
		"ssh-ed25519-cert-v01@openssh.com":            "ED25519-CERT",
		"sk-ssh-ed25519-cert-v01@openssh.com":         "ED25519-SK-CERT",
		"sk-ecdsa-sha2-nistp256-cert-v01@openssh.com": "ECDSA-SK-CERT",
		"ssh-mldsa-65":                                "ML-DSA-65",
		"ssh-mldsa-65-cert-v01@golang.org":            "ML-DSA-65-CERT",
		"ssh-xmss@openssh.com":                        "XMSS",
		"":                                            "",
		"something-new@example.com":                   "something-new@example.com",
		"unknown-cert-v01@openssh.com":                "unknown-cert-v01@openssh.com",
		"SSH-RSA":                                     "SSH-RSA", // names are case-sensitive
	}
	for algo, want := range tests {
		if got := HostKeyAlgorithmDisplay(algo); got != want {
			t.Errorf("HostKeyAlgorithmDisplay(%q) = %q, want %q", algo, got, want)
		}
	}
	// Consistent with KeyTypeDisplay for real keys.
	for _, s := range quickSpecs {
		pub := mustPublic(t, testKey(t, s))
		name, bits := KeyTypeDisplay(pub)
		if name != HostKeyAlgorithmDisplay(pub.Type()) || bits != s.bits {
			t.Errorf("%v: KeyTypeDisplay = %s %d", s, name, bits)
		}
	}
}
