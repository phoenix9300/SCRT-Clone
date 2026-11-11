package keys

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestGenerate(t *testing.T) {
	tests := []struct {
		typ      Type
		bits     int
		wantBits int
		wantType any
	}{
		{RSA, 0, 3072, (*rsa.PrivateKey)(nil)},
		{RSA, 2048, 2048, (*rsa.PrivateKey)(nil)},
		{ECDSA, 0, 256, (*ecdsa.PrivateKey)(nil)},
		{ECDSA, 384, 384, (*ecdsa.PrivateKey)(nil)},
		{ECDSA, 521, 521, (*ecdsa.PrivateKey)(nil)},
		{Ed25519, 0, 256, ed25519.PrivateKey(nil)},
		{Ed25519, 4096, 256, ed25519.PrivateKey(nil)}, // bits are ignored
	}
	for _, tc := range tests {
		k, err := Generate(tc.typ, tc.bits)
		if err != nil {
			t.Errorf("Generate(%s, %d): %v", tc.typ, tc.bits, err)
			continue
		}
		if k.Type() != tc.typ || k.Bits() != tc.wantBits {
			t.Errorf("Generate(%s, %d) made a %s key of %d bits", tc.typ, tc.bits, k.Type(), k.Bits())
		}
		if fmt.Sprintf("%T", k.Signer) != fmt.Sprintf("%T", tc.wantType) {
			t.Errorf("Generate(%s, %d).Signer is a %T, want %T", tc.typ, tc.bits, k.Signer, tc.wantType)
		}
		if k.Format != "" || k.Comment != "" {
			t.Errorf("generated key has Format %q, Comment %q", k.Format, k.Comment)
		}
	}

	for _, tc := range []struct {
		typ  Type
		bits int
	}{
		{RSA, 1024}, {RSA, 2047}, {RSA, 32768}, {RSA, -1},
		{ECDSA, 224}, {ECDSA, 512}, {ECDSA, 255},
		{DSA, 1024}, {DSA, 0}, {"", 0}, {"ed448", 0},
	} {
		if k, err := Generate(tc.typ, tc.bits); err == nil {
			t.Errorf("Generate(%q, %d) succeeded with a %d-bit key", tc.typ, tc.bits, k.Bits())
		}
	}
	if _, err := Generate(DSA, 1024); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Generate(DSA) = %v, want ErrUnsupported", err)
	}
}

// marshalers enumerates every private key encoding this package can write.
var marshalers = []struct {
	name    string
	format  Format // format reported when the result is parsed back
	comment bool   // the encoding carries a comment
	marshal func(k *PrivateKey, pass []byte) ([]byte, error)
}{
	{"openssh", FormatOpenSSH, true, func(k *PrivateKey, p []byte) ([]byte, error) { return k.Marshal(FormatOpenSSH, p) }},
	{"pem", FormatPEM, false, func(k *PrivateKey, p []byte) ([]byte, error) { return k.Marshal(FormatPEM, p) }},
	{"pkcs8", FormatPEM, false, func(k *PrivateKey, p []byte) ([]byte, error) { return k.MarshalPEM(PEMPKCS8, p) }},
	{"ppk3", FormatPPK, true, func(k *PrivateKey, p []byte) ([]byte, error) { return k.Marshal(FormatPPK, p) }},
	{"ppk2", FormatPPK, true, func(k *PrivateKey, p []byte) ([]byte, error) { return k.MarshalPPK(2, p) }},
}

// roundTripKeys returns one key of every type, DSA included.
func roundTripKeys(t *testing.T) map[string]*PrivateKey {
	keys := map[string]*PrivateKey{"dsa-1024": fixtureKey(t, "dsa")}
	for _, s := range quickSpecs {
		keys[s.String()] = testKey(t, s)
	}
	return keys
}

