package keys

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des" //nolint:gosec // 3des-cbc protected keys must remain readable
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/pem"
	"fmt"
	"math/big"

	"golang.org/x/crypto/chacha20"
	"golang.org/x/crypto/poly1305" //nolint:staticcheck // chacha20-poly1305@openssh.com is not the RFC 8439 AEAD
	"golang.org/x/crypto/ssh"
)

// This file implements the "openssh-key-v1" private key container described
// in OpenSSH's PROTOCOL.key:
//
//	"openssh-key-v1\0"
//	string  ciphername
//	string  kdfname
//	string  kdfoptions
//	uint32  number of keys (always 1)
//	string  public key
//	string  private section, encrypted and padded:
//	            uint32 check, uint32 check
//	            string key type, type-specific fields
//	            string comment
//	            padding 1, 2, 3, ...
//	[authentication tag, for AEAD ciphers]
//
// Parsing is done here rather than with ssh.ParseRawPrivateKey because the
// library discards the comment, has no DSA support and only decrypts
// aes256-ctr/cbc. Marshalling of RSA, ECDSA and Ed25519 keys is delegated to
// ssh.MarshalPrivateKey.

const (
	opensshMagic   = "openssh-key-v1\x00"
	pemTypeOpenSSH = "OPENSSH PRIVATE KEY"

	// maxBcryptRounds bounds the work factor accepted from a key file, as
	// golang.org/x/crypto/ssh does. ssh-keygen defaults to 16 (24 since
	// OpenSSH 10) and each round costs roughly 10 ms, so the limit caps a
	// decryption attempt at some tens of seconds.
	maxBcryptRounds = 2048
	// opensshWriteRounds is the work factor used for the keys we write.
	opensshWriteRounds = 16
)

// opensshFile is the decoded outer container of an OpenSSH private key.
type opensshFile struct {
	cipher  string
	kdf     string
	kdfOpts []byte
	public  []byte
	private []byte // private section, encrypted unless cipher is "none"
	tag     []byte // AEAD tag that trails the private section
}

func parseOpenSSHFile(der []byte) (*opensshFile, error) {
	if !bytes.HasPrefix(der, []byte(opensshMagic)) {
		return nil, fmt.Errorf("%w: missing openssh-key-v1 signature", ErrMalformed)
	}
	r := newWireReader(der[len(opensshMagic):])
	f := &opensshFile{
		cipher:  string(r.str()),
		kdf:     string(r.str()),
		kdfOpts: r.str(),
	}
	nkeys := r.uint32()
	f.public = r.str()
	f.private = r.str()
	f.tag = r.rest()
	if r.err != nil {
		return nil, fmt.Errorf("%w (OpenSSH private key)", r.err)
	}
	if nkeys != 1 {
		return nil, fmt.Errorf("%w: OpenSSH key file holds %d keys, expected 1", ErrUnsupported, nkeys)
	}
	return f, nil
}

func (f *opensshFile) encrypted() bool { return f.cipher != "none" }

// opensshCipher describes a cipher usable for private key encryption.
type opensshCipher struct {
	keyLen, ivLen, blockSize, tagLen int
}

var opensshCiphers = map[string]opensshCipher{
	"aes128-ctr":                    {16, 16, 16, 0},
	"aes192-ctr":                    {24, 16, 16, 0},
	"aes256-ctr":                    {32, 16, 16, 0},
	"aes128-cbc":                    {16, 16, 16, 0},
	"aes192-cbc":                    {24, 16, 16, 0},
	"aes256-cbc":                    {32, 16, 16, 0},
	"3des-cbc":                      {24, 8, 8, 0},
	"aes128-gcm@openssh.com":        {16, 12, 16, 16},
	"aes256-gcm@openssh.com":        {32, 12, 16, 16},
	"chacha20-poly1305@openssh.com": {64, 0, 8, 16},
}

