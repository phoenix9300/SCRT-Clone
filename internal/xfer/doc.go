// Package xfer implements the classic in-band serial file transfer protocols
// XMODEM, YMODEM and ZMODEM for a terminal emulator.
//
// The protocols are implemented from the public specifications (Chuck
// Forsberg's XMODEM/YMODEM and ZMODEM documents). A transfer runs over a
// [Stream], an 8-bit clean byte path to the remote peer that the host
// application diverts terminal traffic into for the duration of the
// transfer. Typical remote peers are the lrzsz tools (rz, sz, rb, sb, rx,
// sx) or the XMODEM loader of a router or switch bootloader.
//
// # Starting a transfer
//
// ZMODEM sessions are usually started by the remote side: the user types
// "sz file" or "rz" and the remote program announces itself with a hex
// header. A [Detector] fed with terminal output recognises that
// announcement; the host then prints everything before the header, hands
// the remaining bytes to the stream and calls [ZmodemReceive] or
// [ZmodemSend]. Both functions cope with the announcement header being the
// first thing they read, and with it being absent or truncated.
//
// XMODEM and YMODEM have no announcement; the user starts them explicitly.
//
// # After a transfer
//
// Every transfer function consumes its own protocol bytes and nothing else
// as far as that is practical. Bytes that were read from the stream but
// turned out to belong to the terminal (for example a shell prompt that
// directly followed the last protocol frame) are handed back through
// [Options.Leftover] or, if that is nil and the stream implements
// [Unreader], through Unread.
//
// All functions honour context cancellation: they send the cancel sequence
// to the peer (see [Abort]) and return ctx.Err(). No goroutine outlives the
// call.
package xfer
