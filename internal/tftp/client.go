package tftp

import (
	"context"
	"io"
	"net"
	"strconv"
	"time"
)

// Options tunes a client transfer. A nil *Options selects the defaults.
type Options struct {
	BlockSize int           // blksize option to request; 0 = plain 512-byte blocks
	TSize     int64         // Put: file size to announce with the tsize option; 0 = do not
	Timeout   time.Duration // retransmission timeout, default 5s
	Retries   int           // retransmissions before giving up, default 5

	// wrap lets tests interpose on the client's socket.
	wrap func(net.PacketConn) net.PacketConn
}

// dial opens the client socket and arranges for ctx cancellation to
// interrupt pending reads.
func dial(ctx context.Context, addr string, o *Options) (*conn, net.Addr, func(), error) {
	if o == nil {
		o = &Options{}
	}
	server, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, nil, nil, err
	}
	var pc net.PacketConn
	if pc, err = net.ListenUDP("udp", nil); err != nil {
		return nil, nil, nil, err
	}
	if o.wrap != nil {
		pc = o.wrap(pc)
	}
	stop := context.AfterFunc(ctx, func() { pc.Close() })
	c := newConn(pc, nil, o.Timeout, o.Retries)
	c.host = server
	return c, server, func() { stop(); pc.Close() }, nil
}

func (o *Options) requestOptions(tsize string) []option {
	var opts []option
	if o != nil && o.BlockSize != 0 {
		opts = append(opts, option{"blksize", strconv.Itoa(o.BlockSize)})
	}
	if tsize != "" {
		opts = append(opts, option{"tsize", tsize})
	}
	return opts
}

// applyOACK adopts the options the server acknowledged.
func (c *conn) applyOACK(oack []byte, requested int) error {
	for _, o := range parseOACK(oack) {
		if o.name != "blksize" {
			continue
		}
		v, ok := atoiRange(o.value, minBlockSize, maxBlockSize)
		if !ok || requested == 0 || v > requested {
			c.fail(CodeOptionRejected, "bad blksize")
			return &Error{CodeOptionRejected, "server acknowledged an invalid blksize"}
		}
		c.blksize = v
	}
	return nil
}

func ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil && err != nil {
		return cerr
	}
	return err
}

// Get downloads the file name from the TFTP server at addr ("host:port")
// into w and returns the number of bytes received.
func Get(ctx context.Context, addr, name string, w io.Writer, o *Options) (int64, error) {
	c, server, closeConn, err := dial(ctx, addr, o)
	if err != nil {
		return 0, err
	}
	defer closeConn()
	blk := 0
	if o != nil {
		blk = o.BlockSize
	}
	req := buildRequest(opRRQ, name, o.requestOptions("0"))

	// The first answer comes from the server's transfer port: DATA 1 if it
	// ignored the options, OACK otherwise.
	for try := 0; ; try++ {
		if try > c.retries {
			return 0, ctxErr(ctx, ErrTimeout)
		}
		if _, err := c.pc.WriteTo(req, server); err != nil {
			return 0, ctxErr(ctx, err)
		}
		op, p, err := c.read(time.Now().Add(c.timeout))
		if isTimeout(err) {
			continue
		}
		if err != nil {
			return 0, ctxErr(ctx, err)
		}
		switch op {
		case opERROR:
			return 0, parseError(p)
		case opOACK:
			if err := c.applyOACK(p, blk); err != nil {
				return 0, err
			}
			ack := ackPacket(0)
			if _, err := c.pc.WriteTo(ack, c.peer); err != nil {
				return 0, ctxErr(ctx, err)
			}
			n, err := c.receive(w, ack, c.peer, nil)
			return n, ctxErr(ctx, err)
		case opDATA:
			n, err := c.receive(w, req, server, append([]byte(nil), p...))
			return n, ctxErr(ctx, err)
		}
		c.peer = nil // not a valid first answer; keep listening
	}
}

// Put uploads r to the TFTP server at addr ("host:port") as name and
// returns the number of bytes sent.
func Put(ctx context.Context, addr, name string, r io.Reader, o *Options) (int64, error) {
	c, server, closeConn, err := dial(ctx, addr, o)
	if err != nil {
		return 0, err
	}
	defer closeConn()
	blk, tsize := 0, ""
	if o != nil {
		blk = o.BlockSize
		if o.TSize > 0 {
			tsize = strconv.FormatInt(o.TSize, 10)
		}
	}
	oack, err := c.exchange(buildRequest(opWRQ, name, o.requestOptions(tsize)), server, 0)
	if err != nil {
		return 0, ctxErr(ctx, err)
	}
	if oack != nil {
		if err := c.applyOACK(oack, blk); err != nil {
			return 0, err
		}
	}
	n, err := c.send(r)
	return n, ctxErr(ctx, err)
}
