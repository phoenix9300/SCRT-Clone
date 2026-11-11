package keys

import (
	"bytes"
	"crypto"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// errSSHCom is returned for the proprietary ssh.com private key format.
var errSSHCom = fmt.Errorf("%w: ssh.com (SSH2 ENCRYPTED PRIVATE KEY) private key files; convert the key with PuTTYgen first", ErrUnsupported)

// Info describes a private key file. It is produced by [Inspect] without
// decrypting anything, so fields that live inside the encrypted part of a
// file are left at their zero value.
type Info struct {
	// Format is the container format.
	Format Format
	// Type is the key algorithm, or "" when it cannot be determined
	// without the passphrase (encrypted PKCS#8).
	Type Type
	// Encrypted reports whether a passphrase is needed to load the key.
	Encrypted bool
	// Comment is the key's comment. PEM files have none, and an encrypted
	// OpenSSH file keeps it inside the encrypted section.
	Comment string
	// Bits is the key size as reported by ssh-keygen, or 0 if unknown
	// (encrypted PEM files).
	Bits int

	// Variant names the exact encoding, for display: "openssh-key-v1",
	// "PKCS#1", "SEC1", "OpenSSL DSA", "PKCS#8", "PPK v2" or "PPK v3".
	Variant string
	// Cipher and KDF name the encryption and passphrase hashing in use,
	// for example "aes256-ctr" and "bcrypt". Both are empty for
	// unencrypted files.
	Cipher string
	KDF    string
	// PublicKey is the public half when the file exposes it without the
	// passphrase: always for OpenSSH and PPK files, and for unencrypted
	// PEM files. It is nil otherwise.
	PublicKey ssh.PublicKey
}

// typeOfAlgorithm maps an SSH key algorithm name to a Type.
func typeOfAlgorithm(algo string) Type {
	algo = strings.TrimSuffix(algo, certAlgoSuffix)
	switch algo {
	case ssh.KeyAlgoRSA:
		return RSA
	case keyAlgoDSA:
		return DSA
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		return ECDSA
	case ssh.KeyAlgoED25519:
		return Ed25519
	}
	return ""
}

// findPrivatePEM returns the first PEM block in data that holds a private
// key. If there is none it returns the label of the first block of any other
// kind, to make error messages helpful.
func findPrivatePEM(data []byte) (block *pem.Block, otherType string) {
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, otherType
		}
		if b.Type == pemTypeOpenSSH || isPEMPrivateType(b.Type) {
			return b, ""
		}
		if otherType == "" {
			otherType = b.Type
		}
	}
}

// classify performs the format detection shared by Inspect and
// ParsePrivateKey. Exactly one of the results is meaningful: isPPK, a PEM
// block, or an error.
func classify(data []byte) (ppk bool, block *pem.Block, err error) {
	if isPPK(data) {
		return true, nil, nil
	}
	if bytes.Contains(data, []byte("---- BEGIN SSH2 ENCRYPTED PRIVATE KEY")) {
		return false, nil, errSSHCom
	}
	block, other := findPrivatePEM(data)
	switch {
	case block != nil:
		return false, block, nil
	case other != "":
		return false, nil, fmt.Errorf("%w: PEM block %q is not a private key", ErrUnknownFormat, other)
	case bytes.Contains(data, []byte("---- BEGIN SSH2 PUBLIC KEY")):
		return false, nil, fmt.Errorf("%w: this is a public key file, not a private key", ErrUnknownFormat)
	case bytes.Contains(data, []byte("-----BEGIN ")):
		return false, nil, fmt.Errorf("%w: damaged PEM armour", ErrMalformed)
	}
	if _, _, _, _, perr := ssh.ParseAuthorizedKey(data); perr == nil {
		return false, nil, fmt.Errorf("%w: this is a public key file, not a private key", ErrUnknownFormat)
	}
	return false, nil, ErrUnknownFormat
}

// ParsePrivateKey reads a private key, auto-detecting the container format
// (OpenSSH, classic PEM or PuTTY PPK).
//
// passphrase is only consulted for encrypted keys; it is ignored otherwise,
// so a cached passphrase can be passed unconditionally. If the key is
// encrypted and passphrase is empty the error is [ErrPassphraseRequired]; if
// the passphrase does not decrypt the key it is [ErrWrongPassphrase]. Text
// surrounding a PEM block is ignored, and in a file with several PEM blocks
// the first private key is used.
func ParsePrivateKey(data []byte, passphrase []byte) (*PrivateKey, error) {
	data = trimBOM(data)
	ppk, block, err := classify(data)
	if err != nil {
		return nil, err
	}

	var (
		signer  crypto.Signer
		comment string
		format  Format
	)
	switch {
	case ppk:
		format = FormatPPK
		signer, comment, err = parsePPK(data, passphrase)
	case block.Type == pemTypeOpenSSH:
		format = FormatOpenSSH
		signer, comment, err = parseOpenSSHPrivate(block.Bytes, passphrase)
	default:
		format = FormatPEM
		signer, err = parsePEMPrivate(block, passphrase)
	}
	if err != nil {
		return nil, err
	}
	return &PrivateKey{Signer: signer, Comment: comment, Format: format}, nil
}

