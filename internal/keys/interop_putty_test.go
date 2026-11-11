package keys

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Interoperability with PuTTY's puttygen. These tests are skipped when the
// tool is not installed; testdata covers the same ground statically.

// puttyPassFile writes the passphrase file puttygen's --old-passphrase and
// --new-passphrase options expect. An empty file means "no passphrase".
func puttyPassFile(t *testing.T, dir, pass string) string {
	t.Helper()
	name := "pass-empty"
	if pass != "" {
		name = "pass-set"
	}
	return writeTemp(t, dir, name, []byte(pass))
}

// interopKeys is every generated key plus the DSA fixture.
func interopKeys(t *testing.T) map[string]*PrivateKey {
	keys := map[string]*PrivateKey{"dsa-1024": fixtureKey(t, "dsa")}
	for _, s := range allSpecs {
		keys[s.String()] = testKey(t, s)
	}
	return keys
}

// TestPuttygenReadsOurPPK writes PPK files of both versions, with and
// without a passphrase, and has puttygen extract the public key, print the
// fingerprint and convert the key back to OpenSSH and to the other PPK
// version.
func TestPuttygenReadsOurPPK(t *testing.T) {
	puttygen := needTool(t, "puttygen")
	dir := t.TempDir()
	passFile := puttyPassFile(t, dir, interopPass)

	for name, key := range interopKeys(t) {
		key.Comment = "ppk interop " + name
		pub := mustPublic(t, key)
		for _, version := range []int{2, 3} {
			for _, encrypted := range []bool{false, true} {
				label := fmt.Sprintf("%s/v%d/encrypted=%v", name, version, encrypted)
				t.Run(label, func(t *testing.T) {
					var pass []byte
					var passArgs []string
					if encrypted {
						pass = []byte(interopPass)
						passArgs = []string{"--old-passphrase", passFile}
					}
					data, err := key.MarshalPPK(version, pass)
					if err != nil {
						t.Fatal(err)
					}
					base := filepath.Join(dir, strings.NewReplacer("/", "_", "=", "_").Replace(label))
					path := writeTemp(t, dir, filepath.Base(base)+".ppk", data)

					// Public key, including the comment.
					out := runTool(t, puttygen, append([]string{path, "-O", "public-openssh"}, passArgs...)...)
					if want := string(MarshalAuthorizedKey(pub, key.Comment)); out != want {
						t.Fatalf("puttygen -O public-openssh:\n got  %s want %s", out, want)
					}

					// Fingerprint.
					out = runTool(t, puttygen, append([]string{path, "-O", "fingerprint"}, passArgs...)...)
					if !strings.Contains(out, FingerprintSHA256(pub)) {
						t.Errorf("puttygen fingerprint %q lacks %s", strings.TrimSpace(out), FingerprintSHA256(pub))
					}

					// Private key round trip through OpenSSH format. puttygen
					// keeps the passphrase when none is given for the output.
					converted := base + ".openssh"
					runTool(t, puttygen, append([]string{path, "-O", "private-openssh", "-o", converted}, passArgs...)...)
					back, err := ParsePrivateKey(mustRead(t, converted), pass)
					if err != nil {
						t.Fatalf("parsing puttygen's OpenSSH export: %v", err)
					}
					requireSameKey(t, back, key)

					// And through the other PPK version. puttygen refuses a
					// conversion that changes nothing but the file version,
					// so change the comment as well.
					resaved := base + ".resaved.ppk"
					other := fmt.Sprintf("version=%d", 5-version)
					runTool(t, puttygen, append([]string{path, "-O", "private", "-o", resaved,
						"--ppk-param", other, "-C", "re-saved by puttygen"}, passArgs...)...)
					back, err = ParsePrivateKey(mustRead(t, resaved), pass)
					if err != nil {
						t.Fatalf("parsing puttygen's re-saved PPK: %v", err)
					}
					requireSameKey(t, back, key)
					if back.Comment != "re-saved by puttygen" {
						t.Errorf("comment after re-save = %q", back.Comment)
					}

					if encrypted {
						wrong := writeTemp(t, dir, "pass-wrong", []byte("wrong"))
						if _, err := tryTool(puttygen, path, "-O", "private-openssh", "-o", base+".never", "--old-passphrase", wrong); err == nil {
							t.Error("puttygen accepted a wrong passphrase")
						}
					}
				})
			}
		}
	}
}

