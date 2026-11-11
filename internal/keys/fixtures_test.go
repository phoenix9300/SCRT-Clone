package keys

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The files under testdata were written by OpenSSH 9.6 (ssh-keygen),
// PuTTY 0.81 (puttygen) and OpenSSL 3.0, never by this package, so these
// tests check interoperability even on machines where the tools are not
// installed. File names are "<key>.<variant>":
//
//	<key>.pub                 the expected public key
//	<key>.ossh[-enc[-...]]    ssh-keygen, openssh-key-v1 (-Z cipher, -a rounds)
//	<key>.pem[-enc[-...]]     ssh-keygen -m PEM, or openssl -traditional
//	<key>.p8[-enc[-...]]      ssh-keygen -m PKCS8, or openssl pkcs8 -topk8
//	<key>.ppk2|ppk3[-enc...]  puttygen -O private [--ppk-param ...]
//	<key>.rfc4716|pkix|...    public key encodings
//
// Every variant containing "-enc" is protected with the passphrase
// "hawser-test". OpenSSH and PPK files carry the comment "<key>@hawser-test".

// fixtureKeys describes the key behind each fixture name prefix.
var fixtureKeys = map[string]keySpec{
	"rsa":         {RSA, 2048},
	"ecdsa256":    {ECDSA, 256},
	"ecdsa384":    {ECDSA, 384},
	"ecdsa521":    {ECDSA, 521},
	"ed25519":     {Ed25519, 256},
	"ed25519ossl": {Ed25519, 256},
	"dsa":         {DSA, 1024},
}

type privateFixture struct {
	name      string // file name
	base      string // key name
	variant   string
	format    Format
	encrypted bool
	comment   string // comment the file carries
}

// privateFixtures lists every private key file under testdata.
func privateFixtures(t testing.TB) []privateFixture {
	t.Helper()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var out []privateFixture
	for _, e := range entries {
		base, variant, ok := strings.Cut(e.Name(), ".")
		if !ok {
			continue
		}
		f := privateFixture{name: e.Name(), base: base, variant: variant, encrypted: strings.Contains(variant, "-enc")}
		switch {
		case strings.HasPrefix(variant, "ossh"):
			f.format, f.comment = FormatOpenSSH, base+"@hawser-test"
		case strings.HasPrefix(variant, "ppk"):
			f.format, f.comment = FormatPPK, base+"@hawser-test"
		case strings.HasPrefix(variant, "pem"), strings.HasPrefix(variant, "p8"):
			f.format = FormatPEM
		default:
			continue
		}
		out = append(out, f)
	}
	if len(out) < 80 {
		t.Fatalf("found only %d private key fixtures; is testdata incomplete?", len(out))
	}
	return out
}

func TestFixturesParse(t *testing.T) {
	for _, f := range privateFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			data := fixture(t, f.name)
			spec := fixtureKeys[f.base]
			wantPub := mustParsePublic(t, fixture(t, f.base+".pub"))
			unsupported := strings.HasSuffix(f.variant, "argon2d")

			var pass []byte
			if f.encrypted {
				pass = []byte(fixturePass)

				_, err := ParsePrivateKey(data, nil)
				requireErr(t, err, ErrPassphraseRequired)
				_, err = ParsePrivateKey(data, []byte{})
				requireErr(t, err, ErrPassphraseRequired)
				if !unsupported {
					_, err = ParsePrivateKey(data, []byte("not the passphrase"))
					requireErr(t, err, ErrWrongPassphrase)
				}
			}

			key, err := ParsePrivateKey(data, pass)
			if unsupported {
				requireErr(t, err, ErrUnsupported)
				if !strings.Contains(err.Error(), "Argon2d") {
					t.Errorf("error does not name Argon2d: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePrivateKey: %v", err)
			}
			if !samePublic(mustPublic(t, key), wantPub) {
				t.Fatal("parsed key does not match the .pub file")
			}
			if key.Format != f.format {
				t.Errorf("Format = %q, want %q", key.Format, f.format)
			}
			if key.Comment != f.comment {
				t.Errorf("Comment = %q, want %q", key.Comment, f.comment)
			}
			if key.Type() != spec.typ || key.Bits() != spec.bits {
				t.Errorf("Type/Bits = %s/%d, want %s/%d", key.Type(), key.Bits(), spec.typ, spec.bits)
			}

			// An unencrypted key parses whatever passphrase is offered.
			if !f.encrypted {
				again, err := ParsePrivateKey(data, []byte("ignored"))
				if err != nil {
					t.Fatalf("with a superfluous passphrase: %v", err)
				}
				requireSameKey(t, again, key)
			}

			// The key must actually work.
			signer, err := key.SSHSigner()
			if err != nil {
				t.Fatalf("SSHSigner: %v", err)
			}
			msg := []byte("fixture " + f.name)
			sig, err := signer.Sign(nil, msg)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if err := wantPub.Verify(msg, sig); err != nil {
				t.Fatalf("signature does not verify against the .pub file: %v", err)
			}
		})
	}
}

