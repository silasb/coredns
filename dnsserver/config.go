package dnsserver

import (
	"net"

	"github.com/miekg/coredns/middleware"
)

// Config configuration for a single server.
type Config struct {
	// The address of the site.
	Addr string

	// The hostname to bind listener to;
	// defaults to Addr.Host.
	ListenHost string

	// The port to listen on.
	Port string

	// The directory from which to parse db files.
	Root string

	// Middleware stack.
	Middleware []middleware.Middleware

	// Compiled middleware stack.
	middlewareChain Handler
}

// Address returns the host:port of c as a string.
func (c Config) Address() string {
	return net.JoinHostPort(c.Addr, c.Port)
}
