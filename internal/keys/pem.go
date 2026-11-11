package keys

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des" //nolint:gosec // legacy PEM encryption must remain readable
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/md5" //nolint:gosec // mandated by the legacy PEM key derivation
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // PBKDF2 default PRF
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"hash"
	"math/big"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/scrypt"
)

// PEM block labels of the classic private key encodings.
const (
	pemTypeRSA            = "RSA PRIVATE KEY"       // PKCS#1
	pemTypeEC             = "EC PRIVATE KEY"        // SEC1
	pemTypeDSA            = "DSA PRIVATE KEY"       // OpenSSL's DSA encoding
	pemTypePKCS8          = "PRIVATE KEY"           // PKCS#8 PrivateKeyInfo
	pemTypePKCS8Encrypted = "ENCRYPTED PRIVATE KEY" // PKCS#8 EncryptedPrivateKeyInfo
)

// PEMStyle selects between the two families of PEM private key encodings.
type PEMStyle int

const (
	// PEMTraditional is what "ssh-keygen -m PEM" and older OpenSSL write:
	// PKCS#1 "RSA PRIVATE KEY", SEC1 "EC PRIVATE KEY" and OpenSSL
	// "DSA PRIVATE KEY". With a passphrase the block is protected by the
	// legacy "Proc-Type: 4,ENCRYPTED" scheme using AES-128-CBC, exactly as
	// ssh-keygen does; it is the one encrypted PEM flavour that PuTTYgen,
	// OpenSSL and practically every SSH library can read. That scheme
	// stretches the passphrase with a single round of MD5 and does not
	// authenticate the ciphertext, so prefer PEMPKCS8 or FormatOpenSSH for
	// encrypted keys unless a consumer requires it.
	//
	// Ed25519 has no traditional encoding and is written as PKCS#8.
	PEMTraditional PEMStyle = iota
	// PEMPKCS8 is what "ssh-keygen -m PKCS8" and current OpenSSL write:
	// "PRIVATE KEY", or with a passphrase "ENCRYPTED PRIVATE KEY" using
	// PBES2 with PBKDF2-HMAC-SHA256 and AES-256-CBC.
	PEMPKCS8
)

// Work factor limits for encrypted PEM files, and the parameters used when
// writing encrypted PKCS#8.
const (
	maxPBKDF2Iterations = 10_000_000
	maxScryptMemory     = 1 << 30 // bytes
	pkcs8Iterations     = 600_000
	pkcs8SaltLen        = 16
)

var (
	oidPBES2      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidScrypt     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11591, 4, 11}
	oidPBEMD5DES  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 3}
	oidPBESHA1DES = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 10}

	oidHMACSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA224 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 8}
	oidHMACSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACSHA384 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 10}
	oidHMACSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}

	oidAES128CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oidDESEDE3CBC = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}
	oidDESCBC     = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 7}

	oidKeyDSA = asn1.ObjectIdentifier{1, 2, 840, 10040, 4, 1}
)

// cbcCipher describes a block cipher used in CBC mode by the PEM formats.
type cbcCipher struct {
	name     string // DEK-Info name
	keyLen   int
	newBlock func([]byte) (cipher.Block, error)
}

var (
	cbcAES128 = cbcCipher{"AES-128-CBC", 16, aes.NewCipher}
	cbcAES192 = cbcCipher{"AES-192-CBC", 24, aes.NewCipher}
	cbcAES256 = cbcCipher{"AES-256-CBC", 32, aes.NewCipher}
	cbc3DES   = cbcCipher{"DES-EDE3-CBC", 24, des.NewTripleDESCipher}
	cbcDES    = cbcCipher{"DES-CBC", 8, des.NewCipher}
)

// legacyPEMEncrypted reports whether block uses RFC 1421 encryption.
func legacyPEMEncrypted(block *pem.Block) bool {
	return strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED")
}

