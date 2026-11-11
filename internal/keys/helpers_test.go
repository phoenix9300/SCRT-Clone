package keys

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fixturePass is the passphrase of every encrypted file under testdata.
const fixturePass = "hawser-test"

// keySpec names a key type and size used across the tests.
type keySpec struct {
	typ  Type
	bits int
}

func (s keySpec) String() string {
	return fmt.Sprintf("%s-%d", s.typ, s.bits)
}

// allSpecs is every type and size Generate supports out of the box.
var allSpecs = []keySpec{
	{RSA, 2048}, {RSA, 3072}, {RSA, 4096},
	{ECDSA, 256}, {ECDSA, 384}, {ECDSA, 521},
	{Ed25519, 256},
}

// quickSpecs is one representative key per algorithm, for tests where the
// key size makes no difference.
var quickSpecs = []keySpec{{RSA, 2048}, {ECDSA, 256}, {ECDSA, 384}, {ECDSA, 521}, {Ed25519, 256}}

var (
	keyCacheMu sync.Mutex
	keyCache   = map[keySpec]*PrivateKey{}
)

// testKey returns a generated key of the given kind. Keys are generated once
// per test binary because large RSA keys are slow to make. The returned
// struct is a copy, so tests may change its Comment.
func testKey(t testing.TB, s keySpec) *PrivateKey {
	t.Helper()
	keyCacheMu.Lock()
	defer keyCacheMu.Unlock()
	k, ok := keyCache[s]
	if !ok {
		var err error
		if k, err = Generate(s.typ, s.bits); err != nil {
			t.Fatalf("Generate(%v): %v", s, err)
		}
		keyCache[s] = k
	}
	cp := *k
	cp.Comment = s.String() + "@hawser"
	return &cp
}

// fixture reads a file from testdata.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fixtureKey loads one of the unencrypted OpenSSH fixtures, which is how the
// tests get hold of a DSA key.
func fixtureKey(t testing.TB, base string) *PrivateKey {
	t.Helper()
	k, err := ParsePrivateKey(fixture(t, base+".ossh"), nil)
	if err != nil {
		t.Fatalf("loading fixture %s: %v", base, err)
	}
	return k
}

// mustPublic returns the SSH public key of k.
func mustPublic(t testing.TB, k *PrivateKey) ssh.PublicKey {
	t.Helper()
	pub, err := k.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	return pub
}

// mustParsePublic parses a public key in any supported encoding.
func mustParsePublic(t testing.TB, data []byte) ssh.PublicKey {
	t.Helper()
	pub, _, err := ParsePublicKey(data)
	if err != nil {
		t.Fatalf("ParsePublicKey: %v\n%s", err, data)
	}
	return pub
}

func samePublic(a, b ssh.PublicKey) bool {
	return a != nil && b != nil && bytes.Equal(a.Marshal(), b.Marshal())
}

// samePrivate compares two private keys for equality of the key material.
func samePrivate(a, b crypto.Signer) bool {
	if ra, ok := a.(*rsa.PrivateKey); ok {
		// rsa.PrivateKey.Equal is sensitive to the order of the primes,
		// which is arbitrary: Go generates them in random order while PuTTY
		// (and so our PPK writer) always stores the larger one first.
		rb, ok := b.(*rsa.PrivateKey)
		if !ok || !ra.PublicKey.Equal(&rb.PublicKey) || ra.D.Cmp(rb.D) != 0 ||
			len(ra.Primes) != 2 || len(rb.Primes) != 2 {
			return false
		}
		p, q := ra.Primes[0], ra.Primes[1]
		return (p.Cmp(rb.Primes[0]) == 0 && q.Cmp(rb.Primes[1]) == 0) ||
			(p.Cmp(rb.Primes[1]) == 0 && q.Cmp(rb.Primes[0]) == 0)
	}
	eq, ok := a.(interface{ Equal(crypto.PrivateKey) bool })
	return ok && eq.Equal(b)
}

// requireSameKey fails the test unless got holds the same key as want.
func requireSameKey(t testing.TB, got, want *PrivateKey) {
	t.Helper()
	if got == nil {
		t.Fatal("no key")
	}
	if !samePrivate(got.Signer, want.Signer) {
		t.Fatalf("private key differs: got %T, want %T", got.Signer, want.Signer)
	}
	if !samePublic(mustPublic(t, got), mustPublic(t, want)) {
		t.Fatal("public key differs")
	}
}

// requireErr fails the test unless err wraps target.
func requireErr(t testing.TB, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got error %v, want %v", err, target)
	}
}

// keyFields returns the first two fields (type and base64 blob) of an
// authorized_keys line, i.e. the line without its comment.
func keyFields(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return line
	}
	return f[0] + " " + f[1]
}

// needTool returns the path of an external program or skips the test.
func needTool(t testing.TB, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed; skipping interoperability test", name)
	}
	return path
}

// tryTool runs an external program and returns its standard output.
func tryTool(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Stdin = nil
	// Keep the tools from picking up the user's locale, agent or config.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "SSH_AUTH_SOCK=", "SSH_ASKPASS=/bin/false", "DISPLAY=")
	if err := cmd.Run(); err != nil {
		return stdout.String(), errors.New(name + " " + strings.Join(args, " ") + ": " + err.Error() + ": " + strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runTool is tryTool for commands that must succeed.
func runTool(t testing.TB, name string, args ...string) string {
	t.Helper()
	out, err := tryTool(name, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// writeTemp writes data to a new file with the permissions ssh-keygen
// demands of private keys and returns its path.
func writeTemp(t testing.TB, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
