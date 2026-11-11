package keys

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // mandated by PPK version 2
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"math/big"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/ssh"
)

// This file implements PuTTY's private key format as documented in the
// "PPK file format" appendix of the PuTTY manual. A file looks like this
// (the Key-Derivation and Argon2-* headers appear only in encrypted
// version 3 files):
//
//	PuTTY-User-Key-File-3: ssh-ed25519
//	Encryption: aes256-cbc
//	Comment: user@host
//	Public-Lines: 2
//	<base64 of the SSH public key blob, 64 columns>
//	Key-Derivation: Argon2id
//	Argon2-Memory: 8192
//	Argon2-Passes: 21
//	Argon2-Parallelism: 1
//	Argon2-Salt: <hex>
//	Private-Lines: 1
//	<base64 of the private blob, encrypted if requested>
//	Private-MAC: <hex>
//
// The private blob holds, by key type:
//
//	ssh-rsa               mpint d, mpint p, mpint q, mpint iqmp
//	ssh-dss               mpint x
//	ecdsa-sha2-nistp*     mpint d
//	ssh-ed25519           string seed (32 bytes)
//
// The MAC covers the algorithm name, encryption name, comment, public blob
// and the plaintext private blob including its padding, each as an SSH
// string.
//
// Version 2 derives the AES-256-CBC key from two SHA-1 hashes of a sequence
// number and the passphrase, uses an all-zero IV and authenticates with
// HMAC-SHA-1 keyed by SHA-1("putty-private-key-file-mac-key" || passphrase).
// Version 3 runs Argon2 to obtain 80 bytes - cipher key (32), IV (16) and MAC
// key (32) - and authenticates with HMAC-SHA-256; unencrypted version 3
// files use an empty MAC key.

const (
	ppkHeaderPrefix = "PuTTY-User-Key-File-"
	ppkMACKeyPrefix = "putty-private-key-file-mac-key"
	ppkCipherNone   = "none"
	ppkCipherAES    = "aes256-cbc"
	ppkLineWidth    = 64

	// PPKArgon2id and PPKArgon2i are the key derivation functions
	// available for encrypted version 3 files.
	PPKArgon2id = "Argon2id"
	PPKArgon2i  = "Argon2i"
	ppkArgon2d  = "Argon2d"

	// Defaults for the files we write. Memory and parallelism are
	// PuTTYgen's own; PuTTYgen calibrates the pass count to about 100 ms,
	// which on current hardware comes out at around 21.
	ppkDefaultMemory      = 8192 // KiB
	ppkDefaultPasses      = 21
	ppkDefaultParallelism = 1
	ppkSaltLen            = 16

	// Limits on the Argon2 parameters accepted from a file. Besides the
	// individual bounds, memory times passes is capped: the run time is
	// proportional to that product, and 2^25 KiB-passes (1 GiB for 32
	// passes, or PuTTYgen's default 8 MiB for all 1024) is about 200 times
	// the default cost.
	maxArgon2Memory      = 1 << 20 // KiB, i.e. 1 GiB
	maxArgon2Passes      = 1024
	maxArgon2Parallelism = 255
	maxArgon2Work        = 1 << 25 // KiB x passes
)

// PPKOptions controls how [PrivateKey.MarshalPPKOptions] writes a PuTTY
// private key file. The zero value selects PuTTYgen's defaults.
type PPKOptions struct {
	// Version is the file format version: 3 (the default, readable by
	// PuTTY 0.75 and later) or 2 (readable by every PuTTY release since
	// 0.52 and by most third-party tools).
	Version int
	// KDF is the passphrase hash of encrypted version 3 files: PPKArgon2id
	// (the default) or PPKArgon2i.
	KDF string
	// Memory is the Argon2 memory cost in KiB (default 8192).
	Memory uint32
	// Passes is the Argon2 time cost (default 21, roughly 100 ms).
	Passes uint32
	// Parallelism is the Argon2 lane count (default 1).
	Parallelism uint32
}

// ppkFile is a decoded .ppk file. private is still encrypted if the file is.
type ppkFile struct {
	version    int
	algorithm  string
	encryption string
	comment    string
	public     []byte
	private    []byte
	mac        []byte

	kdf         string
	memory      uint32
	passes      uint32
	parallelism uint32
	salt        []byte
}