// legacyPEMParams decodes the DEK-Info header of an encrypted block.
func legacyPEMParams(block *pem.Block) (cbcCipher, []byte, error) {
	dek, ok := block.Headers["DEK-Info"]
	if !ok {
		return cbcCipher{}, nil, fmt.Errorf("%w: encrypted PEM block without DEK-Info header", ErrMalformed)
	}
	name, ivHex, ok := strings.Cut(dek, ",")
	if !ok {
		return cbcCipher{}, nil, fmt.Errorf("%w: bad DEK-Info header", ErrMalformed)
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	var spec cbcCipher
	switch name {
	case cbcAES128.name:
		spec = cbcAES128
	case cbcAES192.name:
		spec = cbcAES192
	case cbcAES256.name:
		spec = cbcAES256
	case cbc3DES.name:
		spec = cbc3DES
	case cbcDES.name:
		spec = cbcDES
	default:
		return cbcCipher{}, nil, fmt.Errorf("%w: PEM cipher %q", ErrUnsupported, name)
	}
	iv, err := hex.DecodeString(strings.TrimSpace(ivHex))
	if err != nil || len(iv) < 8 {
		return cbcCipher{}, nil, fmt.Errorf("%w: bad DEK-Info initialisation vector", ErrMalformed)
	}
	return spec, iv, nil
}

// legacyPEMKey is OpenSSL's EVP_BytesToKey with MD5 and one iteration: the
// derivation used by "Proc-Type: 4,ENCRYPTED" PEM blocks.
func legacyPEMKey(passphrase, salt []byte, keyLen int) []byte {
	var out, prev []byte
	h := md5.New() //nolint:gosec
	for len(out) < keyLen {
		h.Reset()
		h.Write(prev)
		h.Write(passphrase)
		h.Write(salt)
		prev = h.Sum(prev[:0])
		out = append(out, prev...)
	}
	zero(prev)
	zero(out[keyLen:])
	return out[:keyLen]
}

// cbcDecrypt decrypts data and strips PKCS#7 padding. Any failure is
// reported as a wrong passphrase: neither PEM scheme has an integrity check,
// so bad padding is the only available signal.
func cbcDecrypt(spec cbcCipher, key, iv, data []byte) ([]byte, error) {
	c, err := spec.newBlock(key)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	bs := c.BlockSize()
	if len(iv) != bs {
		return nil, fmt.Errorf("%w: initialisation vector has the wrong length", ErrMalformed)
	}
	if len(data) == 0 || len(data)%bs != 0 {
		return nil, fmt.Errorf("%w: encrypted data is not a whole number of cipher blocks", ErrMalformed)
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(c, iv).CryptBlocks(plain, data)

	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > bs {
		zero(plain)
		return nil, ErrWrongPassphrase
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			zero(plain)
			return nil, ErrWrongPassphrase
		}
	}
	return plain[:len(plain)-pad], nil
}

// cbcEncrypt applies PKCS#7 padding and encrypts.
func cbcEncrypt(spec cbcCipher, key, iv, plain []byte) ([]byte, error) {
	c, err := spec.newBlock(key)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	bs := c.BlockSize()
	pad := bs - len(plain)%bs
	padded := make([]byte, len(plain)+pad)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	cipher.NewCBCEncrypter(c, iv).CryptBlocks(padded, padded)
	return padded, nil
}

// decryptLegacyPEM removes RFC 1421 encryption from block.
func decryptLegacyPEM(block *pem.Block, passphrase []byte) ([]byte, error) {
	spec, iv, err := legacyPEMParams(block)
	if err != nil {
		return nil, err
	}
	if len(passphrase) == 0 {
		return nil, ErrPassphraseRequired
	}
	key := legacyPEMKey(passphrase, iv[:8], spec.keyLen)
	defer zero(key)
	return cbcDecrypt(spec, key, iv, block.Bytes)
}

// encryptLegacyPEM wraps der in an RFC 1421 encrypted block. AES-128-CBC is
// what ssh-keygen writes and, besides 3DES, the only cipher PuTTYgen imports.
func encryptLegacyPEM(blockType string, der, passphrase []byte) (*pem.Block, error) {
	spec := cbcAES128
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	key := legacyPEMKey(passphrase, iv[:8], spec.keyLen)
	defer zero(key)
	enc, err := cbcEncrypt(spec, key, iv, der)
	if err != nil {
		return nil, err
	}
	return &pem.Block{
		Type: blockType,
		Headers: map[string]string{
			"Proc-Type": "4,ENCRYPTED",
			"DEK-Info":  spec.name + "," + strings.ToUpper(hex.EncodeToString(iv)),
		},
		Bytes: enc,
	}, nil
}

// isPEMPrivateType reports whether a PEM label is one of the private key
// encodings handled by parsePEMPrivate.
func isPEMPrivateType(t string) bool {
	switch t {
	case pemTypeRSA, pemTypeEC, pemTypeDSA, pemTypePKCS8, pemTypePKCS8Encrypted:
		return true
	}
	return false
}

// pemEncrypted reports whether a classic PEM private key block needs a
// passphrase.
func pemEncrypted(block *pem.Block) bool {
	return block.Type == pemTypePKCS8Encrypted || legacyPEMEncrypted(block)
}

// parsePEMPrivate decodes a PKCS#1, SEC1, OpenSSL DSA or PKCS#8 block.
func parsePEMPrivate(block *pem.Block, passphrase []byte) (crypto.Signer, error) {
	der := block.Bytes
	blockType := block.Type
	encrypted := false

	switch {
	case blockType == pemTypePKCS8Encrypted:
		plain, err := decryptPKCS8(der, passphrase)
		if err != nil {
			return nil, err
		}
		defer zero(plain)
		der, blockType, encrypted = plain, pemTypePKCS8, true
	case legacyPEMEncrypted(block):
		if blockType == pemTypePKCS8 {
			return nil, fmt.Errorf("%w: PKCS#8 block with legacy PEM encryption", ErrMalformed)
		}
		plain, err := decryptLegacyPEM(block, passphrase)
		if err != nil {
			return nil, err
		}
		defer zero(plain)
		der, encrypted = plain, true
	}

	signer, err := parsePrivateDER(blockType, der)
	if err != nil && encrypted && !isUnsupported(err) {
		// Without an integrity check a wrong passphrase can survive the
		// padding test and only show up as undecodable ASN.1.
		return nil, ErrWrongPassphrase
	}
	return signer, err
}

// parsePrivateDER decodes the DER body of an unencrypted private key block.
func parsePrivateDER(blockType string, der []byte) (crypto.Signer, error) {
	switch blockType {
	case pemTypeRSA:
		key, err := x509.ParsePKCS1PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		return checkParsedRSA(key)
	case pemTypeEC:
		key, err := x509.ParseECPrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if _, err := ecdsaCurveName(key.Curve); err != nil {
			return nil, err
		}
		return key, nil
	case pemTypeDSA:
		return parseOpenSSLDSA(der)
	case pemTypePKCS8:
		return parsePKCS8(der)
	}
	return nil, fmt.Errorf("%w: PEM block %q is not a private key", ErrUnknownFormat, blockType)
}

// checkParsedRSA applies this package's limits to an RSA key decoded by
// crypto/x509.
func checkParsedRSA(key *rsa.PrivateKey) (crypto.Signer, error) {
	if key.N.BitLen() > MaxRSABits {
		return nil, fmt.Errorf("%w: %d-bit RSA modulus (limit %d)", ErrUnsupported, key.N.BitLen(), MaxRSABits)
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	key.Precompute()
	return key, nil
}

// opensslDSAKey is OpenSSL's traditional DSA private key structure.
type opensslDSAKey struct {
	Version       int
	P, Q, G, Y, X *big.Int
}

func parseOpenSSLDSA(der []byte) (crypto.Signer, error) {
	var k opensslDSAKey
	rest, err := asn1.Unmarshal(der, &k)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if len(rest) != 0 || k.Version != 0 {
		return nil, fmt.Errorf("%w: unexpected DSA private key structure", ErrMalformed)
	}
	return newDSAPrivateKey(k.P, k.Q, k.G, k.Y, k.X)
}

// pkcs8Info is PrivateKeyInfo (RFC 5208). Trailing attributes and the
// RFC 5958 public key are ignored.
type pkcs8Info struct {
	Version    int
	Algorithm  pkix.AlgorithmIdentifier
	PrivateKey []byte
}

type dsaDomainParams struct {
	P, Q, G *big.Int
}

func parsePKCS8(der []byte) (crypto.Signer, error) {
	var info pkcs8Info
	if _, err := asn1.Unmarshal(der, &info); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if info.Algorithm.Algorithm.Equal(oidKeyDSA) {
		// crypto/x509 does not decode DSA keys.
		var params dsaDomainParams
		if rest, err := asn1.Unmarshal(info.Algorithm.Parameters.FullBytes, &params); err != nil || len(rest) != 0 {
			return nil, fmt.Errorf("%w: bad DSA parameters in PKCS#8 key", ErrMalformed)
		}
		x := new(big.Int)
		if rest, err := asn1.Unmarshal(info.PrivateKey, &x); err != nil || len(rest) != 0 {
			return nil, fmt.Errorf("%w: bad DSA private value in PKCS#8 key", ErrMalformed)
		}
		return newDSAPrivateKey(params.P, params.Q, params.G, nil, x)
	}

	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return checkParsedRSA(k)
	case *ecdsa.PrivateKey:
		if _, err := ecdsaCurveName(k.Curve); err != nil {
			return nil, err
		}
		return k, nil
	case ed25519.PrivateKey:
		return k, nil
	}
	return nil, fmt.Errorf("%w: PKCS#8 key of type %T cannot be used with SSH", ErrUnsupported, key)
}

// marshalPKCS8 encodes a key as an unencrypted PrivateKeyInfo.
func marshalPKCS8(s crypto.Signer) ([]byte, error) {
	if d, ok := s.(*DSAPrivateKey); ok {
		params, err := asn1.Marshal(dsaDomainParams{d.P, d.Q, d.G})
		if err != nil {
			return nil, err
		}
		x, err := asn1.Marshal(d.X)
		if err != nil {
			return nil, err
		}
		return asn1.Marshal(pkcs8Info{
			Algorithm: pkix.AlgorithmIdentifier{
				Algorithm:  oidKeyDSA,
				Parameters: asn1.RawValue{FullBytes: params},
			},
			PrivateKey: x,
		})
	}
	if r, ok := s.(*rsa.PrivateKey); ok {
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
	}
	der, err := x509.MarshalPKCS8PrivateKey(s)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	return der, nil
}

// Encrypted PKCS#8 (RFC 5958 EncryptedPrivateKeyInfo with RFC 8018 schemes).

type encryptedPKCS8 struct {
	Algorithm pkix.AlgorithmIdentifier
	Data      []byte
}

type pbes2Params struct {
	KDF    pkix.AlgorithmIdentifier
	Cipher pkix.AlgorithmIdentifier
}

type pbkdf2Params struct {
	Salt       []byte
	Iterations int
	KeyLength  int                      `asn1:"optional"`
	PRF        pkix.AlgorithmIdentifier `asn1:"optional"`
}

type scryptParams struct {
	Salt      []byte
	N         int
	R         int
	P         int
	KeyLength int `asn1:"optional"`
}

type pbes1Params struct {
	Salt       []byte
	Iterations int
}

// pkcs8Scheme is the decoded protection of an EncryptedPrivateKeyInfo.
type pkcs8Scheme struct {
	cipher cbcCipher
	iv     []byte // PBES2 only
	kdf    string // human-readable description
	// derive returns keyLen bytes of key material (cipher key, and for
	// PBES1 the IV as well).
	derive func(passphrase []byte, keyLen int) ([]byte, error)
	pbes1  bool
}

func unmarshalStrict(der []byte, v any) error {
	rest, err := asn1.Unmarshal(der, v)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("trailing data")
	}
	return nil
}