// TestPuttygenReadsOurPPKOptions covers the non-default Argon2 settings.
func TestPuttygenReadsOurPPKOptions(t *testing.T) {
	puttygen := needTool(t, "puttygen")
	dir := t.TempDir()
	passFile := puttyPassFile(t, dir, interopPass)
	key := testKey(t, keySpec{ECDSA, 384})
	want := string(MarshalAuthorizedKey(mustPublic(t, key), key.Comment))

	for i, opts := range []PPKOptions{
		{KDF: PPKArgon2i},
		{KDF: PPKArgon2id, Memory: 1024, Passes: 3, Parallelism: 4},
		{KDF: PPKArgon2i, Memory: 65536, Passes: 2, Parallelism: 2},
		{Memory: 64, Passes: 1},
	} {
		data, err := key.MarshalPPKOptions(opts, []byte(interopPass))
		if err != nil {
			t.Fatalf("%+v: %v", opts, err)
		}
		path := writeTemp(t, dir, fmt.Sprintf("opts%d.ppk", i), data)
		out := runTool(t, puttygen, path, "-O", "public-openssh", "--old-passphrase", passFile)
		if out != want {
			t.Errorf("%+v: puttygen read a different key", opts)
		}
	}
}

// TestPuttygenRejectsTamperedPPK modifies our files and checks that both
// puttygen and this package notice.
func TestPuttygenRejectsTamperedPPK(t *testing.T) {
	puttygen := needTool(t, "puttygen")
	dir := t.TempDir()
	key := testKey(t, keySpec{Ed25519, 256})
	for _, version := range []int{2, 3} {
		data, err := key.MarshalPPK(version, nil)
		if err != nil {
			t.Fatal(err)
		}
		tampered := []byte(strings.Replace(string(data), "Comment: ", "Comment: evil ", 1))
		path := writeTemp(t, dir, fmt.Sprintf("tampered%d.ppk", version), tampered)
		if _, err := tryTool(puttygen, path, "-O", "private-openssh", "-o", path+".out"); err == nil {
			t.Errorf("v%d: puttygen accepted a tampered file", version)
		}
		_, err = ParsePrivateKey(tampered, nil)
		requireErr(t, err, ErrMACMismatch)
	}
}

// TestParsePuttygenPPK converts our keys to PPK with puttygen, in both
// versions and with every key derivation function, and parses the result.
func TestParsePuttygenPPK(t *testing.T) {
	puttygen := needTool(t, "puttygen")
	dir := t.TempDir()
	passFile := puttyPassFile(t, dir, interopPass)

	params := []struct {
		name, param string
		unsupported bool
	}{
		{"v2", "version=2", false},
		{"v3", "version=3", false},
		{"v3-argon2i", "version=3,kdf=argon2i,memory=2048,passes=4", false},
		{"v3-argon2id-parallel", "version=3,kdf=argon2id,memory=4096,passes=2,parallelism=4", false},
		{"v3-argon2d", "version=3,kdf=argon2d,memory=1024,passes=2", true},
	}

	for name, key := range interopKeys(t) {
		key.Comment = "from puttygen " + name
		source, err := key.Marshal(FormatOpenSSH, nil)
		if err != nil {
			t.Fatal(err)
		}
		sourcePath := writeTemp(t, dir, name+".openssh", source)

		for _, p := range params {
			for _, encrypted := range []bool{false, true} {
				label := fmt.Sprintf("%s/%s/encrypted=%v", name, p.name, encrypted)
				t.Run(label, func(t *testing.T) {
					out := filepath.Join(dir, strings.NewReplacer("/", "_", "=", "_").Replace(label)+".ppk")
					args := []string{sourcePath, "-O", "private", "-o", out, "--ppk-param", p.param}
					var pass []byte
					if encrypted {
						pass = []byte(interopPass)
						args = append(args, "--new-passphrase", passFile)
					}
					runTool(t, puttygen, args...)
					data := mustRead(t, out)

					info, err := Inspect(data)
					if err != nil {
						t.Fatalf("Inspect: %v", err)
					}
					if info.Format != FormatPPK || info.Encrypted != encrypted || info.Comment != key.Comment ||
						!samePublic(info.PublicKey, mustPublic(t, key)) {
						t.Errorf("Inspect: %+v", info)
					}

					if encrypted {
						_, err := ParsePrivateKey(data, nil)
						requireErr(t, err, ErrPassphraseRequired)
						if p.unsupported {
							_, err = ParsePrivateKey(data, pass)
							requireErr(t, err, ErrUnsupported)
							return
						}
						_, err = ParsePrivateKey(data, []byte("wrong"))
						requireErr(t, err, ErrWrongPassphrase)
						requireErr(t, err, ErrMACMismatch)
					}
					back, err := ParsePrivateKey(data, pass)
					if err != nil {
						t.Fatalf("ParsePrivateKey: %v", err)
					}
					requireSameKey(t, back, key)
					if back.Comment != key.Comment || back.Format != FormatPPK {
						t.Errorf("Comment/Format = %q/%q", back.Comment, back.Format)
					}
				})
			}
		}
	}
}

