package keys

import (
	"crypto"
	"crypto/dsa" //nolint:staticcheck // legacy keys must still be readable
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // mandated by the ssh-dss signature scheme
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"

	"golang.org/x/crypto/ssh"
)

// keyAlgoDSA is the SSH name of DSA keys. golang.org/x/crypto/ssh no longer
// implements the algorithm, so this package carries the little that is needed
// to read, describe and convert such keys.
const keyAlgoDSA = "ssh-dss"

// DSAPrivateKey adapts a legacy DSA private key to crypto.Signer so that it
// can be carried in [PrivateKey.Signer]. DSA is obsolete: keys can be parsed,
// inspected and converted to other formats, but not generated.
type DSAPrivateKey struct {
	dsa.PrivateKey
}

// Public returns the *dsa.PublicKey half of the key.
func (k *DSAPrivateKey) Public() crypto.PublicKey {
	return &k.PrivateKey.PublicKey
}

// Sign signs digest and returns the ASN.1 DER encoding of (r, s), the
// convention crypto.Signer uses for DSA-family signatures. opts is ignored;
// the caller is responsible for hashing.
func (k *DSAPrivateKey) Sign(rnd io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	r, s, err := dsa.Sign(rnd, &k.PrivateKey, digest)
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(struct{ R, S *big.Int }{r, s})
}

// Equal reports whether x is the same DSA private key.
func (k *DSAPrivateKey) Equal(x crypto.PrivateKey) bool {
	o, ok := x.(*DSAPrivateKey)
	if !ok || k == nil || o == nil {
		return false
	}
	return bigEqual(k.P, o.P) && bigEqual(k.Q, o.Q) && bigEqual(k.G, o.G) &&
		bigEqual(k.Y, o.Y) && bigEqual(k.X, o.X)
}

func bigEqual(a, b *big.Int) bool {
	return a != nil && b != nil && a.Cmp(b) == 0
}

// Limits on DSA domain parameters accepted from key files. Real keys use a
// 1024-bit p with a 160-bit q (2048/256 is the largest size ever deployed).
const (
	minDSAModulusBits  = 512
	maxDSAModulusBits  = 8192
	minDSASubgroupBits = 160
	maxDSASubgroupBits = 512
)

// checkDSAPublic validates the public parameters of a DSA key.
func checkDSAPublic(p, q, g, y *big.Int) error {
	switch {
	case p == nil || q == nil || g == nil || y == nil:
		return fmt.Errorf("%w: incomplete DSA key", ErrMalformed)
	case p.BitLen() < minDSAModulusBits || p.BitLen() > maxDSAModulusBits || p.Bit(0) == 0:
		return fmt.Errorf("%w: invalid DSA modulus (%d bits)", ErrMalformed, p.BitLen())
	case q.BitLen() < minDSASubgroupBits || q.BitLen() > maxDSASubgroupBits || q.Bit(0) == 0 || q.Cmp(p) >= 0:
		return fmt.Errorf("%w: invalid DSA subgroup order (%d bits)", ErrMalformed, q.BitLen())
	case g.Cmp(big.NewInt(1)) <= 0 || g.Cmp(p) >= 0:
		return fmt.Errorf("%w: invalid DSA generator", ErrMalformed)
	case y.Cmp(big.NewInt(1)) <= 0 || y.Cmp(p) >= 0:
		return fmt.Errorf("%w: invalid DSA public value", ErrMalformed)
	}
	return nil
}

// newDSAPrivateKey validates the parameters and builds a key. y may be nil,
// in which case it is derived from x; otherwise it must match.
func newDSAPrivateKey(p, q, g, y, x *big.Int) (*DSAPrivateKey, error) {
	if x == nil || p == nil || q == nil || g == nil {
		return nil, fmt.Errorf("%w: incomplete DSA key", ErrMalformed)
	}
	if x.Sign() <= 0 || x.Cmp(q) >= 0 {
		return nil, fmt.Errorf("%w: DSA private value out of range", ErrMalformed)
	}
	// Check everything that does not depend on y before the modular
	// exponentiation, so that hostile parameters cannot make it expensive.
	probe := y
	if probe == nil {
		probe = big.NewInt(2)
	}
	if err := checkDSAPublic(p, q, g, probe); err != nil {
		return nil, err
	}
	derived := new(big.Int).Exp(g, x, p)
	if y == nil {
		y = derived
		if err := checkDSAPublic(p, q, g, y); err != nil {
			return nil, err
		}
	} else if y.Cmp(derived) != 0 {
		return nil, fmt.Errorf("%w: DSA public and private values do not match", ErrMalformed)
	}
	return &DSAPrivateKey{dsa.PrivateKey{
		PublicKey: dsa.PublicKey{
			Parameters: dsa.Parameters{P: p, Q: q, G: g},
			Y:          y,
		},
		X: x,
	}}, nil
}