func TestFixturesInspect(t *testing.T) {
	for _, f := range privateFixtures(t) {
		t.Run(f.name, func(t *testing.T) {
			data := fixture(t, f.name)
			spec := fixtureKeys[f.base]
			wantPub := mustParsePublic(t, fixture(t, f.base+".pub"))

			info, err := Inspect(data)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if info.Format != f.format || info.Encrypted != f.encrypted {
				t.Errorf("Format/Encrypted = %q/%v, want %q/%v", info.Format, info.Encrypted, f.format, f.encrypted)
			}
			if f.encrypted != (info.Cipher != "") || f.encrypted != (info.KDF != "") {
				t.Errorf("Cipher/KDF = %q/%q for encrypted=%v", info.Cipher, info.KDF, f.encrypted)
			}
			if info.Variant == "" {
				t.Error("Variant is empty")
			}

			// What can be known without the passphrase depends on the format.
			publicVisible := f.format != FormatPEM || !f.encrypted
			typeVisible := publicVisible || !strings.HasPrefix(f.variant, "p8")
			commentVisible := f.format == FormatPPK || (f.format == FormatOpenSSH && !f.encrypted)

			wantType, wantBits, wantComment := Type(""), 0, ""
			if typeVisible {
				wantType = spec.typ
			}
			if publicVisible {
				wantBits = spec.bits
			}
			if commentVisible {
				wantComment = f.comment
			}
			if info.Type != wantType || info.Bits != wantBits || info.Comment != wantComment {
				t.Errorf("Type/Bits/Comment = %q/%d/%q, want %q/%d/%q",
					info.Type, info.Bits, info.Comment, wantType, wantBits, wantComment)
			}
			if publicVisible != (info.PublicKey != nil) {
				t.Fatalf("PublicKey present = %v, want %v", info.PublicKey != nil, publicVisible)
			}
			if publicVisible && !samePublic(info.PublicKey, wantPub) {
				t.Error("PublicKey does not match the .pub file")
			}

			// ParsePublicKey sees the same public half.
			pub, comment, err := ParsePublicKey(data)
			if !publicVisible {
				requireErr(t, err, ErrPassphraseRequired)
				return
			}
			if err != nil {
				t.Fatalf("ParsePublicKey: %v", err)
			}
			if !samePublic(pub, wantPub) || comment != wantComment {
				t.Errorf("ParsePublicKey gave comment %q (want %q) or a different key", comment, wantComment)
			}
		})
	}
}

