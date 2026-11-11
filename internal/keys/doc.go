// Package keys is Hawser's SSH key toolkit. It backs the "Key Generation
// Wizard", "Convert Private Key" and "Export Public Key" features and loads
// users' existing identity files regardless of the container they are stored
// in.
//
// # Private keys
//
// [ParsePrivateKey] auto-detects and reads
//
//   - OpenSSH "openssh-key-v1" files (BEGIN OPENSSH PRIVATE KEY), plain or
//     bcrypt-protected with any of the AES-CTR, AES-CBC, AES-GCM, 3DES-CBC or
//     ChaCha20-Poly1305 ciphers ssh-keygen can produce;
//   - classic PEM files: PKCS#1 (RSA PRIVATE KEY), SEC1 (EC PRIVATE KEY),
//     OpenSSL DSA (DSA PRIVATE KEY) including the legacy
//     "Proc-Type: 4,ENCRYPTED" envelope, PKCS#8 (PRIVATE KEY) and encrypted
//     PKCS#8 (ENCRYPTED PRIVATE KEY, PBES2 with PBKDF2 or scrypt and the
//     PBES1 DES schemes);
//   - PuTTY .ppk files, versions 2 and 3 (Argon2id and Argon2i).
//
// The result is a [PrivateKey] whose Signer field holds one of
// *rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey (always the value
// type, never a pointer) or *[DSAPrivateKey]. [PrivateKey.Marshal],
// [PrivateKey.MarshalPEM] and [PrivateKey.MarshalPPK] write the key back out
// in any of the three container families, and [Generate] creates new RSA,
// ECDSA and Ed25519 keys. DSA keys are legacy: they can be read and converted
// but not generated.
//
// [Inspect] describes a key file (format, key type, encryption, comment and
// public key) without needing the passphrase.
//
// # Errors
//
// Passphrase handling is reported through two sentinel errors that callers
// can test with errors.Is: [ErrPassphraseRequired] when the file is encrypted
// and no passphrase was supplied, and [ErrWrongPassphrase] when decryption
// with the supplied passphrase failed. [ErrUnknownFormat], [ErrMalformed],
// [ErrUnsupported] and [ErrMACMismatch] classify the remaining failures. No
// function in this package panics on malformed input.
//
// # Public keys
//
// [ParsePublicKey] reads authorized_keys lines, RFC 4716 blocks, PKIX/PKCS#1
// PEM public keys and the public half of any private key file.
// [MarshalAuthorizedKey] and [MarshalRFC4716] export them, and
// [FingerprintSHA256], [FingerprintMD5] and [RandomArt] reproduce the output
// of ssh-keygen -l / -lv exactly. [ParseCertificate] decodes OpenSSH
// certificates.
//
// # Resource limits
//
// Key files are untrusted input, and their key-derivation parameters decide
// how much CPU and memory a decryption attempt costs. The parsers therefore
// refuse files asking for more than 2048 bcrypt rounds (the limit of
// golang.org/x/crypto/ssh), 1 GiB of Argon2 memory, 1024 Argon2 passes or a
// memory-times-passes product above 2^25 KiB, 10 million PBKDF2 iterations or
// 1 GiB of scrypt memory, as well as RSA moduli above 16384 bits. Such files
// fail with ErrUnsupported.
//
// # Sensitive data
//
// Derived encryption keys and intermediate plaintext buffers are zeroed
// before the functions return. Passphrase slices belong to the caller and are
// never retained or modified. Go's garbage collector may still have copied
// key material (big integers in particular), so this is best effort rather
// than a guarantee.
package keys