// dsaPublicKey implements ssh.PublicKey for "ssh-dss" keys.
type dsaPublicKey struct {
	key dsa.PublicKey
}

func newDSAPublicKey(k *dsa.PublicKey) (ssh.PublicKey, error) {
	if k == nil {
		return nil, fmt.Errorf("%w: incomplete DSA key", ErrMalformed)
	}
	if err := checkDSAPublic(k.P, k.Q, k.G, k.Y); err != nil {
		return nil, err
	}
	return &dsaPublicKey{key: *k}, nil
}

// parseDSAPublicBlob decodes an "ssh-dss" public key blob.
func parseDSAPublicBlob(blob []byte) (ssh.PublicKey, error) {
	r := newWireReader(blob)
	if algo := string(r.str()); r.err == nil && algo != keyAlgoDSA {
		return nil, fmt.Errorf("%w: not an ssh-dss key", ErrMalformed)
	}
	p, q, g, y := r.mpint(), r.mpint(), r.mpint(), r.mpint()
	if r.err != nil {
		return nil, r.err
	}
	if !r.done() {
		return nil, fmt.Errorf("%w: trailing data after DSA public key", ErrMalformed)
	}
	return newDSAPublicKey(&dsa.PublicKey{Parameters: dsa.Parameters{P: p, Q: q, G: g}, Y: y})
}

// Type returns "ssh-dss".
func (k *dsaPublicKey) Type() string { return keyAlgoDSA }

// Marshal returns the SSH wire encoding of the key.
func (k *dsaPublicKey) Marshal() []byte {
	var w wireWriter
	w.text(keyAlgoDSA)
	w.mpint(k.key.P)
	w.mpint(k.key.Q)
	w.mpint(k.key.G)
	w.mpint(k.key.Y)
	return w.bytes()
}

// Verify checks an "ssh-dss" signature (RFC 4253 section 6.6) over data.
func (k *dsaPublicKey) Verify(data []byte, sig *ssh.Signature) error {
	if sig == nil || sig.Format != keyAlgoDSA {
		return errors.New("keys: signature is not of type ssh-dss")
	}
	if len(sig.Blob) != 40 {
		return errors.New("keys: malformed ssh-dss signature")
	}
	digest := sha1.Sum(data) //nolint:gosec
	r := new(big.Int).SetBytes(sig.Blob[:20])
	s := new(big.Int).SetBytes(sig.Blob[20:])
	if !dsa.Verify(&k.key, digest[:], r, s) {
		return errors.New("keys: ssh-dss signature verification failed")
	}
	return nil
}

// CryptoPublicKey returns the underlying *dsa.PublicKey.
func (k *dsaPublicKey) CryptoPublicKey() crypto.PublicKey {
	pub := k.key
	return &pub
}

// dsaSSHSigner implements ssh.Signer for DSA keys.
type dsaSSHSigner struct {
	key *DSAPrivateKey
	pub ssh.PublicKey
}

func newDSASSHSigner(k *DSAPrivateKey) (ssh.Signer, error) {
	pub, err := newDSAPublicKey(&k.PublicKey)
	if err != nil {
		return nil, err
	}
	if k.Q.BitLen() != 160 {
		return nil, fmt.Errorf("%w: ssh-dss signatures need a 160-bit subgroup, key has %d bits", ErrUnsupported, k.Q.BitLen())
	}
	return &dsaSSHSigner{key: k, pub: pub}, nil
}

func (s *dsaSSHSigner) PublicKey() ssh.PublicKey { return s.pub }

func (s *dsaSSHSigner) Sign(rnd io.Reader, data []byte) (*ssh.Signature, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	digest := sha1.Sum(data) //nolint:gosec
	r, sv, err := dsa.Sign(rnd, &s.key.PrivateKey, digest[:])
	if err != nil {
		return nil, err
	}
	blob := make([]byte, 40)
	r.FillBytes(blob[:20])
	sv.FillBytes(blob[20:])
	return &ssh.Signature{Format: keyAlgoDSA, Blob: blob}, nil
}