// decrypt returns the plaintext private section. For an unencrypted key the
// result aliases the file contents; otherwise it is a fresh buffer that the
// caller should zero when done.
func (f *opensshFile) decrypt(passphrase []byte) ([]byte, error) {
	if !f.encrypted() {
		if f.kdf != "none" || len(f.kdfOpts) != 0 || len(f.tag) != 0 {
			return nil, fmt.Errorf("%w: inconsistent OpenSSH key header", ErrMalformed)
		}
		if len(f.private)%8 != 0 {
			return nil, fmt.Errorf("%w: OpenSSH private section has a bad length", ErrMalformed)
		}
		return f.private, nil
	}

	spec, ok := opensshCiphers[f.cipher]
	if !ok {
		return nil, fmt.Errorf("%w: OpenSSH key cipher %q", ErrUnsupported, f.cipher)
	}
	if f.kdf != "bcrypt" {
		return nil, fmt.Errorf("%w: OpenSSH key derivation %q", ErrUnsupported, f.kdf)
	}
	opts := newWireReader(f.kdfOpts)
	salt := opts.str()
	rounds := opts.uint32()
	if !opts.done() || len(salt) == 0 {
		return nil, fmt.Errorf("%w: bad bcrypt KDF options", ErrMalformed)
	}
	if rounds == 0 || rounds > maxBcryptRounds {
		return nil, fmt.Errorf("%w: bcrypt work factor %d is outside 1..%d", ErrUnsupported, rounds, maxBcryptRounds)
	}
	if len(f.private) < spec.blockSize || len(f.private)%spec.blockSize != 0 || len(f.tag) != spec.tagLen {
		return nil, fmt.Errorf("%w: OpenSSH private section has a bad length", ErrMalformed)
	}
	if len(passphrase) == 0 {
		return nil, ErrPassphraseRequired
	}

	km, err := bcryptPBKDF(passphrase, salt, int(rounds), spec.keyLen+spec.ivLen)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	defer zero(km)
	key, iv := km[:spec.keyLen], km[spec.keyLen:]

	plain := make([]byte, len(f.private))
	switch f.cipher {
	case "aes128-ctr", "aes192-ctr", "aes256-ctr":
		c, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		cipher.NewCTR(c, iv).XORKeyStream(plain, f.private)
	case "aes128-cbc", "aes192-cbc", "aes256-cbc":
		c, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		cipher.NewCBCDecrypter(c, iv).CryptBlocks(plain, f.private)
	case "3des-cbc":
		c, err := des.NewTripleDESCipher(key)
		if err != nil {
			return nil, err
		}
		cipher.NewCBCDecrypter(c, iv).CryptBlocks(plain, f.private)
	case "aes128-gcm@openssh.com", "aes256-gcm@openssh.com":
		c, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(c)
		if err != nil {
			return nil, err
		}
		sealed := append(append(make([]byte, 0, len(f.private)+len(f.tag)), f.private...), f.tag...)
		if _, err := aead.Open(plain[:0], iv, sealed, nil); err != nil {
			zero(plain)
			return nil, ErrWrongPassphrase
		}
	case "chacha20-poly1305@openssh.com":
		// OpenSSH's own construction with sequence number 0 and no
		// associated data: the first 32 key bytes key the payload cipher,
		// keystream block 0 yields the Poly1305 key and the payload is
		// encrypted from block 1 onwards.
		c, err := chacha20.NewUnauthenticatedCipher(key[:32], make([]byte, chacha20.NonceSize))
		if err != nil {
			return nil, err
		}
		var polyKey [32]byte
		var tag [16]byte
		c.XORKeyStream(polyKey[:], polyKey[:])
		copy(tag[:], f.tag)
		ok := poly1305.Verify(&tag, f.private, &polyKey)
		zero(polyKey[:])
		if !ok {
			return nil, ErrWrongPassphrase
		}
		c.SetCounter(1)
		c.XORKeyStream(plain, f.private)
	}
	return plain, nil
}

// parseOpenSSHPrivate decodes a complete OpenSSH private key (the bytes
// inside the PEM armour).
func parseOpenSSHPrivate(der, passphrase []byte) (crypto.Signer, string, error) {
	f, err := parseOpenSSHFile(der)
	if err != nil {
		return nil, "", err
	}
	plain, err := f.decrypt(passphrase)
	if err != nil {
		return nil, "", err
	}
	if f.encrypted() {
		defer zero(plain)
	}
	signer, comment, err := parseOpenSSHPrivateSection(plain, f.encrypted())
	if err != nil {
		return nil, "", err
	}
	// The public key is stored twice; a mismatch means the file was
	// tampered with or is corrupt.
	pub, err := publicKeyOf(signer)
	if err != nil {
		return nil, "", err
	}
	if subtle.ConstantTimeCompare(pub.Marshal(), f.public) != 1 {
		return nil, "", fmt.Errorf("%w: public and private halves of the OpenSSH key differ", ErrMalformed)
	}
	return signer, comment, nil
}

