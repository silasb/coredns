package proxy

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"github.com/miekg/coredns/middleware/pkg/singleflight"
	"github.com/miekg/coredns/request"

	"github.com/miekg/dns"
)

type Client struct {
	Timeout time.Duration

	group *singleflight.Group
}

func NewClient() *Client {
	return &Client{Timeout: defaultTimeout, group: new(singleflight.Group)}
}

// ServeDNS does not satisfy middleware.Handler, instead it interacts with the upstream
// and returns the respons or an error.
func (c *Client) ServeDNS(w dns.ResponseWriter, r *dns.Msg, u *UpstreamHost) (*dns.Msg, error) {
	var (
		err error
		co  net.Conn
	)

	if request.Proto(w) == "tcp" {
		co, _ = u.TCPPool.Get()
		// err ??? dial ourselves?
	} else {
		co, _ = u.UDPPool.Get()
		// err ??? dial ourselves?
	}

	reply, _, err := c.Exchange(r, co)

	if reply != nil && reply.Truncated {
		// Suppress proxy error for truncated responses
		err = nil
	}

	if err != nil {
		return nil, err
	}

	reply.Compress = true
	reply.Id = r.Id
	return reply, err
}

func (c *Client) Exchange(m *dns.Msg, co net.Conn) (*dns.Msg, time.Duration, error) {
	t := "nop"
	if t1, ok := dns.TypeToString[m.Question[0].Qtype]; ok {
		t = t1
	}
	cl := "nop"
	if cl1, ok := dns.ClassToString[m.Question[0].Qclass]; ok {
		cl = cl1
	}

	start := time.Now()

	// Name needs to be normalized! Bug in go dns.
	r, err := c.group.Do(m.Question[0].Name+t+cl, func() (interface{}, error) {
		ret, e := c.exchange(m, co, dns.MinMsgSize)
		return ret, e
	})

	rtt := time.Since(start)

	return r.(*dns.Msg), rtt, err
}

func (c *Client) exchange(m *dns.Msg, co net.Conn, udpsize uint16) (r *dns.Msg, err error) {
	opt := m.IsEdns0()

	// If EDNS0 is used use that for size.
	if opt != nil && opt.UDPSize() >= dns.MinMsgSize {
		udpsize = opt.UDPSize()
	}

	co.SetWriteDeadline(deadline(c.Timeout))
	if err = WriteMsg(co, m); err != nil {
		return nil, err
	}

	co.SetReadDeadline(deadline(c.Timeout))
	r, err = ReadMsg(co, udpsize)
	if err == nil && r.Id != m.Id {
		err = dns.ErrId
	}

	return r, err
}

// ReadMsg reads a message from the connection co.
func ReadMsg(co net.Conn, udpsize uint16) (*dns.Msg, error) {
	p, err := readMsgHeader(co, nil, udpsize)
	if err != nil {
		return nil, err
	}

	m := new(dns.Msg)
	if err := m.Unpack(p); err != nil {
		// If ErrTruncated was returned, we still want to allow the user to use
		// the message, but naively they can just check err if they don't want
		// to use a truncated message
		if err == dns.ErrTruncated {
			return m, err
		}
		return nil, err
	}
	return m, err
}

// readMsgHeader reads a DNS message, parses and populates hdr (when hdr is not nil).
// Returns message as a byte slice to be parsed with Msg.Unpack later on.
// Note that error handling on the message body is not possible as only the header is parsed.
func readMsgHeader(co net.Conn, hdr *dns.Header, udpsize uint16) ([]byte, error) {
	var (
		p   []byte
		n   int
		err error
	)

	switch t := co.(type) {
	case *net.TCPConn, *tls.Conn:
		r := t.(io.Reader)

		// First two bytes specify the length of the entire message.
		l, err := tcpMsgLen(r)
		if err != nil {
			return nil, err
		}
		p = make([]byte, l)
		n, err = tcpRead(r, p)
	default:
		if udpsize > dns.MinMsgSize {
			p = make([]byte, udpsize)
		} else {
			p = make([]byte, dns.MinMsgSize)
		}
		n, err = Read(co, p)
	}

	if err != nil {
		return nil, err
	} else if n < 12 { // 12 == dns.headerSize
		return nil, dns.ErrShortRead
	}

	p = p[:n]
	if hdr != nil {
		dh, _, err := unpackMsgHdr(p, 0)
		if err != nil {
			return nil, err
		}
		*hdr = dh
	}
	return p, err
}

// tcpMsgLen is a helper func to read first two bytes of stream as uint16 packet length.
func tcpMsgLen(t io.Reader) (int, error) {
	p := []byte{0, 0}
	n, err := t.Read(p)
	if err != nil {
		return 0, err
	}
	if n != 2 {
		return 0, dns.ErrShortRead
	}
	l := binary.BigEndian.Uint16(p)
	if l == 0 {
		return 0, dns.ErrShortRead
	}
	return int(l), nil
}

// tcpRead calls TCPConn.Read enough times to fill allocated buffer.
func tcpRead(t io.Reader, p []byte) (int, error) {
	n, err := t.Read(p)
	if err != nil {
		return n, err
	}
	for n < len(p) {
		j, err := t.Read(p[n:])
		if err != nil {
			return n, err
		}
		n += j
	}
	return n, err
}

// Read implements the net.Conn read method.
func Read(co net.Conn, p []byte) (n int, err error) {
	if co == nil {
		return 0, dns.ErrConnEmpty
	}
	if len(p) < 2 {
		return 0, io.ErrShortBuffer
	}
	switch t := co.(type) {
	case *net.TCPConn, *tls.Conn:
		r := t.(io.Reader)

		l, err := tcpMsgLen(r)
		if err != nil {
			return 0, err
		}
		if l > len(p) {
			return int(l), io.ErrShortBuffer
		}
		return tcpRead(r, p[:l])
	}
	// UDP connection
	n, err = Read(co, p)
	if err != nil {
		return n, err
	}
	return n, err
}

// WriteMsg sends a message through the connection co.
func WriteMsg(co net.Conn, m *dns.Msg) (err error) {
	out, err := m.Pack()
	if err != nil {
		return err
	}
	if _, err = co.Write(out); err != nil {
		return err
	}
	return nil
}

func Write(co net.Conn, p []byte) (n int, err error) {
	switch t := co.(type) {
	case *net.TCPConn, *tls.Conn:
		w := t.(io.Writer)

		lp := len(p)
		if lp < 2 {
			return 0, io.ErrShortBuffer
		}
		if lp > dns.MaxMsgSize {
			return 0, errors.New("message too large")
		}
		l := make([]byte, 2, lp+2)
		binary.BigEndian.PutUint16(l, uint16(lp))
		p = append(l, p...)
		n, err := io.Copy(w, bytes.NewReader(p))
		return int(n), err
	}
	n, err = co.(*net.UDPConn).Write(p)
	return n, err
}

func deadline(timeout time.Duration) time.Time { return time.Now().Add(timeout) }