// TestRoundTrip writes every key type in every format, with and without a
// passphrase, and reads it back.
func TestRoundTrip(t *testing.T) {
	pass := []byte("correct horse battery staple")
	for name, key := range roundTripKeys(t) {
		key.Comment = "round trip: " + name + " ü中"
		for _, m := range marshalers {
			for _, encrypted := range []bool{false, true} {
				label := fmt.Sprintf("%s/%s/encrypted=%v", name, m.name, encrypted)
				t.Run(label, func(t *testing.T) {
					var p []byte
					if encrypted {
						p = pass
					}
					data, err := m.marshal(key, p)
					if err != nil {
						t.Fatalf("marshal: %v", err)
					}

					info, err := Inspect(data)
					if err != nil {
						t.Fatalf("Inspect: %v", err)
					}
					if info.Format != m.format || info.Encrypted != encrypted {
						t.Errorf("Inspect: Format/Encrypted = %q/%v, want %q/%v", info.Format, info.Encrypted, m.format, encrypted)
					}

					if encrypted {
						_, err := ParsePrivateKey(data, nil)
						requireErr(t, err, ErrPassphraseRequired)
						_, err = ParsePrivateKey(data, []byte("Correct horse battery staple"))
						requireErr(t, err, ErrWrongPassphrase)
					}
					back, err := ParsePrivateKey(data, p)
					if err != nil {
						t.Fatalf("ParsePrivateKey: %v", err)
					}
					requireSameKey(t, back, key)
					if back.Format != m.format {
						t.Errorf("Format = %q, want %q", back.Format, m.format)
					}
					wantComment := ""
					if m.comment {
						wantComment = key.Comment
					}
					if back.Comment != wantComment {
						t.Errorf("Comment = %q, want %q", back.Comment, wantComment)
					}
					if back.Type() != key.Type() || back.Bits() != key.Bits() {
						t.Errorf("Type/Bits = %s/%d, want %s/%d", back.Type(), back.Bits(), key.Type(), key.Bits())
					}
				})
			}
		}
	}
}

// TestEncryptionIsRandomised makes sure two encryptions of the same key with
// the same passphrase differ (fresh salt and IV each time) and that the
// plaintext key material does not leak into the output.
func TestEncryptionIsRandomised(t *testing.T) {
	key := testKey(t, keySpec{Ed25519, 256})
	seed := key.Signer.(ed25519.PrivateKey).Seed()
	pass := []byte("pw")
	for _, m := range marshalers {
		a, err := m.marshal(key, pass)
		if err != nil {
			t.Fatal(err)
		}
		b, err := m.marshal(key, pass)
		if err != nil {
			t.Fatal(err)
		}
		if m.name == "ppk2" {
			// Version 2 has no salt: the output is deterministic by design.
			if !bytes.Equal(a, b) {
				t.Errorf("%s: output should be deterministic", m.name)
			}
		} else if bytes.Equal(a, b) {
			t.Errorf("%s: two encryptions produced identical output", m.name)
		}
		plain, err := m.marshal(key, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !containsSecret(t, plain, seed) {
			t.Errorf("%s: test is broken, the unencrypted file should contain the seed", m.name)
		}
		if containsSecret(t, a, seed) {
			t.Errorf("%s: the encrypted file contains the plaintext seed", m.name)
		}
	}
}

// containsSecret reports whether the decoded payload of a key file contains
// secret verbatim.
func containsSecret(t *testing.T, file, secret []byte) bool {
	t.Helper()
	if block, _ := pem.Decode(file); block != nil {
		return bytes.Contains(block.Bytes, secret)
	}
	f, err := parsePPKFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(f.private, secret)
}

// TestSignRoundTrip signs through ssh.Signer and verifies with the public
// key, for every key type and every signature algorithm on offer.
func TestSignRoundTrip(t *testing.T) {
	msg := []byte("hawser signing round trip")
	for name, key := range roundTripKeys(t) {
		t.Run(name, func(t *testing.T) {
			signer, err := key.SSHSigner()
			if err != nil {
				t.Fatalf("SSHSigner: %v", err)
			}
			pub := mustPublic(t, key)
			if !samePublic(signer.PublicKey(), pub) {
				t.Fatal("signer and PublicKey disagree")
			}
			sig, err := signer.Sign(rand.Reader, msg)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if err := pub.Verify(msg, sig); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if err := pub.Verify(append([]byte("x"), msg...), sig); err == nil {
				t.Fatal("signature verified for a different message")
			}

			// The public key must survive an authorized_keys round trip
			// and still verify.
			reparsed := mustParsePublic(t, MarshalAuthorizedKey(pub, "c"))
			if err := reparsed.Verify(msg, sig); err != nil {
				t.Fatalf("Verify with re-parsed key: %v", err)
			}

			if key.Type() != RSA {
				return
			}
			multi, ok := signer.(ssh.MultiAlgorithmSigner)
			if !ok {
				t.Fatalf("RSA signer %T is not a MultiAlgorithmSigner", signer)
			}
			for _, algo := range []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA} {
				sig, err := multi.SignWithAlgorithm(rand.Reader, msg, algo)
				if err != nil {
					t.Fatalf("SignWithAlgorithm(%s): %v", algo, err)
				}
				if sig.Format != algo {
					t.Errorf("signature format %q, want %q", sig.Format, algo)
				}
				if err := pub.Verify(msg, sig); err != nil {
					t.Fatalf("Verify(%s): %v", algo, err)
				}
			}
		})
	}
}