// TestFixturesInspectDetails pins the cipher and KDF names reported for a
// representative selection of files.
func TestFixturesInspectDetails(t *testing.T) {
	tests := []struct {
		name, variant, cipher, kdf string
	}{
		{"rsa.ossh", "openssh-key-v1", "", ""},
		{"rsa.ossh-enc", "openssh-key-v1", "aes256-ctr", "bcrypt"},
		{"ed25519.ossh-enc-chacha20poly1305", "openssh-key-v1", "chacha20-poly1305@openssh.com", "bcrypt"},
		{"ed25519.ossh-enc-aes256gcm", "openssh-key-v1", "aes256-gcm@openssh.com", "bcrypt"},
		{"rsa.pem", "PKCS#1", "", ""},
		{"rsa.pem-enc", "PKCS#1", "AES-128-CBC", "MD5"},
		{"rsa.pem-enc-3des", "PKCS#1", "DES-EDE3-CBC", "MD5"},
		{"ecdsa256.pem", "SEC1", "", ""},
		{"dsa.pem-enc", "OpenSSL DSA", "AES-128-CBC", "MD5"},
		{"rsa.p8", "PKCS#8", "", ""},
		{"rsa.p8-enc-scrypt", "PKCS#8", "AES-256-CBC", "scrypt"},
		{"rsa.p8-enc-3des-sha1", "PKCS#8", "DES-EDE3-CBC", "PBKDF2-HMAC-SHA1"},
		{"rsa.p8-enc-aes192-sha512", "PKCS#8", "AES-192-CBC", "PBKDF2-HMAC-SHA512"},
		{"rsa.p8-enc-aes128-sha384", "PKCS#8", "AES-128-CBC", "PBKDF2-HMAC-SHA384"},
		{"rsa.p8-enc-pbes1-md5", "PKCS#8", "DES-CBC", "PBKDF1-MD5"},
		{"rsa.p8-enc-pbes1-sha1", "PKCS#8", "DES-CBC", "PBKDF1-SHA1"},
		{"ed25519.ppk2", "PPK v2", "", ""},
		{"ed25519.ppk2-enc", "PPK v2", "aes256-cbc", "SHA-1"},
		{"ed25519.ppk3-enc", "PPK v3", "aes256-cbc", "Argon2id"},
		{"ed25519.ppk3-enc-argon2i", "PPK v3", "aes256-cbc", "Argon2i"},
		{"ed25519.ppk3-enc-argon2d", "PPK v3", "aes256-cbc", "Argon2d"},
	}
	for _, tc := range tests {
		info, err := Inspect(fixture(t, tc.name))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if info.Variant != tc.variant || info.Cipher != tc.cipher || info.KDF != tc.kdf {
			t.Errorf("%s: Variant/Cipher/KDF = %q/%q/%q, want %q/%q/%q",
				tc.name, info.Variant, info.Cipher, info.KDF, tc.variant, tc.cipher, tc.kdf)
		}
	}
}

func TestFixturesPublicEncodings(t *testing.T) {
	tests := []struct {
		name, base, comment string
	}{
		{"rsa.rfc4716", "rsa", "2048-bit RSA, converted by root@vm from OpenSSH"},
		{"dsa.rfc4716", "dsa", "1024-bit DSA, converted by root@vm from OpenSSH"},
		{"ed25519.rfc4716", "ed25519", "ed25519@hawser-test"},
		{"ecdsa521.rfc4716", "ecdsa521", `quote " and back\slash`}, // puttygen escapes both
		{"rsa.pkix", "rsa", ""},
		{"rsa.pkcs1pub", "rsa", ""},
		{"ecdsa384.pkix", "ecdsa384", ""},
		{"dsa.pkix", "dsa", ""},
		{"ed25519ossl.pkix", "ed25519ossl", ""},
		{"ed25519.pub", "ed25519", "ed25519@hawser-test"},
		{"dsa.pub", "dsa", "dsa@hawser-test"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := mustParsePublic(t, fixture(t, tc.base+".pub"))
			pub, comment, err := ParsePublicKey(fixture(t, tc.name))
			if err != nil {
				t.Fatalf("ParsePublicKey: %v", err)
			}
			if !samePublic(pub, want) {
				t.Error("key differs from the .pub file")
			}
			if comment != tc.comment {
				t.Errorf("comment = %q, want %q", comment, tc.comment)
			}
		})
	}
}

