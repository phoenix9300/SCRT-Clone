package xfer

import "hash/crc32"

// crc16Table is the lookup table for the CRC-16 used by XMODEM-CRC, YMODEM
// and ZMODEM (CCITT polynomial 0x1021, initial value 0, no reflection).
var crc16Table = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// crc16Update folds p into a running CRC-16/XMODEM value.
func crc16Update(crc uint16, p []byte) uint16 {
	for _, b := range p {
		crc = crc<<8 ^ crc16Table[byte(crc>>8)^b]
	}
	return crc
}

// crc16 returns the CRC-16/XMODEM of p.
func crc16(p []byte) uint16 { return crc16Update(0, p) }

// crc32Frame returns the ZMODEM 32-bit frame check of a data subpacket: the
// IEEE CRC-32 of the payload followed by the frame end byte. The value is
// transmitted least significant byte first.
func crc32Frame(data []byte, frameEnd byte) uint32 {
	end := [1]byte{frameEnd}
	return crc32.Update(crc32.ChecksumIEEE(data), crc32.IEEETable, end[:])
}

// crc16Frame is the 16-bit counterpart of crc32Frame. It is transmitted most
// significant byte first.
func crc16Frame(data []byte, frameEnd byte) uint16 {
	end := [1]byte{frameEnd}
	return crc16Update(crc16Update(0, data), end[:])
}

// checksum8 is the arithmetic checksum of original XMODEM.
func checksum8(p []byte) byte {
	var s byte
	for _, b := range p {
		s += b
	}
	return s
}