// parsePKCS8Scheme decodes the encryption algorithm of an encrypted PKCS#8
// structure without doing any expensive work.
func parsePKCS8Scheme(algo pkix.AlgorithmIdentifier) (*pkcs8Scheme, error) {
	bad := func(what string) error {
		return fmt.Errorf("%w: bad %s in encrypted PKCS#8 key", ErrMalformed, what)
	}
	switch {
	case algo.Algorithm.Equal(oidPBEMD5DES), algo.Algorithm.Equal(oidPBESHA1DES):
		var p pbes1Params
		if err := unmarshalStrict(algo.Parameters.FullBytes, &p); err != nil {
			return nil, bad("PBES1 parameters")
		}
		if p.Iterations < 1 || p.Iterations > maxPBKDF2Iterations {
			return nil, fmt.Errorf("%w: PBKDF1 iteration count %d", ErrUnsupported, p.Iterations)
		}
		newHash, name := md5.New, "PBKDF1-MD5"
		if algo.Algorithm.Equal(oidPBESHA1DES) {
			newHash, name = sha1.New, "PBKDF1-SHA1"
		}
		return &pkcs8Scheme{
			cipher: cbcDES,
			kdf:    name,
			pbes1:  true,
			derive: func(passphrase []byte, keyLen int) ([]byte, error) {
				h := newHash()
				h.Write(passphrase)
				h.Write(p.Salt)
				sum := h.Sum(nil)
				for i := 1; i < p.Iterations; i++ {
					h.Reset()
					h.Write(sum)
					sum = h.Sum(sum[:0])
				}
				return sum[:keyLen], nil
			},
		}, nil

	case algo.Algorithm.Equal(oidPBES2):
		var p pbes2Params
		if err := unmarshalStrict(algo.Parameters.FullBytes, &p); err != nil {
			return nil, bad("PBES2 parameters")
		}
		s := &pkcs8Scheme{}
		switch {
		case p.Cipher.Algorithm.Equal(oidAES128CBC):
			s.cipher = cbcAES128
		case p.Cipher.Algorithm.Equal(oidAES192CBC):
			s.cipher = cbcAES192
		case p.Cipher.Algorithm.Equal(oidAES256CBC):
			s.cipher = cbcAES256
		case p.Cipher.Algorithm.Equal(oidDESEDE3CBC):
			s.cipher = cbc3DES
		case p.Cipher.Algorithm.Equal(oidDESCBC):
			s.cipher = cbcDES
		default:
			return nil, fmt.Errorf("%w: PKCS#8 encryption scheme %v", ErrUnsupported, p.Cipher.Algorithm)
		}
		if err := unmarshalStrict(p.Cipher.Parameters.FullBytes, &s.iv); err != nil {
			return nil, bad("cipher parameters")
		}

		switch {
		case p.KDF.Algorithm.Equal(oidPBKDF2):
			var k pbkdf2Params
			if err := unmarshalStrict(p.KDF.Parameters.FullBytes, &k); err != nil {
				return nil, bad("PBKDF2 parameters")
			}
			if k.Iterations < 1 || k.Iterations > maxPBKDF2Iterations {
				return nil, fmt.Errorf("%w: PBKDF2 iteration count %d", ErrUnsupported, k.Iterations)
			}
			if k.KeyLength != 0 && k.KeyLength != s.cipher.keyLen {
				return nil, bad("PBKDF2 key length")
			}
			var newHash func() hash.Hash
			switch prf := k.PRF.Algorithm; {
			case len(prf) == 0, prf.Equal(oidHMACSHA1):
				newHash, s.kdf = sha1.New, "PBKDF2-HMAC-SHA1"
			case prf.Equal(oidHMACSHA224):
				newHash, s.kdf = sha256.New224, "PBKDF2-HMAC-SHA224"
			case prf.Equal(oidHMACSHA256):
				newHash, s.kdf = sha256.New, "PBKDF2-HMAC-SHA256"
			case prf.Equal(oidHMACSHA384):
				newHash, s.kdf = sha512.New384, "PBKDF2-HMAC-SHA384"
			case prf.Equal(oidHMACSHA512):
				newHash, s.kdf = sha512.New, "PBKDF2-HMAC-SHA512"
			default:
				return nil, fmt.Errorf("%w: PBKDF2 pseudo-random function %v", ErrUnsupported, prf)
			}
			s.derive = func(passphrase []byte, keyLen int) ([]byte, error) {
				return pbkdf2.Key(passphrase, k.Salt, k.Iterations, keyLen, newHash), nil
			}
		case p.KDF.Algorithm.Equal(oidScrypt):
			var k scryptParams
			if err := unmarshalStrict(p.KDF.Parameters.FullBytes, &k); err != nil {
				return nil, bad("scrypt parameters")
			}
			if k.KeyLength != 0 && k.KeyLength != s.cipher.keyLen {
				return nil, bad("scrypt key length")
			}
			if k.N < 2 || k.R < 1 || k.P < 1 || k.P > 1024 ||
				k.N > maxScryptMemory/128 || k.R > maxScryptMemory/128 ||
				int64(k.N)*int64(k.R)*128 > maxScryptMemory {
				return nil, fmt.Errorf("%w: scrypt cost N=%d r=%d p=%d", ErrUnsupported, k.N, k.R, k.P)
			}
			s.kdf = "scrypt"
			s.derive = func(passphrase []byte, keyLen int) ([]byte, error) {
				key, err := scrypt.Key(passphrase, k.Salt, k.N, k.R, k.P, keyLen)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
				}
				return key, nil
			}
		default:
			return nil, fmt.Errorf("%w: PKCS#8 key derivation %v", ErrUnsupported, p.KDF.Algorithm)
		}
		return s, nil
	}
	return nil, fmt.Errorf("%w: PKCS#8 encryption algorithm %v", ErrUnsupported, algo.Algorithm)
}

