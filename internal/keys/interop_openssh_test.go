package keys

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Interoperability with OpenSSH's ssh-keygen. These tests are skipped when
// the tool is not installed; testdata covers the same ground statically.

const interopPass = "interop pass phrase"

// TestSSHKeygenReadsOurKeys writes every generated key in the OpenSSH and PEM
// encodings, with and without a passphrase, and has ssh-keygen derive the
// public key from each. It also compares fingerprints and random art.
func TestSSHKeygenReadsOurKeys(t *testing.T) {
	keygen := needTool(t, "ssh-keygen")
	dir := t.TempDir()

	for _, spec := range allSpecs {
		key := testKey(t, spec)
		pub := mustPublic(t, key)
		wantKey := keyFields(string(MarshalAuthorizedKey(pub, "")))

		for _, m := range marshalers[:3] { // openssh, pem, pkcs8
			for _, encrypted := range []bool{false, true} {
				name := fmt.Sprintf("%v/%s/encrypted=%v", spec, m.name, encrypted)
				t.Run(name, func(t *testing.T) {
					var pass []byte
					args := []string{"-y", "-f", ""}
					if encrypted {
						pass = []byte(interopPass)
						args = append(args, "-P", interopPass)
					}
					data, err := m.marshal(key, pass)
					if err != nil {
						t.Fatal(err)
					}
					path := writeTemp(t, dir, strings.ReplaceAll(name, "/", "_"), data)
					args[2] = path

					out, err := tryTool(keygen, args...)
					if err != nil {
						if spec.typ == Ed25519 && m.name != "openssh" {
							// OpenSSH before 9.7 cannot read Ed25519 keys
							// in PKCS#8; OpenSSL can vouch for them instead.
							opensslChecksEd25519(t, path, pass, pub.Marshal())
							return
						}
						t.Fatal(err)
					}
					if keyFields(out) != wantKey {
						t.Fatalf("ssh-keygen derived a different public key:\n got %s want %s", out, wantKey)
					}
					if m.comment {
						if got := strings.Join(strings.Fields(out)[2:], " "); got != key.Comment {
							t.Errorf("ssh-keygen sees comment %q, want %q", got, key.Comment)
						}
					}
					if encrypted {
						if _, err := tryTool(keygen, "-y", "-f", path, "-P", "wrong"); err == nil {
							t.Error("ssh-keygen accepted a wrong passphrase")
						}
					}
				})
			}
		}

		t.Run(spec.String()+"/fingerprint", func(t *testing.T) {
			pubPath := writeTemp(t, dir, spec.String()+".pub", MarshalAuthorizedKey(pub, key.Comment))
			compareFingerprints(t, keygen, pubPath, key.Comment)
		})
	}

	t.Run("dsa", func(t *testing.T) {
		key := fixtureKey(t, "dsa")
		pub := mustPublic(t, key)
		for _, m := range marshalers[:3] {
			for _, pass := range []string{"", interopPass} {
				data, err := m.marshal(key, []byte(pass))
				if err != nil {
					t.Fatal(err)
				}
				path := writeTemp(t, dir, "dsa_"+m.name+strconv.Itoa(len(pass)), data)
				out, err := tryTool(keygen, "-y", "-f", path, "-P", pass)
				if err != nil {
					t.Skipf("this ssh-keygen has no DSA support: %v", err)
				}
				if keyFields(out) != keyFields(string(MarshalAuthorizedKey(pub, ""))) {
					t.Fatalf("%s: ssh-keygen derived a different DSA public key", m.name)
				}
			}
		}
		pubPath := writeTemp(t, dir, "dsa.pub", MarshalAuthorizedKey(pub, key.Comment))
		compareFingerprints(t, keygen, pubPath, key.Comment)
	})
}

// opensslChecksEd25519 verifies an Ed25519 PKCS#8 file with OpenSSL: the
// public key it derives must end in the same 32 bytes as the SSH blob.
func opensslChecksEd25519(t *testing.T, path string, pass, sshBlob []byte) {
	t.Helper()
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("ssh-keygen cannot read Ed25519 PKCS#8 keys and openssl is not installed")
	}
	out := runTool(t, openssl, "pkey", "-in", path, "-passin", "pass:"+string(pass), "-pubout", "-outform", "DER")
	if len(out) < 32 || !bytes.HasSuffix(sshBlob, []byte(out[len(out)-32:])) {
		t.Fatal("openssl derived a different Ed25519 public key")
	}
	t.Log("ssh-keygen cannot read Ed25519 PKCS#8 keys; verified with openssl instead")
}