// TestParsePuttygenGeneratedKeys parses keys generated by puttygen itself,
// which exercises PuTTY's own choice of primes and encodings.
func TestParsePuttygenGeneratedKeys(t *testing.T) {
	puttygen := needTool(t, "puttygen")
	if testing.Short() {
		t.Skip("puttygen key generation is slow")
	}
	dir := t.TempDir()

	kinds := []struct {
		name string
		args []string
		spec keySpec
	}{
		{"rsa-2048", []string{"-t", "rsa", "-b", "2048"}, keySpec{RSA, 2048}},
		{"dsa-1024", []string{"-t", "dsa", "-b", "1024"}, keySpec{DSA, 1024}},
		{"ecdsa-256", []string{"-t", "ecdsa", "-b", "256"}, keySpec{ECDSA, 256}},
		{"ecdsa-384", []string{"-t", "ecdsa", "-b", "384"}, keySpec{ECDSA, 384}},
		{"ecdsa-521", []string{"-t", "ecdsa", "-b", "521"}, keySpec{ECDSA, 521}},
		{"ed25519", []string{"-t", "ed25519"}, keySpec{Ed25519, 256}},
	}
	for _, kind := range kinds {
		for _, pass := range []string{"", interopPass} {
			for _, version := range []string{"2", "3"} {
				label := fmt.Sprintf("%s/v%s/encrypted=%v", kind.name, version, pass != "")
				t.Run(label, func(t *testing.T) {
					path := filepath.Join(dir, strings.NewReplacer("/", "_", "=", "_").Replace(label)+".ppk")
					passFile := puttyPassFile(t, dir, pass)
					args := append([]string{"-q", "-o", path, "-C", "native " + kind.name,
						"--new-passphrase", passFile, "--ppk-param", "version=" + version}, kind.args...)
					runTool(t, puttygen, args...)
					wantPub := mustParsePublic(t, []byte(runTool(t, puttygen, path, "-O", "public-openssh", "--old-passphrase", passFile)))

					key, err := ParsePrivateKey(mustRead(t, path), []byte(pass))
					if err != nil {
						t.Fatalf("ParsePrivateKey: %v", err)
					}
					if !samePublic(mustPublic(t, key), wantPub) {
						t.Fatal("parsed key does not match puttygen's public key")
					}
					if key.Comment != "native "+kind.name || key.Type() != kind.spec.typ || key.Bits() != kind.spec.bits {
						t.Errorf("Comment/Type/Bits = %q/%s/%d", key.Comment, key.Type(), key.Bits())
					}

					// Convert to OpenSSH and let puttygen confirm it is
					// still the same key.
					converted, err := key.Marshal(FormatOpenSSH, []byte(pass))
					if err != nil {
						t.Fatal(err)
					}
					out := runTool(t, puttygen, writeTemp(t, dir, filepath.Base(path)+".openssh", converted),
						"-O", "public-openssh", "--old-passphrase", passFile)
					if keyFields(out) != keyFields(string(MarshalAuthorizedKey(wantPub, ""))) {
						t.Error("puttygen reads a different key from our OpenSSH conversion")
					}
				})
			}
		}
	}

	t.Run("ed448 is reported as unsupported", func(t *testing.T) {
		path := filepath.Join(dir, "ed448.ppk")
		if _, err := tryTool(puttygen, "-q", "-t", "ed448", "-o", path, "--new-passphrase", puttyPassFile(t, dir, "")); err != nil {
			t.Skipf("puttygen cannot generate Ed448 keys: %v", err)
		}
		data := mustRead(t, path)
		_, err := ParsePrivateKey(data, nil)
		requireErr(t, err, ErrUnsupported)
		info, err := Inspect(data)
		if err != nil || info.Format != FormatPPK || info.Type != "" || info.Bits != 448 || info.PublicKey != nil {
			t.Errorf("Inspect of an Ed448 key: %+v, %v", info, err)
		}
	})
}