// parseEncryptedPKCS8 splits an EncryptedPrivateKeyInfo into its protection
// scheme and ciphertext.
func parseEncryptedPKCS8(der []byte) (*pkcs8Scheme, []byte, error) {
	var enc encryptedPKCS8
	if err := unmarshalStrict(der, &enc); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	scheme, err := parsePKCS8Scheme(enc.Algorithm)
	if err != nil {
		return nil, nil, err
	}
	return scheme, enc.Data, nil
}

// decryptPKCS8 returns the PrivateKeyInfo inside an EncryptedPrivateKeyInfo.
func decryptPKCS8(der, passphrase []byte) ([]byte, error) {
	scheme, data, err := parseEncryptedPKCS8(der)
	if err != nil {
		return nil, err
	}
	if len(passphrase) == 0 {
		return nil, ErrPassphraseRequired
	}
	if scheme.pbes1 {
		km, err := scheme.derive(passphrase, 16)
		if err != nil {
			return nil, err
		}
		defer zero(km)
		return cbcDecrypt(scheme.cipher, km[:8], km[8:16], data)
	}
	key, err := scheme.derive(passphrase, scheme.cipher.keyLen)
	if err != nil {
		return nil, err
	}
	defer zero(key)
	return cbcDecrypt(scheme.cipher, key, scheme.iv, data)
}