func (f *ppkFile) encrypted() bool { return f.encryption != ppkCipherNone }

// ppkTrim removes a byte order mark and leading blank space.
func ppkTrim(data []byte) []byte {
	return bytes.TrimLeft(trimBOM(data), " \t\r\n")
}

// isPPK reports whether data looks like a PuTTY private key file.
func isPPK(data []byte) bool {
	return bytes.HasPrefix(ppkTrim(data), []byte(ppkHeaderPrefix))
}

// ppkScanner walks the lines of a .ppk file.
type ppkScanner struct {
	lines []string
	pos   int
}

func (s *ppkScanner) next() (string, bool) {
	if s.pos >= len(s.lines) {
		return "", false
	}
	l := s.lines[s.pos]
	s.pos++
	return l, true
}

// header reads a "Name: value" line with the given name.
func (s *ppkScanner) header(name string) (string, error) {
	line, ok := s.next()
	if !ok {
		return "", fmt.Errorf("%w: PPK file ends before the %s header", ErrMalformed, name)
	}
	if line == name+":" {
		return "", nil // value (and the space before it) stripped by an editor
	}
	value, ok := strings.CutPrefix(line, name+": ")
	if !ok {
		return "", fmt.Errorf("%w: expected PPK header %q", ErrMalformed, name)
	}
	return value, nil
}

// number reads a header holding a non-negative decimal integer.
func (s *ppkScanner) number(name string) (uint32, error) {
	v, err := s.header(name)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: bad value for PPK header %s", ErrMalformed, name)
	}
	return uint32(n), nil
}

// body reads a "<Name>-Lines: n" header and the n base64 lines after it.
func (s *ppkScanner) body(name string) ([]byte, error) {
	n, err := s.number(name)
	if err != nil {
		return nil, err
	}
	if int64(n) > int64(len(s.lines)-s.pos) {
		return nil, fmt.Errorf("%w: PPK file is shorter than %s announces", ErrMalformed, name)
	}
	var b64 strings.Builder
	for range n {
		line, _ := s.next()
		b64.WriteString(strings.TrimSpace(line))
	}
	blob, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		return nil, fmt.Errorf("%w: bad base64 under PPK header %s", ErrMalformed, name)
	}
	return blob, nil
}

