package keys

import (
	"crypto/sha512"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/blowfish" //nolint:staticcheck // required by the OpenSSH key format
)

// bcryptPBKDF implements bcrypt_pbkdf, the password-based key derivation
// function OpenSSH uses to protect "openssh-key-v1" private keys. It is
// PBKDF2 with SHA-512 as the outer hash and a bcrypt variant ("bcrypt_hash")
// as the pseudo-random function, with the output bytes of the individual
// blocks interleaved rather than concatenated.
//
// golang.org/x/crypto has an implementation, but only as an internal package,
// so it is reimplemented here on top of the exported Blowfish primitives.
func bcryptPBKDF(passphrase, salt []byte, rounds, keyLen int) ([]byte, error) {
	const hashLen = 32
	switch {
	case rounds < 1:
		return nil, errors.New("bcrypt_pbkdf: rounds must be positive")
	case len(passphrase) == 0:
		return nil, errors.New("bcrypt_pbkdf: empty passphrase")
	case len(salt) == 0 || len(salt) > 1<<20:
		return nil, errors.New("bcrypt_pbkdf: invalid salt length")
	case keyLen < 1 || keyLen > 1024:
		return nil, errors.New("bcrypt_pbkdf: invalid key length")
	}

	blocks := (keyLen + hashLen - 1) / hashLen
	interleaved := make([]byte, blocks*hashLen)
	defer zero(interleaved)

	passHash := sha512.Sum512(passphrase)
	defer zero(passHash[:])

	var (
		saltHash [sha512.Size]byte
		block    [hashLen]byte
		acc      [hashLen]byte
		counter  [4]byte
	)
	defer zero(block[:])
	defer zero(acc[:])

	h := sha512.New()
	for n := 1; n <= blocks; n++ {
		// First iteration: the salt is extended with the block counter.
		binary.BigEndian.PutUint32(counter[:], uint32(n))
		h.Reset()
		h.Write(salt)
		h.Write(counter[:])
		h.Sum(saltHash[:0])
		if err := bcryptHash(block[:], passHash[:], saltHash[:]); err != nil {
			return nil, err
		}
		acc = block

		// Remaining iterations chain on the previous output.
		for i := 1; i < rounds; i++ {
			saltHash = sha512.Sum512(block[:])
			if err := bcryptHash(block[:], passHash[:], saltHash[:]); err != nil {
				return nil, err
			}
			for j := range acc {
				acc[j] ^= block[j]
			}
		}

		// Byte i of block n lands at position i*blocks + (n-1).
		for i, b := range acc {
			interleaved[i*blocks+n-1] = b
		}
	}

	key := make([]byte, keyLen)
	copy(key, interleaved)
	return key, nil
}

// bcryptHash is the PRF of bcrypt_pbkdf: Blowfish keyed EksBlowfish-style
// with 64 rounds of the hashed passphrase and salt, then used to encrypt a
// fixed 32-byte string 64 times. out must be 32 bytes long.
func bcryptHash(out, passHash, saltHash []byte) error {
	c, err := blowfish.NewSaltedCipher(passHash, saltHash)
	if err != nil {
		return err
	}
	for range 64 {
		blowfish.ExpandKey(saltHash, c)
		blowfish.ExpandKey(passHash, c)
	}

	copy(out, "OxychromaticBlowfishSwatDynamite")
	for off := 0; off < len(out); off += blowfish.BlockSize {
		blk := out[off : off+blowfish.BlockSize]
		for range 64 {
			c.Encrypt(blk, blk)
		}
	}
	// The reference implementation emits each 32-bit word little-endian.
	for off := 0; off < len(out); off += 4 {
		out[off], out[off+1], out[off+2], out[off+3] = out[off+3], out[off+2], out[off+1], out[off]
	}
	return nil
}