// Inspect reports the format, key type, encryption state, comment and size
// of a private key file without needing its passphrase.
func Inspect(data []byte) (Info, error) {
	data = trimBOM(data)
	ppk, block, err := classify(data)
	if err != nil {
		return Info{}, err
	}
	switch {
	case ppk:
		return inspectPPK(data)
	case block.Type == pemTypeOpenSSH:
		return inspectOpenSSH(block)
	default:
		return inspectPEM(block)
	}
}

// setPublic fills the fields of info that derive from a wire-format public
// key. Key types this package cannot load are still described as far as
// possible.
func (info *Info) setPublic(blob []byte) error {
	r := newWireReader(blob)
	algo := string(r.str())
	if r.err != nil {
		return fmt.Errorf("%w (public key)", r.err)
	}
	info.Type = typeOfAlgorithm(algo)
	info.Bits = publicKeyBits(blob)
	pub, err := parsePublicBlob(blob)
	if err != nil {
		if info.Type == "" {
			return nil // unsupported key type; report what we know
		}
		return err
	}
	info.PublicKey = pub
	return nil
}

func inspectPPK(data []byte) (Info, error) {
	f, err := parsePPKFile(data)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		Format:    FormatPPK,
		Encrypted: f.encrypted(),
		Comment:   f.comment,
		Variant:   fmt.Sprintf("PPK v%d", f.version),
	}
	if f.encrypted() {
		info.Cipher = f.encryption
		info.KDF = "SHA-1"
		if f.version == 3 {
			info.KDF = f.kdf
		}
	}
	if err := info.setPublic(f.public); err != nil {
		return Info{}, err
	}
	return info, nil
}

func inspectOpenSSH(block *pem.Block) (Info, error) {
	f, err := parseOpenSSHFile(block.Bytes)
	if err != nil {
		return Info{}, err
	}
	info := Info{
		Format:    FormatOpenSSH,
		Encrypted: f.encrypted(),
		Variant:   "openssh-key-v1",
	}
	if f.encrypted() {
		info.Cipher, info.KDF = f.cipher, f.kdf
	} else if plain, err := f.decrypt(nil); err == nil {
		// The comment sits after the key in the private section.
		if _, comment, err := parseOpenSSHPrivateSection(plain, false); err == nil {
			info.Comment = comment
		}
	}
	if err := info.setPublic(f.public); err != nil {
		return Info{}, err
	}
	return info, nil
}

func inspectPEM(block *pem.Block) (Info, error) {
	info := Info{Format: FormatPEM, Encrypted: pemEncrypted(block)}
	switch block.Type {
	case pemTypeRSA:
		info.Variant, info.Type = "PKCS#1", RSA
	case pemTypeEC:
		info.Variant, info.Type = "SEC1", ECDSA
	case pemTypeDSA:
		info.Variant, info.Type = "OpenSSL DSA", DSA
	default:
		info.Variant = "PKCS#8"
	}

	switch {
	case block.Type == pemTypePKCS8Encrypted:
		scheme, _, err := parseEncryptedPKCS8(block.Bytes)
		if err != nil {
			return Info{}, err
		}
		info.Cipher, info.KDF = scheme.cipher.name, scheme.kdf
		return info, nil
	case info.Encrypted:
		spec, _, err := legacyPEMParams(block)
		if err != nil {
			return Info{}, err
		}
		info.Cipher, info.KDF = spec.name, "MD5"
		return info, nil
	}

	signer, err := parsePEMPrivate(block, nil)
	if err != nil {
		return Info{}, err
	}
	key := &PrivateKey{Signer: signer}
	info.Type, info.Bits = key.Type(), key.Bits()
	if info.PublicKey, err = key.PublicKey(); err != nil {
		return Info{}, err
	}
	return info, nil
}

// isUnsupported reports whether err marks a recognised but unsupported
// feature, as opposed to undecodable data.
func isUnsupported(err error) bool {
	return errors.Is(err, ErrUnsupported)
}