// TestCryptoSigner exercises the crypto.Signer side of the DSA wrapper.
func TestCryptoSigner(t *testing.T) {
	key := fixtureKey(t, "dsa")
	d, ok := key.Signer.(*DSAPrivateKey)
	if !ok {
		t.Fatalf("DSA key is a %T", key.Signer)
	}
	digest := bytes.Repeat([]byte{0x5a}, 20)
	der, err := d.Sign(nil, digest, crypto.SHA1)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(der) < 40 || der[0] != 0x30 {
		t.Fatalf("signature is not a DER sequence: %x", der)
	}
	if !d.Equal(d) || d.Equal(testKey(t, keySpec{Ed25519, 256}).Signer) {
		t.Error("DSAPrivateKey.Equal is wrong")
	}
}

// TestSSHAuthentication runs a real SSH handshake in-process and logs in
// with each key type.
func TestSSHAuthentication(t *testing.T) {
	hostKey, err := testKey(t, keySpec{Ed25519, 256}).SSHSigner()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range quickSpecs {
		t.Run(spec.String(), func(t *testing.T) {
			key := testKey(t, spec)
			// Go through a serialised, encrypted copy to prove the loaded
			// key is what authenticates.
			data, err := key.Marshal(FormatPPK, []byte("pw"))
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := ParsePrivateKey(data, []byte("pw"))
			if err != nil {
				t.Fatal(err)
			}
			signer, err := loaded.SSHSigner()
			if err != nil {
				t.Fatal(err)
			}
			want := mustPublic(t, key)

			server := &ssh.ServerConfig{
				PublicKeyCallback: func(_ ssh.ConnMetadata, offered ssh.PublicKey) (*ssh.Permissions, error) {
					if samePublic(offered, want) {
						return &ssh.Permissions{}, nil
					}
					return nil, errors.New("unknown key")
				},
			}
			server.AddHostKey(hostKey)
			client := &ssh.ClientConfig{
				User:            "hawser",
				Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
				HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()),
			}

			// net.Pipe is unbuffered and deadlocks on the simultaneous
			// version exchange, so use a loopback socket.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Skipf("cannot listen on loopback: %v", err)
			}
			defer ln.Close()
			done := make(chan error, 1)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					done <- err
					return
				}
				defer c.Close()
				conn, _, _, err := ssh.NewServerConn(c, server)
				if err == nil {
					conn.Close()
				}
				done <- err
			}()
			c, err := net.DialTimeout("tcp", ln.Addr().String(), 10*time.Second)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			conn, _, _, err := ssh.NewClientConn(c, "loopback", client)
			if err != nil {
				t.Fatalf("client handshake: %v", err)
			}
			conn.Close()
			if err := <-done; err != nil {
				t.Fatalf("server handshake: %v", err)
			}
		})
	}
}

// TestXCryptoCrossCheck compares the OpenSSH codec with the one in
// golang.org/x/crypto/ssh in both directions.
func TestXCryptoCrossCheck(t *testing.T) {
	pass := []byte("x/crypto")
	for _, spec := range quickSpecs {
		t.Run(spec.String(), func(t *testing.T) {
			key := testKey(t, spec)

			// Ours -> theirs.
			for _, p := range [][]byte{nil, pass} {
				data, err := key.Marshal(FormatOpenSSH, p)
				if err != nil {
					t.Fatal(err)
				}
				var raw any
				if p == nil {
					raw, err = ssh.ParseRawPrivateKey(data)
				} else {
					raw, err = ssh.ParseRawPrivateKeyWithPassphrase(data, p)
				}
				if err != nil {
					t.Fatalf("x/crypto cannot read our key: %v", err)
				}
				theirs, err := New(raw.(crypto.Signer), "")
				if err != nil {
					t.Fatal(err)
				}
				requireSameKey(t, theirs, key)
			}

			// Theirs -> ours, in the PEM flavours as well.
			block, err := ssh.MarshalPrivateKeyWithPassphrase(key.Signer, "theirs", pass)
			if err != nil {
				t.Fatal(err)
			}
			back, err := ParsePrivateKey(pem.EncodeToMemory(block), pass)
			if err != nil {
				t.Fatalf("cannot read x/crypto's key: %v", err)
			}
			requireSameKey(t, back, key)
			if back.Comment != "theirs" {
				t.Errorf("Comment = %q", back.Comment)
			}

			for _, style := range []PEMStyle{PEMTraditional, PEMPKCS8} {
				data, err := key.MarshalPEM(style, nil)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := ssh.ParseRawPrivateKey(data)
				if err != nil {
					t.Fatalf("x/crypto cannot read our PEM (style %d): %v", style, err)
				}
				theirs, err := New(raw.(crypto.Signer), "")
				if err != nil {
					t.Fatal(err)
				}
				requireSameKey(t, theirs, key)
			}
			if spec.typ != Ed25519 {
				// Legacy PEM encryption, which x/crypto can decrypt.
				data, err := key.MarshalPEM(PEMTraditional, pass)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := ssh.ParseRawPrivateKeyWithPassphrase(data, pass)
				if err != nil {
					t.Fatalf("x/crypto cannot decrypt our legacy PEM: %v", err)
				}
				theirs, err := New(raw.(crypto.Signer), "")
				if err != nil {
					t.Fatal(err)
				}
				requireSameKey(t, theirs, key)
			}
		})
	}
}