// parseOpenSSHPrivateSection decodes the decrypted private section.
func parseOpenSSHPrivateSection(plain []byte, encrypted bool) (crypto.Signer, string, error) {
	r := newWireReader(plain)
	check1, check2 := r.uint32(), r.uint32()
	if r.err != nil || check1 != check2 {
		if encrypted {
			return nil, "", ErrWrongPassphrase
		}
		return nil, "", fmt.Errorf("%w: OpenSSH private section check values differ", ErrMalformed)
	}

	keyType := string(r.str())
	if r.err != nil {
		return nil, "", r.err
	}
	var (
		signer crypto.Signer
		err    error
	)
	switch keyType {
	case ssh.KeyAlgoRSA:
		n, e, d := r.mpint(), r.mpint(), r.mpint()
		_ = r.mpint() // iqmp is recomputed
		p, q := r.mpint(), r.mpint()
		if r.err == nil {
			signer, err = newRSAPrivateKey(n, e, d, p, q)
		}
	case keyAlgoDSA:
		p, q, g, y, x := r.mpint(), r.mpint(), r.mpint(), r.mpint(), r.mpint()
		if r.err == nil {
			signer, err = newDSAPrivateKey(p, q, g, y, x)
		}
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		curve, point, d := string(r.str()), r.str(), r.mpint()
		if r.err == nil {
			if "ecdsa-sha2-"+curve != keyType {
				return nil, "", fmt.Errorf("%w: ECDSA curve %q does not match key type %q", ErrMalformed, curve, keyType)
			}
			signer, err = newECDSAPrivateKey(curve, point, d)
		}
	case ssh.KeyAlgoED25519:
		pub, priv := r.str(), r.str()
		if r.err == nil {
			signer, err = newEd25519PrivateKey(pub, priv)
		}
	default:
		return nil, "", fmt.Errorf("%w: OpenSSH private key type %q", ErrUnsupported, keyType)
	}
	if r.err != nil {
		return nil, "", r.err
	}
	if err != nil {
		return nil, "", err
	}

	comment := string(r.str())
	if r.err != nil {
		return nil, "", r.err
	}
	pad := r.rest()
	if len(pad) > 255 {
		return nil, "", fmt.Errorf("%w: excessive padding in OpenSSH private section", ErrMalformed)
	}
	for i, b := range pad {
		if int(b) != i+1 {
			return nil, "", fmt.Errorf("%w: bad padding in OpenSSH private section", ErrMalformed)
		}
	}
	return signer, comment, nil
}

// newRSAPrivateKey validates the components of a two-prime RSA key and
// assembles them. The CRT values are recomputed rather than trusted.
func newRSAPrivateKey(n, e, d, p, q *big.Int) (*rsa.PrivateKey, error) {
	switch {
	case n.BitLen() > MaxRSABits:
		return nil, fmt.Errorf("%w: %d-bit RSA modulus (limit %d)", ErrUnsupported, n.BitLen(), MaxRSABits)
	case n.Sign() <= 0 || d.Sign() <= 0 || p.Sign() <= 0 || q.Sign() <= 0:
		return nil, fmt.Errorf("%w: RSA key component missing", ErrMalformed)
	case p.BitLen() > n.BitLen() || q.BitLen() > n.BitLen() || d.BitLen() > n.BitLen():
		return nil, fmt.Errorf("%w: RSA key component too large", ErrMalformed)
	case e.BitLen() > 31 || e.Int64() < 3 || e.Bit(0) == 0:
		return nil, fmt.Errorf("%w: invalid RSA public exponent", ErrMalformed)
	}
	key := &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{N: n, E: int(e.Int64())},
		D:         d,
		Primes:    []*big.Int{p, q},
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	key.Precompute()
	return key, nil
}

// newECDSAPrivateKey builds an ECDSA key from its SSH components: the curve
// identifier, the public point Q in uncompressed form (may be nil) and the
// private scalar d.
func newECDSAPrivateKey(curveName string, point []byte, d *big.Int) (*ecdsa.PrivateKey, error) {
	curve, err := ecdsaCurveByName(curveName)
	if err != nil {
		return nil, err
	}
	size := (curve.Params().BitSize + 7) / 8
	if d.Sign() <= 0 || d.BitLen() > size*8 {
		return nil, fmt.Errorf("%w: ECDSA private scalar out of range", ErrMalformed)
	}
	raw := d.FillBytes(make([]byte, size))
	defer zero(raw)
	key, err := ecdsa.ParseRawPrivateKey(curve, raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if point != nil {
		derived, err := key.PublicKey.Bytes()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if !bytes.Equal(derived, point) {
			return nil, fmt.Errorf("%w: ECDSA public and private values do not match", ErrMalformed)
		}
	}
	return key, nil
}

// newEd25519PrivateKey builds an Ed25519 key from the OpenSSH representation:
// the 32-byte public key and the 64-byte seed||public private key.
func newEd25519PrivateKey(pub, priv []byte) (ed25519.PrivateKey, error) {
	if len(pub) != ed25519.PublicKeySize || len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: bad Ed25519 key length", ErrMalformed)
	}
	key := ed25519.NewKeyFromSeed(priv[:ed25519.SeedSize])
	if !bytes.Equal(key[ed25519.SeedSize:], pub) || !bytes.Equal(priv[ed25519.SeedSize:], pub) {
		zero(key)
		return nil, fmt.Errorf("%w: Ed25519 public and private values do not match", ErrMalformed)
	}
	return key, nil
}

