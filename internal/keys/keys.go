package keys

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// Type identifies a key algorithm family.
type Type string

// Key algorithm families.
const (
	RSA     Type = "rsa"
	ECDSA   Type = "ecdsa"
	Ed25519 Type = "ed25519"
	// DSA keys are legacy. They can be parsed, inspected and converted
	// between formats but [Generate] refuses to create them.
	DSA Type = "dsa"
)

// Format identifies a private key container format.
type Format string

// Private key container formats.
const (
	// FormatOpenSSH is the "-----BEGIN OPENSSH PRIVATE KEY-----" format
	// written by ssh-keygen since OpenSSH 7.8.
	FormatOpenSSH Format = "openssh"
	// FormatPEM covers the classic PEM encodings: PKCS#1 (RSA), SEC1 (EC),
	// OpenSSL DSA and PKCS#8, with or without encryption.
	FormatPEM Format = "pem"
	// FormatPPK is PuTTY's .ppk format, versions 2 and 3.
	FormatPPK Format = "ppk"
)

// Sentinel errors. Errors returned by this package wrap one of these where
// applicable, so test for them with errors.Is.
var (
	// ErrPassphraseRequired reports that the key is encrypted and no
	// passphrase was supplied.
	ErrPassphraseRequired = errors.New("keys: private key is encrypted and no passphrase was supplied")
	// ErrWrongPassphrase reports that a passphrase was supplied but did not
	// decrypt the key (MAC, padding or check-value mismatch).
	ErrWrongPassphrase = errors.New("keys: wrong passphrase")
	// ErrUnknownFormat reports that the data is not in any key format this
	// package recognises.
	ErrUnknownFormat = errors.New("keys: unrecognised key format")
	// ErrMalformed reports that the data is in a recognised format but is
	// truncated, corrupt or internally inconsistent.
	ErrMalformed = errors.New("keys: malformed key data")
	// ErrUnsupported reports a recognised but unsupported feature, such as
	// an unknown cipher, an Argon2d-protected PPK file or a hardware-backed
	// key type.
	ErrUnsupported = errors.New("keys: unsupported")
	// ErrMACMismatch reports that the MAC of a PPK file did not verify. For
	// an encrypted file the error also wraps ErrWrongPassphrase, because a
	// wrong passphrase and a corrupted file cannot be told apart.
	ErrMACMismatch = errors.New("keys: PPK integrity check failed (MAC mismatch)")
)

// PrivateKey is a private key together with the metadata key files carry.
type PrivateKey struct {
	// Signer is the key itself: *rsa.PrivateKey, *ecdsa.PrivateKey (P-256,
	// P-384 or P-521), ed25519.PrivateKey or *DSAPrivateKey. Parsers in this
	// package always produce the ed25519.PrivateKey value type; a
	// *ed25519.PrivateKey supplied by the caller is accepted and
	// dereferenced by every method.
	Signer crypto.Signer
	// Comment is the free-text label stored in OpenSSH and PPK files. PEM
	// files cannot carry one.
	Comment string
	// Format is the container the key was parsed from. It is empty for
	// generated keys.
	Format Format
}

// New wraps an existing key. signer must be one of the types documented on
// [PrivateKey].
func New(signer crypto.Signer, comment string) (*PrivateKey, error) {
	s, err := normalizeSigner(signer)
	if err != nil {
		return nil, err
	}
	return &PrivateKey{Signer: s, Comment: comment}, nil
}

// Default and accepted key sizes for Generate.
const (
	DefaultRSABits   = 3072
	DefaultECDSABits = 256
	// MinRSABits is the smallest RSA modulus Generate will create.
	MinRSABits = 2048
	// MaxRSABits is the largest RSA modulus this package generates or
	// loads; it matches the limit of OpenSSH.
	MaxRSABits = 16384
)