// parsePPKFile decodes the structure of a .ppk file without decrypting it.
func parsePPKFile(data []byte) (*ppkFile, error) {
	text := string(ppkTrim(data))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	s := &ppkScanner{lines: strings.Split(text, "\n")}

	first, _ := s.next()
	rest, ok := strings.CutPrefix(first, ppkHeaderPrefix)
	if !ok {
		return nil, fmt.Errorf("%w: not a PuTTY private key file", ErrUnknownFormat)
	}
	versionText, algorithm, ok := strings.Cut(rest, ": ")
	if !ok {
		return nil, fmt.Errorf("%w: bad first line of PPK file", ErrMalformed)
	}
	f := &ppkFile{algorithm: strings.TrimSpace(algorithm)}
	switch versionText {
	case "2":
		f.version = 2
	case "3":
		f.version = 3
	case "1":
		return nil, fmt.Errorf("%w: PPK version 1 files (PuTTY 0.51 and older); re-save the key with a current PuTTYgen", ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: PPK format version %q", ErrUnsupported, versionText)
	}

	var err error
	if f.encryption, err = s.header("Encryption"); err != nil {
		return nil, err
	}
	f.encryption = strings.TrimSpace(f.encryption)
	if f.encryption != ppkCipherNone && f.encryption != ppkCipherAES {
		return nil, fmt.Errorf("%w: PPK encryption %q", ErrUnsupported, f.encryption)
	}
	if f.comment, err = s.header("Comment"); err != nil {
		return nil, err
	}
	if f.public, err = s.body("Public-Lines"); err != nil {
		return nil, err
	}

	if f.version == 3 && f.encrypted() {
		if f.kdf, err = s.header("Key-Derivation"); err != nil {
			return nil, err
		}
		f.kdf = strings.TrimSpace(f.kdf)
		if f.memory, err = s.number("Argon2-Memory"); err != nil {
			return nil, err
		}
		if f.passes, err = s.number("Argon2-Passes"); err != nil {
			return nil, err
		}
		if f.parallelism, err = s.number("Argon2-Parallelism"); err != nil {
			return nil, err
		}
		saltHex, err := s.header("Argon2-Salt")
		if err != nil {
			return nil, err
		}
		if f.salt, err = hex.DecodeString(strings.TrimSpace(saltHex)); err != nil {
			return nil, fmt.Errorf("%w: bad Argon2 salt in PPK file", ErrMalformed)
		}
	}

	if f.private, err = s.body("Private-Lines"); err != nil {
		return nil, err
	}
	macHex, err := s.header("Private-MAC")
	if err != nil {
		return nil, err
	}
	if f.mac, err = hex.DecodeString(strings.TrimSpace(macHex)); err != nil {
		return nil, fmt.Errorf("%w: bad Private-MAC in PPK file", ErrMalformed)
	}
	if f.encrypted() && (len(f.private) == 0 || len(f.private)%aes.BlockSize != 0) {
		return nil, fmt.Errorf("%w: encrypted PPK private data is not a whole number of cipher blocks", ErrMalformed)
	}
	return f, nil
}

// ppkSecrets is the key material protecting a .ppk file.
type ppkSecrets struct {
	cipherKey []byte
	iv        []byte
	macKey    []byte
	newMAC    func() hash.Hash
}

func (s *ppkSecrets) wipe() {
	zero(s.cipherKey)
	zero(s.iv)
	zero(s.macKey)
}

// ppkDeriveV2 implements the version 2 scheme.
func ppkDeriveV2(encrypted bool, passphrase []byte) *ppkSecrets {
	s := &ppkSecrets{newMAC: sha1.New}
	h := sha1.New() //nolint:gosec
	h.Write([]byte(ppkMACKeyPrefix))
	if encrypted {
		h.Write(passphrase)
	}
	s.macKey = h.Sum(nil)
	if encrypted {
		// Key = SHA-1(00000000 || passphrase) || SHA-1(00000001 || passphrase),
		// truncated to 32 bytes; the IV is all zeros.
		var km []byte
		for seq := range byte(2) {
			h.Reset()
			h.Write([]byte{0, 0, 0, seq})
			h.Write(passphrase)
			km = h.Sum(km)
		}
		s.cipherKey = km[:32]
		zero(km[32:])
		s.iv = make([]byte, aes.BlockSize)
	}
	return s
}

// ppkDeriveV3 implements the version 3 scheme.
func ppkDeriveV3(encrypted bool, passphrase []byte, kdf string, memory, passes, parallelism uint32, salt []byte) (*ppkSecrets, error) {
	s := &ppkSecrets{newMAC: sha256.New}
	if !encrypted {
		s.macKey = []byte{}
		return s, nil
	}
	switch {
	case passes < 1 || passes > maxArgon2Passes:
		return nil, fmt.Errorf("%w: Argon2 pass count %d is outside 1..%d", ErrUnsupported, passes, maxArgon2Passes)
	case parallelism < 1 || parallelism > maxArgon2Parallelism:
		return nil, fmt.Errorf("%w: Argon2 parallelism %d is outside 1..%d", ErrUnsupported, parallelism, maxArgon2Parallelism)
	case memory < 8*parallelism || memory > maxArgon2Memory:
		return nil, fmt.Errorf("%w: Argon2 memory cost %d KiB is outside %d..%d", ErrUnsupported, memory, 8*parallelism, maxArgon2Memory)
	case uint64(memory)*uint64(passes) > maxArgon2Work:
		return nil, fmt.Errorf("%w: Argon2 cost of %d KiB x %d passes is too high", ErrUnsupported, memory, passes)
	}
	const outLen = 32 + aes.BlockSize + 32
	var km []byte
	switch strings.ToLower(kdf) {
	case "argon2id":
		km = argon2.IDKey(passphrase, salt, passes, memory, uint8(parallelism), outLen)
	case "argon2i":
		km = argon2.Key(passphrase, salt, passes, memory, uint8(parallelism), outLen)
	case "argon2d":
		return nil, fmt.Errorf("%w: PPK files protected with Argon2d; re-save the key with Argon2id (PuTTYgen: Key > Parameters for saving key files)", ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: PPK key derivation %q", ErrUnsupported, kdf)
	}
	s.cipherKey, s.iv, s.macKey = km[:32], km[32:48], km[48:]
	return s, nil
}

// ppkMAC computes the Private-MAC value over the plaintext private blob.
func ppkMAC(s *ppkSecrets, algorithm, encryption, comment string, public, private []byte) []byte {
	var w wireWriter
	w.text(algorithm)
	w.text(encryption)
	w.text(comment)
	w.str(public)
	w.str(private)
	defer zero(w.buf)
	m := hmac.New(s.newMAC, s.macKey)
	m.Write(w.buf)
	return m.Sum(nil)
}

// open verifies the MAC and returns the plaintext private blob (including
// any padding), which the caller should zero when done.
func (f *ppkFile) open(passphrase []byte) ([]byte, error) {
	if f.encrypted() && len(passphrase) == 0 {
		return nil, ErrPassphraseRequired
	}
	var (
		secrets *ppkSecrets
		err     error
	)
	if f.version == 2 {
		secrets = ppkDeriveV2(f.encrypted(), passphrase)
	} else {
		secrets, err = ppkDeriveV3(f.encrypted(), passphrase, f.kdf, f.memory, f.passes, f.parallelism, f.salt)
		if err != nil {
			return nil, err
		}
	}
	defer secrets.wipe()

	plain := bytes.Clone(f.private)
	if f.encrypted() {
		c, err := aes.NewCipher(secrets.cipherKey)
		if err != nil {
			return nil, err
		}
		cipher.NewCBCDecrypter(c, secrets.iv).CryptBlocks(plain, plain)
	}
	mac := ppkMAC(secrets, f.algorithm, f.encryption, f.comment, f.public, plain)
	if !hmac.Equal(mac, f.mac) {
		zero(plain)
		if f.encrypted() {
			return nil, fmt.Errorf("%w (or the file is corrupt: %w)", ErrWrongPassphrase, ErrMACMismatch)
		}
		return nil, fmt.Errorf("%w: the key file is corrupt or has been modified", ErrMACMismatch)
	}
	return plain, nil
}

// ppkBaseKey returns the plain key algorithm and public blob for a PPK
// public section, unwrapping an embedded OpenSSH certificate (PuTTY 0.78+).
func ppkBaseKey(algorithm string, public []byte) (string, []byte, error) {
	if !strings.HasSuffix(algorithm, certAlgoSuffix) {
		return algorithm, public, nil
	}
	pub, err := ssh.ParsePublicKey(public)
	if err != nil {
		return "", nil, fmt.Errorf("%w: certificate in PPK file: %v", ErrMalformed, err)
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok || cert.Key == nil {
		return "", nil, fmt.Errorf("%w: certificate in PPK file", ErrMalformed)
	}
	return cert.Key.Type(), cert.Key.Marshal(), nil
}

// parsePPKPrivate combines the public blob and the plaintext private blob of
// a .ppk file into a key.
func parsePPKPrivate(algorithm string, public, private []byte) (crypto.Signer, error) {
	algorithm, public, err := ppkBaseKey(algorithm, public)
	if err != nil {
		return nil, err
	}
	pub := newWireReader(public)
	if name := string(pub.str()); pub.err != nil || name != algorithm {
		return nil, fmt.Errorf("%w: PPK public key does not match its declared type %q", ErrMalformed, algorithm)
	}
	priv := newWireReader(private)

	var signer crypto.Signer
	switch algorithm {
	case ssh.KeyAlgoRSA:
		e, n := pub.mpint(), pub.mpint()
		d, p, q := priv.mpint(), priv.mpint(), priv.mpint()
		_ = priv.mpint() // iqmp is recomputed
		if pub.err == nil && priv.err == nil {
			signer, err = newRSAPrivateKey(n, e, d, p, q)
		}
	case keyAlgoDSA:
		p, q, g, y := pub.mpint(), pub.mpint(), pub.mpint(), pub.mpint()
		x := priv.mpint()
		if pub.err == nil && priv.err == nil {
			signer, err = newDSAPrivateKey(p, q, g, y, x)
		}
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		curve, point := string(pub.str()), pub.str()
		d := priv.mpint()
		if pub.err == nil && priv.err == nil {
			if "ecdsa-sha2-"+curve != algorithm {
				return nil, fmt.Errorf("%w: ECDSA curve %q does not match key type %q", ErrMalformed, curve, algorithm)
			}
			signer, err = newECDSAPrivateKey(curve, point, d)
		}
	case ssh.KeyAlgoED25519:
		pk, seed := pub.str(), priv.str()
		if pub.err == nil && priv.err == nil {
			// PuTTY treats the seed as a little-endian integer and
			// releases before 0.75 dropped its trailing zero bytes.
			if len(seed) > ed25519.SeedSize || len(pk) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("%w: bad Ed25519 key length in PPK file", ErrMalformed)
			}
			full := make([]byte, ed25519.PrivateKeySize)
			copy(full, seed)
			copy(full[ed25519.SeedSize:], pk)
			signer, err = newEd25519PrivateKey(pk, full)
			zero(full)
		}
	default:
		return nil, fmt.Errorf("%w: PPK key type %q", ErrUnsupported, algorithm)
	}
	if pub.err != nil {
		return nil, fmt.Errorf("%w (PPK public key)", pub.err)
	}
	if priv.err != nil {
		return nil, fmt.Errorf("%w (PPK private key)", priv.err)
	}
	if err != nil {
		return nil, err
	}
	if !pub.done() {
		return nil, fmt.Errorf("%w: trailing data after PPK public key", ErrMalformed)
	}
	return signer, nil
}

// parsePPK decodes a complete .ppk file.
func parsePPK(data, passphrase []byte) (crypto.Signer, string, error) {
	f, err := parsePPKFile(data)
	if err != nil {
		return nil, "", err
	}
	plain, err := f.open(passphrase)
	if err != nil {
		return nil, "", err
	}
	defer zero(plain)
	signer, err := parsePPKPrivate(f.algorithm, f.public, plain)
	if err != nil {
		return nil, "", err
	}
	return signer, f.comment, nil
}

// ppkPrivateBlob encodes the private half of a key the way PuTTY stores it.
func ppkPrivateBlob(s crypto.Signer) ([]byte, error) {
	var w wireWriter
	switch key := s.(type) {
	case *rsa.PrivateKey:
		if err := checkRSAExportable(key); err != nil {
			return nil, err
		}
		// PuTTY's canonical form has p > q and iqmp = q^-1 mod p.
		p, q, iqmp := key.Primes[0], key.Primes[1], key.Precomputed.Qinv
		if p.Cmp(q) < 0 {
			p, q = q, p
			iqmp = new(big.Int).ModInverse(q, p)
			if iqmp == nil {
				return nil, fmt.Errorf("%w: invalid RSA key", ErrMalformed)
			}
		}
		w.mpint(key.D)
		w.mpint(p)
		w.mpint(q)
		w.mpint(iqmp)
	case *DSAPrivateKey:
		w.mpint(key.X)
	case *ecdsa.PrivateKey:
		raw, err := key.Bytes()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		w.mpint(new(big.Int).SetBytes(raw))
		zero(raw)
	case ed25519.PrivateKey:
		w.str(key.Seed())
	default:
		return nil, fmt.Errorf("%w: private key type %T", ErrUnsupported, s)
	}
	return w.bytes(), nil
}

// MarshalPPK serialises the key as a PuTTY private key file of the given
// format version: 3 (Argon2id key derivation, HMAC-SHA-256) or 2 (SHA-1 key
// derivation, HMAC-SHA-1). Both encrypt with AES-256-CBC when a passphrase is
// given; an empty passphrase writes the key unencrypted. Version 0 selects
// version 3.
func (k *PrivateKey) MarshalPPK(version int, passphrase []byte) ([]byte, error) {
	return k.MarshalPPKOptions(PPKOptions{Version: version}, passphrase)
}

// MarshalPPKOptions is MarshalPPK with control over the Argon2 parameters of
// encrypted version 3 files.
func (k *PrivateKey) MarshalPPKOptions(opts PPKOptions, passphrase []byte) ([]byte, error) {
	s, err := k.signer()
	if err != nil {
		return nil, err
	}
	if opts.Version == 0 {
		opts.Version = 3
	}
	if opts.Version != 2 && opts.Version != 3 {
		return nil, fmt.Errorf("%w: PPK format version %d (use 2 or 3)", ErrUnsupported, opts.Version)
	}
	pub, err := publicKeyOf(s)
	if err != nil {
		return nil, err
	}
	algorithm := pub.Type()
	public := pub.Marshal()
	comment := sanitizeComment(k.Comment)
	encrypted := len(passphrase) > 0
	encryption := ppkCipherNone
	if encrypted {
		encryption = ppkCipherAES
	}

	private, err := ppkPrivateBlob(s)
	if err != nil {
		return nil, err
	}
	defer func() { zero(private) }()
	if encrypted {
		// Pad to the cipher block size. Like PuTTY, use the leading bytes
		// of the SHA-1 of the unpadded blob, which keeps the last block
		// from being predictable without consuming randomness.
		if n := len(private) % aes.BlockSize; n != 0 {
			digest := sha1.Sum(private) //nolint:gosec
			padded := make([]byte, len(private), len(private)+aes.BlockSize-n)
			copy(padded, private)
			zero(private)
			private = append(padded, digest[:aes.BlockSize-n]...)
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%s%d: %s\n", ppkHeaderPrefix, opts.Version, algorithm)
	fmt.Fprintf(&out, "Encryption: %s\n", encryption)
	fmt.Fprintf(&out, "Comment: %s\n", comment)
	writePPKBody(&out, "Public-Lines", public)

	var secrets *ppkSecrets
	if opts.Version == 2 {
		secrets = ppkDeriveV2(encrypted, passphrase)
	} else {
		kdf := opts.KDF
		switch strings.ToLower(kdf) {
		case "", "argon2id":
			kdf = PPKArgon2id
		case "argon2i":
			kdf = PPKArgon2i
		case "argon2d":
			kdf = ppkArgon2d // rejected by ppkDeriveV3 with an explanation
		}
		memory, passes, parallelism := opts.Memory, opts.Passes, opts.Parallelism
		if memory == 0 {
			memory = ppkDefaultMemory
		}
		if passes == 0 {
			passes = ppkDefaultPasses
		}
		if parallelism == 0 {
			parallelism = ppkDefaultParallelism
		}
		salt := make([]byte, ppkSaltLen)
		if encrypted {
			if _, err := rand.Read(salt); err != nil {
				return nil, err
			}
		}
		secrets, err = ppkDeriveV3(encrypted, passphrase, kdf, memory, passes, parallelism, salt)
		if err != nil {
			return nil, err
		}
		if encrypted {
			fmt.Fprintf(&out, "Key-Derivation: %s\n", kdf)
			fmt.Fprintf(&out, "Argon2-Memory: %d\n", memory)
			fmt.Fprintf(&out, "Argon2-Passes: %d\n", passes)
			fmt.Fprintf(&out, "Argon2-Parallelism: %d\n", parallelism)
			fmt.Fprintf(&out, "Argon2-Salt: %s\n", hex.EncodeToString(salt))
		}
	}
	defer secrets.wipe()

	mac := ppkMAC(secrets, algorithm, encryption, comment, public, private)
	stored := private
	if encrypted {
		c, err := aes.NewCipher(secrets.cipherKey)
		if err != nil {
			return nil, err
		}
		stored = make([]byte, len(private))
		cipher.NewCBCEncrypter(c, secrets.iv).CryptBlocks(stored, private)
	}
	writePPKBody(&out, "Private-Lines", stored)
	fmt.Fprintf(&out, "Private-MAC: %s\n", hex.EncodeToString(mac))
	return []byte(out.String()), nil
}

// writePPKBody writes a "<name>: <line count>" header followed by blob in
// base64, 64 characters per line.
func writePPKBody(out *strings.Builder, name string, blob []byte) {
	b64 := base64.StdEncoding.EncodeToString(blob)
	lines := (len(b64) + ppkLineWidth - 1) / ppkLineWidth
	fmt.Fprintf(out, "%s: %d\n", name, lines)
	for len(b64) > 0 {
		n := min(len(b64), ppkLineWidth)
		out.WriteString(b64[:n])
		out.WriteByte('\n')
		b64 = b64[n:]
	}
}