// TestFixturesFingerprints checks fingerprints and random art against values
// printed by "ssh-keygen -lv" and "ssh-keygen -E md5 -l" for the fixtures.
func TestFixturesFingerprints(t *testing.T) {
	tests := []struct {
		base, sha256, md5, name string
		bits                    int
		art                     string
	}{
		{
			base:   "ed25519",
			sha256: "SHA256:ewf9bkaU0pKemQyUu+UwFyciaPsdyVueAPXKOfcHWnc",
			name:   "ED25519", bits: 256,
		},
		{
			base:   "ecdsa256",
			sha256: "SHA256:fM59Kg9oltnkd8l62/piG73dxXu6nai9wkmy5Hw3Nas",
			name:   "ECDSA", bits: 256,
		},
		{
			base:   "rsa",
			sha256: "SHA256:3hjemvNLM1MaZs31wHy7ZjWIQMxnzj3GAemfm5fc+cA",
			name:   "RSA", bits: 2048,
		},
	}
	for _, tc := range tests {
		pub := mustParsePublic(t, fixture(t, tc.base+".pub"))
		if got := FingerprintSHA256(pub); got != tc.sha256 {
			t.Errorf("%s: FingerprintSHA256 = %s, want %s", tc.base, got, tc.sha256)
		}
		if got := ssh.FingerprintSHA256(pub); got != tc.sha256 {
			t.Errorf("%s: x/crypto disagrees: %s", tc.base, got)
		}
		if want := "MD5:" + ssh.FingerprintLegacyMD5(pub); FingerprintMD5(pub) != want {
			t.Errorf("%s: FingerprintMD5 = %s, want %s", tc.base, FingerprintMD5(pub), want)
		}
		name, bits := KeyTypeDisplay(pub)
		if name != tc.name || bits != tc.bits {
			t.Errorf("%s: KeyTypeDisplay = %s %d, want %s %d", tc.base, name, bits, tc.name, tc.bits)
		}
	}
}

