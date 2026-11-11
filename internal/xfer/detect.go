package xfer

// DetectKind is the result of feeding terminal output to a Detector.
type DetectKind int

// Detection results.
const (
	// DetectNone: no ZMODEM session start in the data.
	DetectNone DetectKind = iota
	// DetectReceive: the remote sender ("sz") announced itself with ZRQINIT;
	// the host must receive (ZmodemReceive).
	DetectReceive
	// DetectSend: the remote receiver ("rz") announced itself with ZRINIT;
	// the host must send (ZmodemSend).
	DetectSend
)

// String returns the name of the detection result.
func (k DetectKind) String() string {
	switch k {
	case DetectNone:
		return "DetectNone"
	case DetectReceive:
		return "DetectReceive"
	case DetectSend:
		return "DetectSend"
	}
	return "DetectKind(?)"
}

// detectPattern is the common beginning of the two announcement headers:
// ZPAD ZPAD ZDLE 'B' (hex header) followed by the frame type in hex, "00"
// for ZRQINIT or "01" for ZRINIT.
const detectPattern = "**\x18B0"

// detectPrefix is the command lrzsz's sz prints ahead of its ZRQINIT to
// start a receiver on the far side.
const detectPrefix = "rz\r"

// Detector scans terminal output for the start of an auto-ZMODEM session.
// Its state survives chunk boundaries, so the announcement is found even
// when it is split across reads. The zero value is ready to use.
type Detector struct {
	matched int // bytes of detectPattern matched so far
	prefix  int // bytes of detectPrefix matched directly before the pattern
	run     int // bytes of detectPrefix matched in the input so far
}

// Reset forgets a partially matched announcement.
func (d *Detector) Reset() { *d = Detector{} }

// Feed returns the kind and the index in p where the ZMODEM header begins
// ("**\x18B00"/"**\x18B01" hex headers, with optional leading "rz\r"), so
// the host can print p[:idx] to the terminal and hand the remainder to the
// transfer.
//
// When nothing is detected Feed returns (DetectNone, len(p)): all of p is
// terminal output. When the announcement began in an earlier chunk (which
// the host has already printed) idx is 0; the transfer functions cope with
// the truncated header. After a detection the Detector is reset.
func (d *Detector) Feed(p []byte) (kind DetectKind, idx int) {
	for i, c := range p {
		if d.matched == len(detectPattern) {
			// The frame type's second digit decides.
			switch c {
			case '0':
				kind = DetectReceive
			case '1':
				kind = DetectSend
			}
			if kind != DetectNone {
				idx = max(i-len(detectPattern)-d.prefix, 0)
				d.Reset()
				return kind, idx
			}
			d.matched, d.prefix = 0, 0
		}
		switch {
		case c == detectPattern[d.matched]:
			if d.matched == 0 {
				d.prefix = 0
				if d.run == len(detectPrefix) {
					d.prefix = d.run
				}
			}
			d.matched++
		case d.matched == 2 && c == '*':
			// A longer run of ZPADs: still two of them before the ZDLE.
		case c == detectPattern[0]:
			d.matched, d.prefix = 1, 0
		default:
			d.matched, d.prefix = 0, 0
		}
		// Track "rz\r" directly ahead of the pattern.
		switch {
		case d.run < len(detectPrefix) && c == detectPrefix[d.run]:
			d.run++
		case c == detectPrefix[0]:
			d.run = 1
		default:
			d.run = 0
		}
	}
	return DetectNone, len(p)
}
