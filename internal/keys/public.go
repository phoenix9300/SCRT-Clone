package keys

import (
	"bytes"
	"crypto/dsa" //nolint:staticcheck // legacy keys must still be readable
	"crypto/md5" //nolint:gosec // legacy fingerprint format
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

const (
	certAlgoSuffix = "-cert-v01@openssh.com"

	rfc4716Begin = "---- BEGIN SSH2 PUBLIC KEY ----"
	rfc4716End   = "---- END SSH2 PUBLIC KEY ----"
	// rfc4716LineLimit is the maximum line length RFC 4716 allows, and
	// rfc4716DataWidth the width ssh-keygen wraps the key data to.
	rfc4716LineLimit = 72
	rfc4716DataWidth = 70
	// rfc4716MaxValue is the longest header value RFC 4716 allows.
	rfc4716MaxValue = 1024

	pemTypePublicPKIX  = "PUBLIC KEY"
	pemTypePublicPKCS1 = "RSA PUBLIC KEY"
)

// trimBOM strips a UTF-8 byte order mark, which Windows editors like to add.
func trimBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
}

// sanitizeComment makes a comment safe for the single-line fields the key
// formats store it in.
func sanitizeComment(s string) string {
	if !strings.ContainsAny(s, "\r\n\x00") {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', 0:
			return ' '
		}
		return r
	}, s)
}

// parsePublicBlob decodes an SSH wire-format public key, including the
// "ssh-dss" keys golang.org/x/crypto/ssh no longer understands.
func parsePublicBlob(blob []byte) (ssh.PublicKey, error) {
	r := newWireReader(blob)
	algo := string(r.str())
	if r.err != nil {
		return nil, fmt.Errorf("%w (public key)", r.err)
	}
	if algo == keyAlgoDSA {
		return parseDSAPublicBlob(blob)
	}
	pub, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return pub, nil
}

// MarshalAuthorizedKey formats a public key as a line for authorized_keys or
// a ".pub" file: "ssh-ed25519 AAAA... comment\n". The comment is omitted when
// empty.
func MarshalAuthorizedKey(pub ssh.PublicKey, comment string) []byte {
	blob := wireBlob(pub)
	if blob == nil {
		return nil
	}
	var b bytes.Buffer
	b.WriteString(pub.Type())
	b.WriteByte(' ')
	b.WriteString(base64.StdEncoding.EncodeToString(blob))
	if comment = strings.TrimSpace(sanitizeComment(comment)); comment != "" {
		b.WriteByte(' ')
		b.WriteString(comment)
	}
	b.WriteByte('\n')
	return b.Bytes()
}