func TestFixturesCertificates(t *testing.T) {
	t.Run("user", func(t *testing.T) {
		data := fixture(t, "ed25519-cert.pub")
		cert, info, err := ParseCertificate(data)
		if err != nil {
			t.Fatalf("ParseCertificate: %v", err)
		}
		if cert == nil || cert.KeyId != "hawser-test-id" {
			t.Fatalf("unexpected certificate %+v", cert)
		}
		want := CertInfo{
			Type:           "user",
			KeyID:          "hawser-test-id",
			Serial:         42,
			Principals:     []string{"alice", "bob"},
			ValidAfter:     time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC),
			ValidBefore:    time.Date(2027, 1, 1, 12, 30, 0, 0, time.UTC),
			CAFingerprint:  "SHA256:fM59Kg9oltnkd8l62/piG73dxXu6nai9wkmy5Hw3Nas",
			KeyFingerprint: "SHA256:ewf9bkaU0pKemQyUu+UwFyciaPsdyVueAPXKOfcHWnc",
			Algorithm:      "ssh-ed25519-cert-v01@openssh.com",
			KeyType:        "ED25519-CERT",
			KeyBits:        256,
			CAType:         "ECDSA",
		}
		if info.Type != want.Type || info.KeyID != want.KeyID || info.Serial != want.Serial ||
			strings.Join(info.Principals, ",") != strings.Join(want.Principals, ",") ||
			!info.ValidAfter.Equal(want.ValidAfter) || !info.ValidBefore.Equal(want.ValidBefore) ||
			info.CAFingerprint != want.CAFingerprint || info.KeyFingerprint != want.KeyFingerprint ||
			info.Algorithm != want.Algorithm || info.KeyType != want.KeyType || info.KeyBits != want.KeyBits ||
			info.CAType != want.CAType {
			t.Errorf("CertInfo = %+v\nwant     %+v", *info, want)
		}
		if info.Forever || !info.SignatureValid || info.SignatureAlgorithm != "ecdsa-sha2-nistp256" {
			t.Errorf("Forever/SignatureValid/SignatureAlgorithm = %v/%v/%q", info.Forever, info.SignatureValid, info.SignatureAlgorithm)
		}
		if info.Comment != "ed25519@hawser-test" {
			t.Errorf("Comment = %q", info.Comment)
		}
		if got := info.CriticalOptions; len(got) != 2 || got["force-command"] != "/bin/date" || got["source-address"] != "10.0.0.0/8" {
			t.Errorf("CriticalOptions = %v", got)
		}
		ext := info.Extensions
		if _, x11 := ext["permit-X11-forwarding"]; len(ext) != 4 || x11 {
			t.Errorf("Extensions = %v", ext)
		}
		for _, name := range []string{"permit-agent-forwarding", "permit-port-forwarding", "permit-pty", "permit-user-rc"} {
			if v, ok := ext[name]; !ok || v != "" {
				t.Errorf("extension %s = %q, %v", name, v, ok)
			}
		}
		if !info.ValidAt(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) ||
			info.ValidAt(time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)) ||
			info.ValidAt(time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)) {
			t.Error("ValidAt is wrong")
		}

		// The certificate is also a public key.
		pub, comment, err := ParsePublicKey(data)
		if err != nil || !IsCertificate(pub) || comment != "ed25519@hawser-test" {
			t.Fatalf("ParsePublicKey on a certificate: %v, %T, %q", err, pub, comment)
		}
		if name, bits := KeyTypeDisplay(pub); name != "ED25519-CERT" || bits != 256 {
			t.Errorf("KeyTypeDisplay = %s %d", name, bits)
		}
		// "[ED25519-CERT 256]" does not fit the frame, so ssh-keygen drops the size.
		if art := RandomArt(pub); !strings.HasPrefix(art, "+-[ED25519-CERT]--+\n") {
			t.Errorf("certificate random art header:\n%s", art)
		}

		// A flipped bit in the signed body must invalidate the signature.
		forged := *cert
		forged.Serial++
		if DescribeCertificate(&forged).SignatureValid {
			t.Error("a modified certificate still verifies")
		}
	})

	t.Run("host", func(t *testing.T) {
		_, info, err := ParseCertificate(fixture(t, "ecdsa384-cert.pub"))
		if err != nil {
			t.Fatalf("ParseCertificate: %v", err)
		}
		if info.Type != "host" || info.KeyID != "host.example.com" || info.Serial != 0 ||
			strings.Join(info.Principals, ",") != "host.example.com,10.1.2.3" {
			t.Errorf("CertInfo = %+v", *info)
		}
		if !info.Forever || !info.ValidAfter.IsZero() || !info.ValidBefore.IsZero() {
			t.Errorf("validity = %v .. %v (forever=%v), want always..forever", info.ValidAfter, info.ValidBefore, info.Forever)
		}
		if !info.ValidAt(time.Unix(0, 0)) || !info.ValidAt(time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Error("an always/forever certificate must always be valid")
		}
		if info.CAFingerprint != "SHA256:3hjemvNLM1MaZs31wHy7ZjWIQMxnzj3GAemfm5fc+cA" || info.CAType != "RSA" ||
			info.SignatureAlgorithm != "rsa-sha2-512" || !info.SignatureValid {
			t.Errorf("CA = %s %s using %s, valid=%v", info.CAType, info.CAFingerprint, info.SignatureAlgorithm, info.SignatureValid)
		}
		if info.KeyType != "ECDSA-CERT" || info.KeyBits != 384 || len(info.Extensions) != 0 || len(info.CriticalOptions) != 0 {
			t.Errorf("key = %s %d, extensions %v, options %v", info.KeyType, info.KeyBits, info.Extensions, info.CriticalOptions)
		}
	})

	t.Run("not a certificate", func(t *testing.T) {
		for _, name := range []string{"ed25519.pub", "dsa.pub", "rsa.rfc4716", "ed25519.ppk3"} {
			_, _, err := ParseCertificate(fixture(t, name))
			if !errors.Is(err, ErrNotCertificate) {
				t.Errorf("%s: got %v, want ErrNotCertificate", name, err)
			}
		}
		if _, _, err := ParseCertificate([]byte("garbage")); err == nil || errors.Is(err, ErrNotCertificate) {
			t.Errorf("garbage: got %v", err)
		}
	})
}