// compareFingerprints checks FingerprintSHA256, FingerprintMD5, RandomArt and
// KeyTypeDisplay against "ssh-keygen -l", "-E md5 -l" and "-lv".
func compareFingerprints(t *testing.T, keygen, pubPath, comment string) {
	t.Helper()
	sshPub := mustParsePublic(t, mustRead(t, pubPath))
	name, bits := KeyTypeDisplay(sshPub)

	// "256 SHA256:... comment (ED25519)"
	line := strings.TrimSpace(runTool(t, keygen, "-l", "-f", pubPath))
	want := fmt.Sprintf("%d %s %s (%s)", bits, FingerprintSHA256(sshPub), comment, name)
	if line != want {
		t.Errorf("ssh-keygen -l:\n got  %s\n want %s", line, want)
	}

	line = strings.TrimSpace(runTool(t, keygen, "-E", "md5", "-l", "-f", pubPath))
	want = fmt.Sprintf("%d %s %s (%s)", bits, FingerprintMD5(sshPub), comment, name)
	if line != want {
		t.Errorf("ssh-keygen -E md5 -l:\n got  %s\n want %s", line, want)
	}

	out := runTool(t, keygen, "-l", "-v", "-f", pubPath)
	_, art, _ := strings.Cut(out, "\n")
	if art != RandomArt(sshPub) {
		t.Errorf("random art differs.\nssh-keygen:\n%s\nRandomArt:\n%s", art, RandomArt(sshPub))
	}
}