// encryptPKCS8 wraps a PrivateKeyInfo using PBES2 with PBKDF2-HMAC-SHA256 and
// AES-256-CBC.
func encryptPKCS8(der, passphrase []byte) ([]byte, error) {
	salt := make([]byte, pkcs8SaltLen)
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	key := pbkdf2.Key(passphrase, salt, pkcs8Iterations, cbcAES256.keyLen, sha256.New)
	defer zero(key)
	data, err := cbcEncrypt(cbcAES256, key, iv, der)
	if err != nil {
		return nil, err
	}

	kdf, err := asn1.Marshal(pbkdf2Params{
		Salt:       salt,
		Iterations: pkcs8Iterations,
		PRF:        pkix.AlgorithmIdentifier{Algorithm: oidHMACSHA256, Parameters: asn1.NullRawValue},
	})
	if err != nil {
		return nil, err
	}
	ivDER, err := asn1.Marshal(iv)
	if err != nil {
		return nil, err
	}
	params, err := asn1.Marshal(pbes2Params{
		KDF:    pkix.AlgorithmIdentifier{Algorithm: oidPBKDF2, Parameters: asn1.RawValue{FullBytes: kdf}},
		Cipher: pkix.AlgorithmIdentifier{Algorithm: oidAES256CBC, Parameters: asn1.RawValue{FullBytes: ivDER}},
	})
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(encryptedPKCS8{
		Algorithm: pkix.AlgorithmIdentifier{Algorithm: oidPBES2, Parameters: asn1.RawValue{FullBytes: params}},
		Data:      data,
	})
}

