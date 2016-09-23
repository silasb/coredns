package proxy

import (
	"net"
	"time"

	pool "gopkg.in/fatih/pool.v2"
)

func dialTimeout(network, address string, timeout time.Duration) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		return net.DialTimeout(network, address, timeout)
	}
}

func newPool(initial, max int, dialer func() (net.Conn, error)) (pool.Pool, error) {
	return pool.NewChannelPool(initial, max, dialer)
}

// NewUDPPool returns a pool with UDP connections to address. It opens 2 initial connection, with a
// maximum of 10.
func NewUDPPool(address string) pool.Pool {
	p, _ := pool.NewChannelPool(2, 10, dialTimeout("udp", address, defaultTimeout))
	return p
}

// NewTCPPool returns a pool with TCP connections to address. It opens 1 initial connection, with a
// maximum of 5.
func NewTCPPool(address string) pool.Pool {
	p, _ := pool.NewChannelPool(1, 5, dialTimeout("udp", address, defaultTimeout))
	return p
}