func mustRead(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestParseSSHKeygenKeys generates keys with ssh-keygen in every format it
// offers and parses them.
func TestParseSSHKeygenKeys(t *testing.T) {
	keygen := needTool(t, "ssh-keygen")
	dir := t.TempDir()

	kinds := []struct {
		name string
		args []string
		spec keySpec
	}{
		{"rsa-2048", []string{"-t", "rsa", "-b", "2048"}, keySpec{RSA, 2048}},
		{"rsa-3072", []string{"-t", "rsa", "-b", "3072"}, keySpec{RSA, 3072}},
		{"rsa-4096", []string{"-t", "rsa", "-b", "4096"}, keySpec{RSA, 4096}},
		{"ecdsa-256", []string{"-t", "ecdsa", "-b", "256"}, keySpec{ECDSA, 256}},
		{"ecdsa-384", []string{"-t", "ecdsa", "-b", "384"}, keySpec{ECDSA, 384}},
		{"ecdsa-521", []string{"-t", "ecdsa", "-b", "521"}, keySpec{ECDSA, 521}},
		{"ed25519", []string{"-t", "ed25519"}, keySpec{Ed25519, 256}},
		{"dsa", []string{"-t", "dsa"}, keySpec{DSA, 1024}},
	}
	formats := []struct {
		name string
		args []string
	}{
		{"openssh", nil},
		{"pem", []string{"-m", "PEM"}},
		{"pkcs8", []string{"-m", "PKCS8"}},
	}

	for _, kind := range kinds {
		for _, format := range formats {
			for _, pass := range []string{"", interopPass} {
				name := fmt.Sprintf("%s/%s/encrypted=%v", kind.name, format.name, pass != "")
				t.Run(name, func(t *testing.T) {
					path := filepath.Join(dir, strings.NewReplacer("/", "_", "=", "_").Replace(name))
					comment := "made by ssh-keygen: " + kind.name
					args := append([]string{"-q", "-N", pass, "-C", comment, "-f", path}, kind.args...)
					args = append(args, format.args...)
					if _, err := tryTool(keygen, args...); err != nil {
						if kind.spec.typ == DSA {
							t.Skipf("this ssh-keygen cannot generate DSA keys: %v", err)
						}
						t.Fatal(err)
					}
					data := mustRead(t, path)
					wantPub := mustParsePublic(t, mustRead(t, path+".pub"))

					// ssh-keygen falls back to its own format for key types
					// that have no PEM encoding (Ed25519), so look at what
					// it actually wrote.
					wantFormat, wantComment := FormatPEM, ""
					if bytes.Contains(data, []byte("BEGIN OPENSSH PRIVATE KEY")) {
						wantFormat, wantComment = FormatOpenSSH, comment
					}
					if format.name == "openssh" && wantFormat != FormatOpenSSH {
						t.Fatalf("expected an OpenSSH format file:\n%s", data)
					}
					if format.name != "openssh" && kind.spec.typ != Ed25519 && wantFormat != FormatPEM {
						t.Fatalf("expected a PEM file:\n%s", data)
					}

					info, err := Inspect(data)
					if err != nil {
						t.Fatalf("Inspect: %v", err)
					}
					if info.Format != wantFormat || info.Encrypted != (pass != "") {
						t.Errorf("Inspect: %+v", info)
					}

					if pass != "" {
						_, err := ParsePrivateKey(data, nil)
						requireErr(t, err, ErrPassphraseRequired)
						_, err = ParsePrivateKey(data, []byte(pass+"x"))
						requireErr(t, err, ErrWrongPassphrase)
					}
					key, err := ParsePrivateKey(data, []byte(pass))
					if err != nil {
						t.Fatalf("ParsePrivateKey: %v", err)
					}
					if !samePublic(mustPublic(t, key), wantPub) {
						t.Fatal("parsed key does not match ssh-keygen's .pub file")
					}
					if key.Format != wantFormat || key.Comment != wantComment {
						t.Errorf("Format/Comment = %q/%q, want %q/%q", key.Format, key.Comment, wantFormat, wantComment)
					}
					if key.Type() != kind.spec.typ || key.Bits() != kind.spec.bits {
						t.Errorf("Type/Bits = %s/%d, want %v", key.Type(), key.Bits(), kind.spec)
					}
				})
			}
		}
	}
}

// TestParseSSHKeygenCiphers covers every cipher and a range of bcrypt work
// factors ssh-keygen can protect a key with.
func TestParseSSHKeygenCiphers(t *testing.T) {
	keygen := needTool(t, "ssh-keygen")
	dir := t.TempDir()

	type variant struct {
		name string
		args []string
	}
	var variants []variant
	for cipher := range opensshCiphers {
		variants = append(variants, variant{cipher, []string{"-Z", cipher}})
	}
	for _, rounds := range []string{"1", "2", "100"} {
		variants = append(variants, variant{"rounds-" + rounds, []string{"-a", rounds}})
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(v.name, "@", "_"))
			args := append([]string{"-q", "-t", "ed25519", "-N", interopPass, "-C", v.name, "-f", path}, v.args...)
			if _, err := tryTool(keygen, args...); err != nil {
				t.Skipf("ssh-keygen does not support this variant: %v", err)
			}
			data := mustRead(t, path)
			wantPub := mustParsePublic(t, mustRead(t, path+".pub"))

			info, err := Inspect(data)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(v.name, "-") && !strings.HasPrefix(v.name, "rounds") && info.Cipher != v.name {
				t.Errorf("Inspect reports cipher %q, want %q", info.Cipher, v.name)
			}
			_, err = ParsePrivateKey(data, []byte("wrong"))
			requireErr(t, err, ErrWrongPassphrase)
			key, err := ParsePrivateKey(data, []byte(interopPass))
			if err != nil {
				t.Fatalf("ParsePrivateKey: %v", err)
			}
			if !samePublic(mustPublic(t, key), wantPub) || key.Comment != v.name {
				t.Errorf("wrong key or comment %q", key.Comment)
			}
		})
	}
}