// Generate creates a new private key.
//
// For RSA, bits is the modulus size: 2048, 3072 or 4096 are the usual
// choices and 0 selects 3072. Any multiple of 8 from 2048 to 16384 is
// accepted. For ECDSA, bits selects the curve: 256, 384 or 521, with 0
// meaning 256. bits is ignored for Ed25519. DSA keys cannot be generated.
func Generate(t Type, bits int) (*PrivateKey, error) {
	switch t {
	case RSA:
		if bits == 0 {
			bits = DefaultRSABits
		}
		if bits < MinRSABits || bits > MaxRSABits || bits%8 != 0 {
			return nil, fmt.Errorf("keys: invalid RSA key size %d (use 2048, 3072 or 4096)", bits)
		}
		k, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return nil, fmt.Errorf("keys: generating RSA key: %w", err)
		}
		return &PrivateKey{Signer: k}, nil
	case ECDSA:
		if bits == 0 {
			bits = DefaultECDSABits
		}
		var curve elliptic.Curve
		switch bits {
		case 256:
			curve = elliptic.P256()
		case 384:
			curve = elliptic.P384()
		case 521:
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("keys: invalid ECDSA key size %d (use 256, 384 or 521)", bits)
		}
		k, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("keys: generating ECDSA key: %w", err)
		}
		return &PrivateKey{Signer: k}, nil
	case Ed25519:
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("keys: generating Ed25519 key: %w", err)
		}
		return &PrivateKey{Signer: k}, nil
	case DSA:
		return nil, fmt.Errorf("%w: DSA keys are obsolete and cannot be generated", ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: unknown key type %q", ErrUnsupported, t)
	}
}

// normalizeSigner validates that s is a key type this package handles and
// returns its canonical representation (Ed25519 keys as a value).
func normalizeSigner(s crypto.Signer) (crypto.Signer, error) {
	switch k := s.(type) {
	case nil:
		return nil, errors.New("keys: no private key")
	case *rsa.PrivateKey:
		if k == nil || k.N == nil || k.D == nil {
			return nil, errors.New("keys: incomplete RSA private key")
		}
		return k, nil
	case *ecdsa.PrivateKey:
		if k == nil || k.Curve == nil {
			return nil, errors.New("keys: incomplete ECDSA private key")
		}
		if _, err := ecdsaCurveName(k.Curve); err != nil {
			return nil, err
		}
		return k, nil
	case ed25519.PrivateKey:
		if len(k) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("keys: invalid Ed25519 private key length %d", len(k))
		}
		return k, nil
	case *ed25519.PrivateKey:
		if k == nil || len(*k) != ed25519.PrivateKeySize {
			return nil, errors.New("keys: invalid Ed25519 private key")
		}
		return *k, nil
	case *DSAPrivateKey:
		if k == nil || k.P == nil || k.Q == nil || k.G == nil || k.Y == nil || k.X == nil {
			return nil, errors.New("keys: incomplete DSA private key")
		}
		return k, nil
	default:
		return nil, fmt.Errorf("%w: private key type %T", ErrUnsupported, s)
	}
}

// signer returns the validated, canonical key.
func (k *PrivateKey) signer() (crypto.Signer, error) {
	if k == nil {
		return nil, errors.New("keys: nil private key")
	}
	return normalizeSigner(k.Signer)
}

// ecdsaCurveName maps a curve to its SSH identifier ("nistp256", ...).
func ecdsaCurveName(c elliptic.Curve) (string, error) {
	switch c {
	case elliptic.P256():
		return "nistp256", nil
	case elliptic.P384():
		return "nistp384", nil
	case elliptic.P521():
		return "nistp521", nil
	}
	name := "unknown"
	if c != nil && c.Params() != nil {
		name = c.Params().Name
	}
	return "", fmt.Errorf("%w: elliptic curve %s (SSH uses P-256, P-384 and P-521)", ErrUnsupported, name)
}