// publicKeyOf returns the SSH public key of a (normalised) signer.
func publicKeyOf(s crypto.Signer) (ssh.PublicKey, error) {
	return (&PrivateKey{Signer: s}).PublicKey()
}

// marshalOpenSSH writes the key in OpenSSH's native format.
func (k *PrivateKey) marshalOpenSSH(passphrase []byte) ([]byte, error) {
	s, err := k.signer()
	if err != nil {
		return nil, err
	}
	comment := sanitizeComment(k.Comment)

	var block *pem.Block
	switch key := s.(type) {
	case *DSAPrivateKey:
		block, err = marshalOpenSSHDSA(key, comment, passphrase)
	case *rsa.PrivateKey:
		if err = checkRSAExportable(key); err != nil {
			return nil, err
		}
		block, err = marshalOpenSSHStd(key, comment, passphrase)
	default:
		block, err = marshalOpenSSHStd(s, comment, passphrase)
	}
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}

// marshalOpenSSHStd delegates to golang.org/x/crypto/ssh, which writes
// unencrypted keys or aes256-ctr with 16 rounds of bcrypt_pbkdf.
func marshalOpenSSHStd(key crypto.Signer, comment string, passphrase []byte) (*pem.Block, error) {
	var (
		block *pem.Block
		err   error
	)
	if len(passphrase) == 0 {
		block, err = ssh.MarshalPrivateKey(key, comment)
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, comment, passphrase)
	}
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return block, nil
}

// checkRSAExportable makes sure an RSA key can be written to formats that
// store exactly two primes and the CRT coefficient.
func checkRSAExportable(key *rsa.PrivateKey) error {
	if len(key.Primes) != 2 {
		return fmt.Errorf("%w: RSA keys with %d prime factors", ErrUnsupported, len(key.Primes))
	}
	if key.Precomputed.Qinv == nil {
		key.Precompute()
	}
	if key.Precomputed.Qinv == nil {
		return fmt.Errorf("%w: invalid RSA key", ErrMalformed)
	}
	return nil
}

// marshalOpenSSHDSA writes a DSA key, which ssh.MarshalPrivateKey cannot do.
// The layout and encryption parameters match those of the other key types.
func marshalOpenSSHDSA(key *DSAPrivateKey, comment string, passphrase []byte) (*pem.Block, error) {
	pub, err := newDSAPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}

	var check [4]byte
	if _, err := rand.Read(check[:]); err != nil {
		return nil, err
	}
	var priv wireWriter
	priv.raw(check[:])
	priv.raw(check[:])
	priv.text(keyAlgoDSA)
	priv.mpint(key.P)
	priv.mpint(key.Q)
	priv.mpint(key.G)
	priv.mpint(key.Y)
	priv.mpint(key.X)
	priv.text(comment)
	defer func() { zero(priv.buf) }()

	cipherName, kdfName := "none", "none"
	var kdfOpts []byte
	blockSize := 8
	if len(passphrase) > 0 {
		cipherName, kdfName, blockSize = "aes256-ctr", "bcrypt", aes.BlockSize
	}
	for i := 1; len(priv.buf)%blockSize != 0; i++ {
		priv.buf = append(priv.buf, byte(i))
	}

	section := priv.buf
	if len(passphrase) > 0 {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		var opts wireWriter
		opts.str(salt)
		opts.uint32(opensshWriteRounds)
		kdfOpts = opts.bytes()

		km, err := bcryptPBKDF(passphrase, salt, opensshWriteRounds, 32+aes.BlockSize)
		if err != nil {
			return nil, fmt.Errorf("keys: %w", err)
		}
		defer zero(km)
		c, err := aes.NewCipher(km[:32])
		if err != nil {
			return nil, err
		}
		section = make([]byte, len(priv.buf))
		cipher.NewCTR(c, km[32:]).XORKeyStream(section, priv.buf)
	}

	var out wireWriter
	out.raw([]byte(opensshMagic))
	out.text(cipherName)
	out.text(kdfName)
	out.str(kdfOpts)
	out.uint32(1)
	out.str(pub.Marshal())
	out.str(section)
	return &pem.Block{Type: pemTypeOpenSSH, Bytes: out.bytes()}, nil
}
