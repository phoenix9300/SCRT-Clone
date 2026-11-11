package keys

import (
	"errors"
	"fmt"
	"math"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrNotCertificate is returned by [ParseCertificate] when the input is a
// plain public key rather than an OpenSSH certificate.
var ErrNotCertificate = errors.New("keys: not an OpenSSH certificate")

// CertInfo is a display-oriented summary of an OpenSSH certificate.
type CertInfo struct {
	// Type is "user" or "host" (or "unknown (n)" for other values).
	Type string
	// KeyID is the identity the CA assigned ("ssh-keygen -I").
	KeyID string
	// Serial is the certificate serial number.
	Serial uint64
	// Principals lists the user or host names the certificate is valid
	// for. An empty list means any principal.
	Principals []string
	// ValidAfter and ValidBefore bound the validity period. A zero
	// ValidAfter means "always valid in the past" and a zero ValidBefore
	// means the certificate never expires (see Forever).
	ValidAfter, ValidBefore time.Time
	// CAFingerprint is the SHA-256 fingerprint of the signing CA key.
	CAFingerprint string
	// Extensions and CriticalOptions are the certificate's permissions,
	// e.g. "permit-pty": "" and "force-command": "/bin/date".
	Extensions, CriticalOptions map[string]string

	// Algorithm is the certificate type name, for example
	// "ssh-ed25519-cert-v01@openssh.com".
	Algorithm string
	// KeyType and KeyBits describe the certified key as ssh-keygen would,
	// e.g. "ED25519-CERT" and 256.
	KeyType string
	KeyBits int
	// KeyFingerprint is the SHA-256 fingerprint of the certified public
	// key (without the certificate), which is what "ssh-keygen -L" shows.
	KeyFingerprint string
	// CAType is the display name of the CA key type and
	// SignatureAlgorithm the algorithm it signed with, e.g. "RSA" and
	// "rsa-sha2-512".
	CAType             string
	SignatureAlgorithm string
	// Forever reports that the certificate has no expiry time.
	Forever bool
	// SignatureValid reports whether the CA signature over the
	// certificate verifies. It says nothing about whether the CA is
	// trusted or the certificate currently valid.
	SignatureValid bool
	// Comment is the trailing comment of the certificate line.
	Comment string
}

// ValidAt reports whether t falls inside the certificate's validity period.
func (c *CertInfo) ValidAt(t time.Time) bool {
	if c == nil {
		return false
	}
	if !c.ValidAfter.IsZero() && t.Before(c.ValidAfter) {
		return false
	}
	return c.Forever || t.Before(c.ValidBefore)
}

// IsCertificate reports whether pub is an OpenSSH certificate.
func IsCertificate(pub ssh.PublicKey) bool {
	_, ok := pub.(*ssh.Certificate)
	return ok
}

// ParseCertificate reads an OpenSSH certificate and summarises it. The usual
// input is the one-line format of "<key>-cert.pub" files
// ("ssh-ed25519-cert-v01@openssh.com AAAA... comment"), but anything
// [ParsePublicKey] accepts will do, including a PPK file with an embedded
// certificate. It returns ErrNotCertificate if the data holds an ordinary
// public key.
func ParseCertificate(data []byte) (*ssh.Certificate, *CertInfo, error) {
	pub, comment, err := ParsePublicKey(data)
	if err != nil {
		return nil, nil, err
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		return nil, nil, ErrNotCertificate
	}
	info := DescribeCertificate(cert)
	info.Comment = comment
	return cert, info, nil
}

// DescribeCertificate summarises an already parsed certificate.
func DescribeCertificate(cert *ssh.Certificate) *CertInfo {
	if cert == nil {
		return nil
	}
	info := &CertInfo{
		KeyID:           cert.KeyId,
		Serial:          cert.Serial,
		Principals:      append([]string(nil), cert.ValidPrincipals...),
		Extensions:      map[string]string{},
		CriticalOptions: map[string]string{},
	}
	if cert.Key != nil {
		info.Algorithm = cert.Type()
	}
	switch cert.CertType {
	case ssh.UserCert:
		info.Type = "user"
	case ssh.HostCert:
		info.Type = "host"
	default:
		info.Type = fmt.Sprintf("unknown (%d)", cert.CertType)
	}
	for k, v := range cert.Extensions {
		info.Extensions[k] = v
	}
	for k, v := range cert.CriticalOptions {
		info.CriticalOptions[k] = v
	}

	if cert.ValidAfter != 0 {
		info.ValidAfter = certTime(cert.ValidAfter)
	}
	if cert.ValidBefore >= math.MaxInt64 {
		// OpenSSH writes 2^64-1 for "forever"; anything beyond the range
		// of a signed timestamp is treated the same way.
		info.Forever = true
	} else {
		info.ValidBefore = certTime(cert.ValidBefore)
	}

	info.KeyType, info.KeyBits = KeyTypeDisplay(cert)
	info.KeyFingerprint = FingerprintSHA256(cert)
	if cert.SignatureKey != nil {
		info.CAFingerprint = FingerprintSHA256(cert.SignatureKey)
		info.CAType, _ = KeyTypeDisplay(cert.SignatureKey)
	}
	if cert.Signature != nil {
		info.SignatureAlgorithm = cert.Signature.Format
	}
	info.SignatureValid = verifyCertSignature(cert)
	return info
}

// certTime converts a certificate timestamp (seconds since the Unix epoch).
func certTime(v uint64) time.Time {
	const maxSeconds = 253402300799 // 9999-12-31T23:59:59Z, the limit of time formatting
	return time.Unix(int64(min(v, maxSeconds)), 0)
}

// verifyCertSignature checks the CA's signature. The signed data is the
// certificate encoding up to, but not including, the signature field.
func verifyCertSignature(cert *ssh.Certificate) bool {
	if cert.Signature == nil {
		return false
	}
	unsigned := *cert
	unsigned.Signature = nil
	body := wireBlob(&unsigned)
	if len(body) < 4 {
		return false
	}
	return cert.SignatureKey.Verify(body[:len(body)-4], cert.Signature) == nil
}