func TestPEMEncodings(t *testing.T) {
	blockType := func(data []byte) (string, map[string]string) {
		block, rest := pem.Decode(data)
		if block == nil || len(bytes.TrimSpace(rest)) != 0 {
			t.Fatalf("not a single PEM block:\n%s", data)
		}
		return block.Type, block.Headers
	}
	pass := []byte("pw")
	tests := []struct {
		spec               keySpec
		trad, tradEnc      string
		legacyEncryption   bool
		pkcs8, pkcs8Enc    string
		variant, variantP8 string
	}{
		{keySpec{RSA, 2048}, "RSA PRIVATE KEY", "RSA PRIVATE KEY", true, "PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "PKCS#1", "PKCS#8"},
		{keySpec{ECDSA, 256}, "EC PRIVATE KEY", "EC PRIVATE KEY", true, "PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "SEC1", "PKCS#8"},
		// Ed25519 has no traditional encoding and falls back to PKCS#8.
		{keySpec{Ed25519, 256}, "PRIVATE KEY", "ENCRYPTED PRIVATE KEY", false, "PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "PKCS#8", "PKCS#8"},
	}
	for _, tc := range tests {
		key := testKey(t, tc.spec)
		check := func(style PEMStyle, p []byte, wantType string, wantLegacy bool) {
			t.Helper()
			data, err := key.MarshalPEM(style, p)
			if err != nil {
				t.Fatalf("%v: %v", tc.spec, err)
			}
			typ, headers := blockType(data)
			if typ != wantType {
				t.Errorf("%v style %d: block type %q, want %q", tc.spec, style, typ, wantType)
			}
			if wantLegacy {
				if headers["Proc-Type"] != "4,ENCRYPTED" || !strings.HasPrefix(headers["DEK-Info"], "AES-128-CBC,") {
					t.Errorf("%v: legacy encryption headers = %v", tc.spec, headers)
				}
			} else if len(headers) != 0 {
				t.Errorf("%v: unexpected headers %v", tc.spec, headers)
			}
		}
		check(PEMTraditional, nil, tc.trad, false)
		check(PEMTraditional, pass, tc.tradEnc, tc.legacyEncryption)
		check(PEMPKCS8, nil, tc.pkcs8, false)
		check(PEMPKCS8, pass, tc.pkcs8Enc, false)

		data, _ := key.MarshalPEM(PEMPKCS8, pass)
		info, err := Inspect(data)
		if err != nil {
			t.Fatal(err)
		}
		if info.Cipher != "AES-256-CBC" || info.KDF != "PBKDF2-HMAC-SHA256" || info.Type != "" || info.PublicKey != nil {
			t.Errorf("%v: Inspect of encrypted PKCS#8 = %+v", tc.spec, info)
		}
	}

	dsa := fixtureKey(t, "dsa")
	data, err := dsa.Marshal(FormatPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	if typ, _ := blockType(data); typ != "DSA PRIVATE KEY" {
		t.Errorf("DSA block type %q", typ)
	}
	if _, err := dsa.MarshalPEM(PEMStyle(7), nil); err == nil {
		t.Error("unknown PEM style accepted")
	}
}

func TestMarshalUnknownFormat(t *testing.T) {
	key := testKey(t, keySpec{Ed25519, 256})
	for _, f := range []Format{"", "pkcs12", "OPENSSH"} {
		if _, err := key.Marshal(f, nil); !errors.Is(err, ErrUnknownFormat) {
			t.Errorf("Marshal(%q) = %v, want ErrUnknownFormat", f, err)
		}
	}
	for _, v := range []int{1, 4, -1} {
		if _, err := key.MarshalPPK(v, nil); !errors.Is(err, ErrUnsupported) {
			t.Errorf("MarshalPPK(%d) = %v, want ErrUnsupported", v, err)
		}
	}
	if data, err := key.MarshalPPK(0, nil); err != nil || !bytes.HasPrefix(data, []byte("PuTTY-User-Key-File-3: ")) {
		t.Errorf("MarshalPPK(0) should write version 3: %v", err)
	}
}

// TestInvalidKeys makes sure the methods cope with missing, typed-nil and
// foreign keys instead of panicking.
func TestInvalidKeys(t *testing.T) {
	p224, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var nilEd *ed25519.PrivateKey
	bad := map[string]*PrivateKey{
		"nil receiver":   nil,
		"no signer":      {},
		"nil rsa":        {Signer: (*rsa.PrivateKey)(nil)},
		"empty rsa":      {Signer: &rsa.PrivateKey{}},
		"nil ecdsa":      {Signer: (*ecdsa.PrivateKey)(nil)},
		"empty ecdsa":    {Signer: &ecdsa.PrivateKey{}},
		"P-224":          {Signer: p224},
		"short ed25519":  {Signer: ed25519.PrivateKey{1, 2, 3}},
		"nil ed25519":    {Signer: nilEd},
		"nil dsa":        {Signer: (*DSAPrivateKey)(nil)},
		"empty dsa":      {Signer: &DSAPrivateKey{}},
		"foreign signer": {Signer: foreignSigner{}},
	}
	for name, k := range bad {
		t.Run(name, func(t *testing.T) {
			if k.Type() != "" || k.Bits() != 0 {
				t.Errorf("Type/Bits = %q/%d", k.Type(), k.Bits())
			}
			if _, err := k.PublicKey(); err == nil {
				t.Error("PublicKey succeeded")
			}
			if _, err := k.SSHSigner(); err == nil {
				t.Error("SSHSigner succeeded")
			}
			if _, err := k.AuthorizedKey(); err == nil {
				t.Error("AuthorizedKey succeeded")
			}
			for _, m := range marshalers {
				if _, err := m.marshal(k, nil); err == nil {
					t.Errorf("marshal %s succeeded", m.name)
				}
				if _, err := m.marshal(k, []byte("pw")); err == nil {
					t.Errorf("encrypted marshal %s succeeded", m.name)
				}
			}
		})
	}
	if _, err := New(nil, ""); err == nil {
		t.Error("New(nil) succeeded")
	}
	if _, err := New(foreignSigner{}, ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("New(foreign) = %v", err)
	}
}

type foreignSigner struct{}

func (foreignSigner) Public() crypto.PublicKey { return nil }
func (foreignSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("not implemented")
}

// TestEd25519PointerAccepted checks the documented normalisation of
// *ed25519.PrivateKey, the type golang.org/x/crypto/ssh hands out.
func TestEd25519PointerAccepted(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(&priv, "ptr")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := k.Signer.(ed25519.PrivateKey); !ok {
		t.Fatalf("New did not normalise: %T", k.Signer)
	}
	direct := &PrivateKey{Signer: &priv, Comment: "ptr"}
	if direct.Type() != Ed25519 || direct.Bits() != 256 {
		t.Errorf("pointer key: %s/%d", direct.Type(), direct.Bits())
	}
	for _, m := range marshalers {
		data, err := m.marshal(direct, nil)
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		back, err := ParsePrivateKey(data, nil)
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		if _, ok := back.Signer.(ed25519.PrivateKey); !ok {
			t.Errorf("%s: parsed Ed25519 key is a %T", m.name, back.Signer)
		}
		requireSameKey(t, back, k)
	}
}

func TestAuthorizedKey(t *testing.T) {
	key := testKey(t, keySpec{Ed25519, 256})
	key.Comment = "me@example"
	line, err := key.AuthorizedKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := mustPublic(t, key)
	want := string(bytes.TrimSuffix(ssh.MarshalAuthorizedKey(pub), []byte("\n"))) + " me@example\n"
	if string(line) != want {
		t.Errorf("AuthorizedKey = %q, want %q", line, want)
	}
}