// TestPuttygenReadsOurOpenSSHAndPEM has puttygen import the OpenSSH and
// traditional PEM files we write (it does not read PKCS#8).
func TestPuttygenReadsOurOpenSSHAndPEM(t *testing.T) {
	puttygen := needTool(t, "puttygen")
	dir := t.TempDir()
	passFile := puttyPassFile(t, dir, interopPass)

	for name, key := range interopKeys(t) {
		want := keyFields(string(MarshalAuthorizedKey(mustPublic(t, key), "")))
		for _, m := range marshalers[:2] { // openssh, pem
			if m.name == "pem" && key.Type() == Ed25519 {
				continue // written as PKCS#8, which puttygen cannot import
			}
			for _, encrypted := range []bool{false, true} {
				label := fmt.Sprintf("%s/%s/encrypted=%v", name, m.name, encrypted)
				t.Run(label, func(t *testing.T) {
					var pass []byte
					args := []string{"", "-O", "public-openssh"}
					if encrypted {
						pass = []byte(interopPass)
						args = append(args, "--old-passphrase", passFile)
					}
					data, err := m.marshal(key, pass)
					if err != nil {
						t.Fatal(err)
					}
					args[0] = writeTemp(t, dir, strings.NewReplacer("/", "_", "=", "_").Replace(label), data)
					if out := runTool(t, puttygen, args...); keyFields(out) != want {
						t.Fatalf("puttygen read a different key: %s", out)
					}
				})
			}
		}
	}
}

