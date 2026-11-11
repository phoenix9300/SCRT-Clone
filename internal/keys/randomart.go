package keys

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Dimensions of the random art field, as in OpenSSH.
const (
	artWidth  = 17
	artHeight = 9
)

// RandomArt renders the "drunken bishop" visual fingerprint of a public key
// exactly as "ssh-keygen -lv" prints it for the default SHA-256 hash: a
// frame titled with the key type and size, the 17x9 field, and a "[SHA256]"
// footer.
//
//	+--[ED25519 256]--+
//	|        .o+*=.   |
//	|        ...      |
//	+----[SHA256]-----+
//
// Every line, including the last, ends in a newline.
func RandomArt(pub ssh.PublicKey) string {
	blob := fingerprintBlob(pub)
	if blob == nil {
		return ""
	}
	digest := sha256.Sum256(blob)
	name, bits := KeyTypeDisplay(pub)
	return randomArt(digest[:], name, bits, "SHA256")
}

// randomArt implements OpenSSH's fingerprint_randomart: a bishop starts in
// the middle of the board and makes four diagonal moves per digest byte,
// taking two bits at a time from the least significant end. Each visit bumps
// the square's counter, which selects the character drawn there; the start
// and end squares are marked S and E.
func randomArt(digest []byte, keyName string, bits int, hashName string) string {
	const symbols = " .o+=*BOX@%&#/^SE"
	const last = byte(len(symbols) - 1)

	var field [artWidth][artHeight]byte
	x, y := artWidth/2, artHeight/2
	for _, b := range digest {
		for range 4 {
			if b&1 != 0 {
				x++
			} else {
				x--
			}
			if b&2 != 0 {
				y++
			} else {
				y--
			}
			x = min(max(x, 0), artWidth-1)
			y = min(max(y, 0), artHeight-1)
			if field[x][y] < last-2 {
				field[x][y]++
			}
			b >>= 2
		}
	}
	field[artWidth/2][artHeight/2] = last - 1
	field[x][y] = last

	// ssh-keygen formats the title into a 17-byte buffer: if "[TYPE bits]"
	// would overflow it the size is dropped, and whatever is left is cut to
	// 16 characters.
	title := fmt.Sprintf("[%s %d]", keyName, bits)
	if len(title) > artWidth {
		title = fmt.Sprintf("[%s]", keyName)
	}
	if len(title) > artWidth-1 {
		title = title[:artWidth-1]
	}
	footer := "[" + hashName + "]"
	if len(footer) > artWidth-1 {
		footer = footer[:artWidth-1]
	}

	var b strings.Builder
	border := func(label string) {
		left := (artWidth - len(label)) / 2
		b.WriteByte('+')
		b.WriteString(strings.Repeat("-", left))
		b.WriteString(label)
		b.WriteString(strings.Repeat("-", artWidth-left-len(label)))
		b.WriteString("+\n")
	}
	border(title)
	for row := range artHeight {
		b.WriteByte('|')
		for col := range artWidth {
			b.WriteByte(symbols[min(field[col][row], last)])
		}
		b.WriteString("|\n")
	}
	border(footer)
	return b.String()
}