// MarshalPEM serialises the key as a classic PEM block in the given style.
// An empty passphrase writes the key unencrypted. See [PEMTraditional] and
// [PEMPKCS8] for the exact encodings.
func (k *PrivateKey) MarshalPEM(style PEMStyle, passphrase []byte) ([]byte, error) {
	s, err := k.signer()
	if err != nil {
		return nil, err
	}
	if style != PEMTraditional && style != PEMPKCS8 {
		return nil, fmt.Errorf("keys: unknown PEM style %d", style)
	}

	var (
		der       []byte
		blockType string
	)
	if style == PEMTraditional {
		switch key := s.(type) {
		case *rsa.PrivateKey:
			if err := key.Validate(); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
			}
			der, blockType = x509.MarshalPKCS1PrivateKey(key), pemTypeRSA
		case *ecdsa.PrivateKey:
			der, err = x509.MarshalECPrivateKey(key)
			if err != nil {
				return nil, fmt.Errorf("keys: %w", err)
			}
			blockType = pemTypeEC
		case *DSAPrivateKey:
			der, err = asn1.Marshal(opensslDSAKey{0, key.P, key.Q, key.G, key.Y, key.X})
			if err != nil {
				return nil, fmt.Errorf("keys: %w", err)
			}
			blockType = pemTypeDSA
		}
	}
	if der != nil {
		defer zero(der)
		block := &pem.Block{Type: blockType, Bytes: der}
		if len(passphrase) > 0 {
			if block, err = encryptLegacyPEM(blockType, der, passphrase); err != nil {
				return nil, err
			}
		}
		return encodePEM(block), nil
	}

	// PKCS#8, also the fallback for Ed25519 in the traditional style.
	der, err = marshalPKCS8(s)
	if err != nil {
		return nil, err
	}
	defer zero(der)
	if len(passphrase) == 0 {
		return encodePEM(&pem.Block{Type: pemTypePKCS8, Bytes: der}), nil
	}
	enc, err := encryptPKCS8(der, passphrase)
	if err != nil {
		return nil, err
	}
	return encodePEM(&pem.Block{Type: pemTypePKCS8Encrypted, Bytes: enc}), nil
}

// encodePEM is pem.EncodeToMemory with the RFC 1421 header order OpenSSL
// expects: Proc-Type first (which encoding/pem guarantees), then DEK-Info.
func encodePEM(block *pem.Block) []byte {
	var buf bytes.Buffer
	if err := pem.Encode(&buf, block); err != nil {
		// Only possible for headers containing a colon, which we never write.
		return nil
	}
	return buf.Bytes()
}