// MarshalRFC4716 formats a public key in the SSH2 / ssh.com public key file
// format of RFC 4716, as produced by "ssh-keygen -e":
//
//	---- BEGIN SSH2 PUBLIC KEY ----
//	Comment: "user@host"
//	AAAAC3NzaC1lZDI1NTE5AAAAI...
//	---- END SSH2 PUBLIC KEY ----
//
// The key data is wrapped at 70 columns. The Comment header is omitted when
// comment is empty; its value is quoted, with embedded quotes and
// backslashes escaped the way PuTTY does. Header lines that would exceed
// the 72-byte limit of the RFC are continued with a trailing backslash,
// which ssh-keygen understands but PuTTY does not, so keep comments short
// (about 60 bytes) if the file is destined for PuTTY.
func MarshalRFC4716(pub ssh.PublicKey, comment string) []byte {
	blob := wireBlob(pub)
	if blob == nil {
		return nil
	}
	var b bytes.Buffer
	b.WriteString(rfc4716Begin + "\n")
	if comment = strings.TrimSpace(sanitizeComment(comment)); comment != "" {
		esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(strings.ToValidUTF8(comment, "?"))
		for len(esc) > rfc4716MaxValue-2 {
			// Drop whole runes (and never half an escape) to fit the limit.
			_, size := utf8.DecodeLastRuneInString(esc)
			esc = esc[:len(esc)-size]
			if n := len(esc) - len(strings.TrimRight(esc, `\`)); n%2 == 1 {
				esc = esc[:len(esc)-1]
			}
		}
		writeRFC4716Header(&b, `Comment: "`+esc+`"`)
	}
	data := base64.StdEncoding.EncodeToString(blob)
	for len(data) > 0 {
		n := min(len(data), rfc4716DataWidth)
		b.WriteString(data[:n])
		b.WriteByte('\n')
		data = data[n:]
	}
	b.WriteString(rfc4716End + "\n")
	return b.Bytes()
}

// writeRFC4716Header writes one logical header line, folding it into
// physical lines of at most 72 bytes joined by trailing backslashes.
//
// ssh-keygen's importer treats every line that contains ": " or starts with
// "----" as a new header rather than as a continuation, and then loses count
// of the lines to skip. The fold points are therefore chosen so that no
// continuation line does either: a line is folded between a colon and the
// space after it, and a run of dashes is spread over lines of three.
func writeRFC4716Header(b *bytes.Buffer, line string) {
	for first := true; ; first = false {
		looksLikeHeader := !first && (strings.Contains(line, ": ") || strings.HasPrefix(line, "----"))
		if len(line) <= rfc4716LineLimit && !looksLikeHeader {
			b.WriteString(line)
			b.WriteByte('\n')
			return
		}
		n := min(len(line), rfc4716LineLimit-1) // leave room for the backslash
		if !first {
			if strings.HasPrefix(line, "----") {
				n = 3
			} else if i := strings.Index(line[:n], ": "); i >= 0 {
				n = i + 1
			}
		}
		for n > 1 && n < len(line) && !utf8.RuneStart(line[n]) {
			n--
		}
		b.WriteString(line[:n])
		b.WriteString("\\\n")
		line = line[n:]
	}
}

// parseRFC4716 decodes an RFC 4716 public key block.
func parseRFC4716(data []byte) (ssh.PublicKey, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	i := 0
	for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "---- BEGIN SSH2 PUBLIC KEY") {
		i++
	}
	if i == len(lines) {
		return nil, "", fmt.Errorf("%w: no RFC 4716 public key block", ErrUnknownFormat)
	}
	i++

	var (
		comment string
		b64     strings.Builder
		ended   bool
	)
	inHeaders := true
	for i < len(lines) && !ended {
		line := strings.TrimRight(lines[i], " \t")
		i++
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "---- END "):
			ended = true
		case inHeaders && strings.Contains(line, ":"):
			// Join continuation lines.
			for strings.HasSuffix(line, `\`) && i < len(lines) {
				line = line[:len(line)-1] + strings.TrimRight(lines[i], " \t")
				i++
			}
			tag, value, _ := strings.Cut(line, ":")
			if strings.EqualFold(strings.TrimSpace(tag), "Comment") {
				comment = unquoteRFC4716(strings.TrimSpace(value))
			}
		default:
			inHeaders = false
			b64.WriteString(strings.TrimSpace(line))
		}
	}
	if !ended {
		return nil, "", fmt.Errorf("%w: RFC 4716 block has no END line", ErrMalformed)
	}
	blob, err := base64.StdEncoding.DecodeString(b64.String())
	if err != nil {
		return nil, "", fmt.Errorf("%w: bad base64 in RFC 4716 block", ErrMalformed)
	}
	pub, err := parsePublicBlob(blob)
	if err != nil {
		return nil, "", err
	}
	return pub, comment, nil
}

// unquoteRFC4716 strips the customary quotation marks from a Comment header
// value and undoes PuTTY-style backslash escapes inside them.
func unquoteRFC4716(v string) string {
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return v
	}
	v = v[1 : len(v)-1]
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// ParsePublicKey reads a public key and its comment from any of:
//
//   - an authorized_keys / ".pub" line (options before the key type are
//     skipped; an OpenSSH certificate is returned as *ssh.Certificate);
//   - an RFC 4716 "---- BEGIN SSH2 PUBLIC KEY ----" block;
//   - a PEM "PUBLIC KEY" (PKIX) or "RSA PUBLIC KEY" (PKCS#1) block;
//   - a private key file. PPK and OpenSSH files store the public key in the
//     clear, so no passphrase is needed (the comment of an encrypted OpenSSH
//     key is unavailable, though). Unencrypted PEM private keys work too;
//     encrypted ones yield ErrPassphraseRequired.
func ParsePublicKey(data []byte) (pub ssh.PublicKey, comment string, err error) {
	data = trimBOM(data)
	trimmed := bytes.TrimSpace(data)
	switch {
	case len(trimmed) == 0:
		return nil, "", fmt.Errorf("%w: empty input", ErrUnknownFormat)
	case isPPK(data):
		f, err := parsePPKFile(data)
		if err != nil {
			return nil, "", err
		}
		pub, err := parsePublicBlob(f.public)
		if err != nil {
			return nil, "", err
		}
		return pub, f.comment, nil
	case bytes.Contains(data, []byte("---- BEGIN SSH2 PUBLIC KEY")):
		return parseRFC4716(data)
	case bytes.Contains(data, []byte("---- BEGIN SSH2 ENCRYPTED PRIVATE KEY")):
		return nil, "", errSSHCom
	}

	if block, _ := pem.Decode(data); block != nil {
		return publicFromPEM(block)
	}
	return parseAuthorizedKeyLine(data)
}

// publicFromPEM extracts a public key from a PEM block of any supported
// kind.
func publicFromPEM(block *pem.Block) (ssh.PublicKey, string, error) {
	switch {
	case block.Type == pemTypeOpenSSH:
		f, err := parseOpenSSHFile(block.Bytes)
		if err != nil {
			return nil, "", err
		}
		pub, err := parsePublicBlob(f.public)
		if err != nil {
			return nil, "", err
		}
		comment := ""
		if !f.encrypted() {
			if plain, err := f.decrypt(nil); err == nil {
				if _, c, err := parseOpenSSHPrivateSection(plain, false); err == nil {
					comment = c
				}
			}
		}
		return pub, comment, nil
	case isPEMPrivateType(block.Type):
		signer, err := parsePEMPrivate(block, nil)
		if err != nil {
			return nil, "", err
		}
		pub, err := publicKeyOf(signer)
		return pub, "", err
	case block.Type == pemTypePublicPKIX:
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if d, ok := key.(*dsa.PublicKey); ok {
			pub, err := newDSAPublicKey(d)
			return pub, "", err
		}
		pub, err := ssh.NewPublicKey(key)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrUnsupported, err)
		}
		return pub, "", nil
	case block.Type == pemTypePublicPKCS1:
		key, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		pub, err := ssh.NewPublicKey(key)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrUnsupported, err)
		}
		return pub, "", nil
	}
	return nil, "", fmt.Errorf("%w: PEM block %q does not hold an SSH key", ErrUnknownFormat, block.Type)
}

// parseAuthorizedKeyLine finds the first key in authorized_keys formatted
// text.
func parseAuthorizedKeyLine(data []byte) (ssh.PublicKey, string, error) {
	if pub, comment, _, _, err := ssh.ParseAuthorizedKey(data); err == nil {
		return pub, comment, nil
	}
	// golang.org/x/crypto/ssh rejects DSA keys; look for one ourselves.
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] != keyAlgoDSA {
				continue
			}
			blob, err := base64.StdEncoding.DecodeString(fields[i+1])
			if err != nil {
				continue
			}
			pub, err := parseDSAPublicBlob(blob)
			if err != nil {
				return nil, "", err
			}
			return pub, strings.Join(fields[i+2:], " "), nil
		}
	}
	return nil, "", fmt.Errorf("%w: no public key found", ErrUnknownFormat)
}

// wireBlob returns the wire encoding of pub, or nil if there is none. It
// exists because ssh.Certificate.Marshal panics on a certificate that lacks
// its key or CA key, which can only happen to hand-assembled values.
func wireBlob(pub ssh.PublicKey) []byte {
	if pub == nil {
		return nil
	}
	if cert, ok := pub.(*ssh.Certificate); ok {
		if cert == nil || cert.Key == nil || cert.SignatureKey == nil {
			return nil
		}
	}
	return pub.Marshal()
}

// fingerprintBlob returns the bytes OpenSSH hashes to fingerprint a key: its
// wire encoding, or for a certificate the encoding of the certified key.
func fingerprintBlob(pub ssh.PublicKey) []byte {
	if cert, ok := pub.(*ssh.Certificate); ok && cert != nil && cert.Key != nil {
		return wireBlob(cert.Key)
	}
	return wireBlob(pub)
}

// FingerprintSHA256 returns the SHA-256 fingerprint of a public key in the
// form "SHA256:<unpadded base64>", identical to "ssh-keygen -lf". Like
// OpenSSH, it fingerprints the certified key when given a certificate, so a
// key and its certificate share a fingerprint.
func FingerprintSHA256(pub ssh.PublicKey) string {
	blob := fingerprintBlob(pub)
	if blob == nil {
		return ""
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// FingerprintMD5 returns the legacy MD5 fingerprint of a public key in the
// form "MD5:aa:bb:...", identical to "ssh-keygen -E md5 -lf".
func FingerprintMD5(pub ssh.PublicKey) string {
	blob := fingerprintBlob(pub)
	if blob == nil {
		return ""
	}
	sum := md5.Sum(blob) //nolint:gosec
	var b strings.Builder
	b.WriteString("MD5")
	for _, c := range sum {
		b.WriteByte(':')
		b.WriteString(hex.EncodeToString([]byte{c}))
	}
	return b.String()
}

// KeyTypeDisplay returns the short name and size ssh-keygen prints for a
// public key, for example ("RSA", 3072), ("ECDSA", 256), ("ED25519", 256) or
// ("ED25519-CERT", 256) for a certificate. For key types whose size cannot
// be determined bits is 0.
func KeyTypeDisplay(pub ssh.PublicKey) (name string, bits int) {
	blob := wireBlob(pub)
	if blob == nil {
		return "", 0
	}
	return HostKeyAlgorithmDisplay(pub.Type()), publicKeyBits(blob)
}

// publicKeyBits extracts the key size from a wire-format public key or
// certificate without depending on the concrete ssh.PublicKey type.
func publicKeyBits(blob []byte) int {
	r := newWireReader(blob)
	algo := string(r.str())
	if base, ok := strings.CutSuffix(algo, certAlgoSuffix); ok {
		algo = base
		r.str() // certificate nonce
	}
	if r.err != nil {
		return 0
	}
	switch algo {
	case ssh.KeyAlgoRSA:
		r.str() // e
		n := r.mpint()
		if r.err != nil {
			return 0
		}
		return n.BitLen()
	case keyAlgoDSA:
		p := r.mpint()
		if r.err != nil {
			return 0
		}
		return p.BitLen()
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoSKECDSA256, "sk-ecdsa-sha2-nistp256":
		return 256
	case ssh.KeyAlgoECDSA384:
		return 384
	case ssh.KeyAlgoECDSA521:
		return 521
	case ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519, "sk-ssh-ed25519":
		return 256
	case "ssh-ed448":
		return 448
	}
	return 0
}

// HostKeyAlgorithmDisplay maps an SSH public key or host key algorithm name
// to the short label OpenSSH uses when talking about keys of that kind, as
// in "ED25519 key fingerprint is SHA256:...":
//
//	ssh-rsa, rsa-sha2-256, rsa-sha2-512        RSA
//	ssh-dss                                    DSA
//	ecdsa-sha2-nistp256/384/521                ECDSA
//	ssh-ed25519                                ED25519
//	sk-ecdsa-sha2-nistp256@openssh.com         ECDSA-SK
//	sk-ssh-ed25519@openssh.com                 ED25519-SK
//	<any of the above>-cert-v01@openssh.com    RSA-CERT, ED25519-CERT, ...
//
// Unknown names are returned unchanged.
func HostKeyAlgorithmDisplay(algo string) string {
	base, isCert := strings.CutSuffix(algo, certAlgoSuffix)
	if !isCert {
		// x/crypto's ML-DSA certificates use a different authority.
		base, isCert = strings.CutSuffix(algo, "-cert-v01@golang.org")
	}
	var name string
	switch base {
	case ssh.KeyAlgoRSA, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512:
		name = "RSA"
	case keyAlgoDSA:
		name = "DSA"
	case ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521:
		name = "ECDSA"
	case ssh.KeyAlgoED25519:
		name = "ED25519"
	case "ssh-ed448":
		name = "ED448"
	case ssh.KeyAlgoSKECDSA256, "webauthn-sk-ecdsa-sha2-nistp256@openssh.com", "sk-ecdsa-sha2-nistp256":
		// The last form is what remains of the certificate type name.
		name = "ECDSA-SK"
	case ssh.KeyAlgoSKED25519, "sk-ssh-ed25519":
		name = "ED25519-SK"
	case "ssh-xmss@openssh.com":
		name = "XMSS"
	case "ssh-mldsa-44":
		name = "ML-DSA-44"
	case "ssh-mldsa-65":
		name = "ML-DSA-65"
	case "ssh-mldsa-87":
		name = "ML-DSA-87"
	default:
		return algo
	}
	if isCert {
		name += "-CERT"
	}
	return name
}