// TestSSHKeygenCertificate has ssh-keygen sign certificates for our keys with
// our keys as the CA and compares ParseCertificate with "ssh-keygen -L".
func TestSSHKeygenCertificate(t *testing.T) {
	keygen := needTool(t, "ssh-keygen")

	tests := []struct {
		ca, subject keySpec
		host        bool
	}{
		{keySpec{Ed25519, 256}, keySpec{Ed25519, 256}, false},
		{keySpec{RSA, 2048}, keySpec{ECDSA, 384}, false},
		{keySpec{ECDSA, 256}, keySpec{RSA, 3072}, true},
		{keySpec{ECDSA, 521}, keySpec{ECDSA, 521}, false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%v signs %v", tc.ca, tc.subject), func(t *testing.T) {
			dir := t.TempDir()
			ca, subject := testKey(t, tc.ca), testKey(t, tc.subject)
			caData, err := ca.Marshal(FormatOpenSSH, nil)
			if err != nil {
				t.Fatal(err)
			}
			caPath := writeTemp(t, dir, "ca", caData)
			subjectPub := mustPublic(t, subject)
			writeTemp(t, dir, "key.pub", MarshalAuthorizedKey(subjectPub, "subject comment"))

			args := []string{"-q", "-s", caPath, "-I", "id", "-n", "user", "-V", "+1h"}
			if tc.host {
				args = append(args, "-h")
			}
			before := time.Now()
			runTool(t, keygen, append(args, filepath.Join(dir, "key.pub"))...)
			certPath := filepath.Join(dir, "key-cert.pub")
			data := mustRead(t, certPath)

			cert, info, err := ParseCertificate(data)
			if err != nil {
				t.Fatalf("ParseCertificate: %v", err)
			}
			wantType := "user"
			if tc.host {
				wantType = "host"
			}
			if info.Type != wantType || info.KeyID != "id" || info.Serial != 0 ||
				len(info.Principals) != 1 || info.Principals[0] != "user" {
				t.Errorf("CertInfo = %+v", *info)
			}
			if info.CAFingerprint != FingerprintSHA256(mustPublic(t, ca)) {
				t.Errorf("CAFingerprint = %s", info.CAFingerprint)
			}
			if info.KeyFingerprint != FingerprintSHA256(subjectPub) || !samePublic(cert.Key, subjectPub) {
				t.Errorf("certified key differs: %s", info.KeyFingerprint)
			}
			if !info.SignatureValid || info.Forever || info.Comment != "subject comment" {
				t.Errorf("SignatureValid/Forever/Comment = %v/%v/%q", info.SignatureValid, info.Forever, info.Comment)
			}
			// "+1h" means from now (rounded down to the minute) for an hour.
			if d := info.ValidBefore.Sub(before.Add(time.Hour)); d < -2*time.Minute || d > 2*time.Minute {
				t.Errorf("ValidBefore = %v, expected about an hour from %v", info.ValidBefore, before)
			}
			if d := before.Sub(info.ValidAfter); d < -time.Minute || d > 2*time.Minute {
				t.Errorf("ValidAfter = %v, expected about %v", info.ValidAfter, before)
			}
			if !info.ValidAt(time.Now().Add(time.Minute)) || info.ValidAt(time.Now().Add(2*time.Hour)) {
				t.Error("ValidAt is wrong")
			}
			wantExt := 5 // ssh-keygen's default user permissions
			if tc.host {
				wantExt = 0
			}
			if len(info.Extensions) != wantExt || len(info.CriticalOptions) != 0 {
				t.Errorf("Extensions = %v, CriticalOptions = %v", info.Extensions, info.CriticalOptions)
			}

			// Compare with ssh-keygen's own description.
			desc := runTool(t, keygen, "-L", "-f", certPath)
			for _, want := range []string{
				fmt.Sprintf("Type: %s %s certificate", info.Algorithm, info.Type),
				fmt.Sprintf("Public key: %s %s", info.KeyType, info.KeyFingerprint),
				fmt.Sprintf("Signing CA: %s %s (using %s)", info.CAType, info.CAFingerprint, info.SignatureAlgorithm),
				`Key ID: "id"`,
				"Serial: 0",
			} {
				if !strings.Contains(desc, want) {
					t.Errorf("ssh-keygen -L output lacks %q:\n%s", want, desc)
				}
			}
			for ext := range info.Extensions {
				if !strings.Contains(desc, ext) {
					t.Errorf("ssh-keygen -L does not list extension %s", ext)
				}
			}

			// Fingerprint and art of a certificate file.
			compareFingerprints(t, keygen, certPath, "subject comment")
		})
	}
}