// TestPublicKeyInterop exchanges public key files with ssh-keygen and
// puttygen in the RFC 4716, PKIX and PKCS#1 encodings.
func TestPublicKeyInterop(t *testing.T) {
	comments := []string{
		"",
		"user@host",
		`quote " and back\slash`,
		"a rather long comment that will certainly not fit on a single seventy-two byte line of an RFC 4716 file",
		"a long comment with colons: one: two: three: four: five: six: seven: eight: nine: ten: eleven: twelve: end",
		strings.Repeat("-", 100),
		"non-ASCII ümläut 中文 " + strings.Repeat("é", 60),
	}

	t.Run("ssh-keygen imports our RFC 4716", func(t *testing.T) {
		keygen := needTool(t, "ssh-keygen")
		dir := t.TempDir()
		for name, key := range interopKeys(t) {
			pub := mustPublic(t, key)
			want := keyFields(string(MarshalAuthorizedKey(pub, "")))
			for i, comment := range comments {
				path := writeTemp(t, dir, fmt.Sprintf("%s-%d.rfc", name, i), MarshalRFC4716(pub, comment))
				out, err := tryTool(keygen, "-i", "-f", path)
				if err != nil {
					if key.Type() == DSA {
						t.Logf("this ssh-keygen has no DSA support: %v", err)
						break
					}
					t.Fatalf("%s comment %d: %v", name, i, err)
				}
				if keyFields(out) != want {
					t.Errorf("%s comment %q: ssh-keygen -i gave %s", name, comment, out)
				}
			}
		}
	})

	t.Run("we import ssh-keygen exports", func(t *testing.T) {
		keygen := needTool(t, "ssh-keygen")
		dir := t.TempDir()
		for name, key := range interopKeys(t) {
			pub := mustPublic(t, key)
			path := writeTemp(t, dir, name+".pub", MarshalAuthorizedKey(pub, "c"))
			for _, format := range []string{"RFC4716", "PKCS8", "PEM"} {
				out, err := tryTool(keygen, "-e", "-m", format, "-f", path)
				if err != nil {
					if format != "RFC4716" || key.Type() == DSA {
						// ssh-keygen has no such encoding for this key type
						// (Ed25519 in PEM), or no DSA support at all.
						t.Logf("%s: ssh-keygen cannot export as %s", name, format)
						continue
					}
					t.Fatalf("%s %s: %v", name, format, err)
				}
				got, _, err := ParsePublicKey([]byte(out))
				if err != nil {
					t.Fatalf("%s %s: ParsePublicKey: %v\n%s", name, format, err, out)
				}
				if !samePublic(got, pub) {
					t.Errorf("%s %s: different key", name, format)
				}
			}
		}
	})

	t.Run("puttygen", func(t *testing.T) {
		puttygen := needTool(t, "puttygen")
		dir := t.TempDir()
		for name, key := range interopKeys(t) {
			pub := mustPublic(t, key)

			// puttygen reads our RFC 4716 files, comment included, as long
			// as the Comment header fits on one line (it does not
			// implement continuation lines).
			for i, comment := range comments[:3] {
				path := writeTemp(t, dir, fmt.Sprintf("%s-%d.rfc", name, i), MarshalRFC4716(pub, comment))
				out := runTool(t, puttygen, path, "-O", "public-openssh")
				if want := string(MarshalAuthorizedKey(pub, comment)); out != want {
					t.Errorf("%s comment %q: puttygen gave %q, want %q", name, comment, out, want)
				}
			}

			// We read puttygen's RFC 4716 export, comment included.
			for i, comment := range comments[1:] {
				key.Comment = comment
				ppk, err := key.MarshalPPK(3, nil)
				if err != nil {
					t.Fatal(err)
				}
				ppkPath := writeTemp(t, dir, fmt.Sprintf("%s-%d.ppk", name, i), ppk)
				out := filepath.Join(dir, fmt.Sprintf("%s-%d.putty.rfc", name, i))
				runTool(t, puttygen, ppkPath, "-O", "public", "-o", out)
				got, gotComment, err := ParsePublicKey(mustRead(t, out))
				if err != nil {
					t.Fatalf("%s: ParsePublicKey: %v", name, err)
				}
				if !samePublic(got, pub) || gotComment != comment {
					t.Errorf("%s: comment %q, want %q (same key: %v)", name, gotComment, comment, samePublic(got, pub))
				}
			}
		}
	})
}

// TestPuttygenCertificatePPK checks that a PPK file with an embedded OpenSSH
// certificate (PuTTY 0.78 and later) still yields the underlying key.
func TestPuttygenCertificatePPK(t *testing.T) {
	puttygen := needTool(t, "puttygen")
	dir := t.TempDir()
	key := fixtureKey(t, "ed25519")
	ppk, err := key.MarshalPPK(3, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := writeTemp(t, dir, "plain.ppk", ppk)
	out := filepath.Join(dir, "with-cert.ppk")
	if _, err := tryTool(puttygen, in, "--certificate", filepath.Join("testdata", "ed25519-cert.pub"), "-O", "private", "-o", out); err != nil {
		t.Skipf("this puttygen cannot embed certificates: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ssh-ed25519-cert-v01@openssh.com") {
		t.Fatalf("puttygen did not embed the certificate:\n%s", data)
	}
	back, err := ParsePrivateKey(data, nil)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}
	requireSameKey(t, back, key)

	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != Ed25519 || info.Bits != 256 || !IsCertificate(info.PublicKey) {
		t.Errorf("Inspect: %+v", info)
	}
	pub, _, err := ParsePublicKey(data)
	if err != nil || !IsCertificate(pub) {
		t.Errorf("ParsePublicKey: %T, %v", pub, err)
	}
	_, certInfo, err := ParseCertificate(data)
	if err != nil || certInfo.KeyID != "hawser-test-id" || !certInfo.SignatureValid {
		t.Errorf("ParseCertificate on a PPK with certificate: %+v, %v", certInfo, err)
	}
}