// ecdsaCurveByName is the inverse of ecdsaCurveName.
func ecdsaCurveByName(name string) (elliptic.Curve, error) {
	switch name {
	case "nistp256":
		return elliptic.P256(), nil
	case "nistp384":
		return elliptic.P384(), nil
	case "nistp521":
		return elliptic.P521(), nil
	}
	return nil, fmt.Errorf("%w: elliptic curve %q", ErrUnsupported, name)
}

// Type returns the key's algorithm family, or "" if the key is missing or of
// an unsupported type.
func (k *PrivateKey) Type() Type {
	s, err := k.signer()
	if err != nil {
		return ""
	}
	switch s.(type) {
	case *rsa.PrivateKey:
		return RSA
	case *ecdsa.PrivateKey:
		return ECDSA
	case ed25519.PrivateKey:
		return Ed25519
	case *DSAPrivateKey:
		return DSA
	}
	return ""
}

// Bits returns the key size as ssh-keygen reports it: the modulus length for
// RSA and DSA, the curve size for ECDSA and 256 for Ed25519. It returns 0 if
// the key is missing or of an unsupported type.
func (k *PrivateKey) Bits() int {
	s, err := k.signer()
	if err != nil {
		return 0
	}
	switch key := s.(type) {
	case *rsa.PrivateKey:
		return key.N.BitLen()
	case *ecdsa.PrivateKey:
		return key.Curve.Params().BitSize
	case ed25519.PrivateKey:
		return 256
	case *DSAPrivateKey:
		return key.P.BitLen()
	}
	return 0
}

// PublicKey returns the SSH public key corresponding to k.
func (k *PrivateKey) PublicKey() (ssh.PublicKey, error) {
	s, err := k.signer()
	if err != nil {
		return nil, err
	}
	if d, ok := s.(*DSAPrivateKey); ok {
		return newDSAPublicKey(&d.PublicKey)
	}
	pub, err := ssh.NewPublicKey(s.Public())
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return pub, nil
}

// SSHSigner returns an ssh.Signer for use with golang.org/x/crypto/ssh
// client authentication. For RSA keys the signer also implements
// ssh.MultiAlgorithmSigner and offers rsa-sha2-256, rsa-sha2-512 and ssh-rsa.
// DSA signers produce "ssh-dss" signatures, which current servers reject.
func (k *PrivateKey) SSHSigner() (ssh.Signer, error) {
	s, err := k.signer()
	if err != nil {
		return nil, err
	}
	if d, ok := s.(*DSAPrivateKey); ok {
		return newDSASSHSigner(d)
	}
	signer, err := ssh.NewSignerFromSigner(s)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return signer, nil
}

// AuthorizedKey returns the public key as an authorized_keys line carrying
// the key's comment.
func (k *PrivateKey) AuthorizedKey() ([]byte, error) {
	pub, err := k.PublicKey()
	if err != nil {
		return nil, err
	}
	return MarshalAuthorizedKey(pub, k.Comment), nil
}

// Marshal serialises the key in the given container format. An empty
// passphrase writes the key unencrypted.
//
//   - FormatOpenSSH: openssh-key-v1; encrypted keys use aes256-ctr with
//     bcrypt_pbkdf, like ssh-keygen.
//   - FormatPEM: the traditional encoding, see [PrivateKey.MarshalPEM] with
//     [PEMTraditional].
//   - FormatPPK: PuTTY version 3, see [PrivateKey.MarshalPPK].
func (k *PrivateKey) Marshal(f Format, passphrase []byte) ([]byte, error) {
	switch f {
	case FormatOpenSSH:
		return k.marshalOpenSSH(passphrase)
	case FormatPEM:
		return k.MarshalPEM(PEMTraditional, passphrase)
	case FormatPPK:
		return k.MarshalPPK(3, passphrase)
	}
	return nil, fmt.Errorf("%w: cannot write format %q", ErrUnknownFormat, f)
}
