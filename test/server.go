package test

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/coredns/core/dnsserver"
	"github.com/miekg/dns"

	"github.com/mholt/caddy"
	"github.com/mholt/caddy/caddyfile"
)

func TCPServer(t *testing.T, laddr string) (*dns.Server, string, error) {
	l, err := net.Listen("tcp", laddr)
	if err != nil {
		return nil, "", err
	}

	server := &dns.Server{Listener: l, ReadTimeout: time.Hour, WriteTimeout: time.Hour}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = func() { t.Logf("started TCP server on %s", l.Addr()); waitLock.Unlock() }

	go func() {
		server.ActivateAndServe()
		l.Close()
	}()

	waitLock.Lock()
	return server, l.Addr().String(), nil
}

func UDPServer(t *testing.T, laddr string) (*dns.Server, string, error) {
	pc, err := net.ListenPacket("udp", laddr)
	if err != nil {
		return nil, "", err
	}
	server := &dns.Server{PacketConn: pc, ReadTimeout: time.Hour, WriteTimeout: time.Hour}

	waitLock := sync.Mutex{}
	waitLock.Lock()
	server.NotifyStartedFunc = func() { t.Logf("started UDP server on %s", pc.LocalAddr()); waitLock.Unlock() }

	go func() {
		server.ActivateAndServe()
		pc.Close()
	}()

	waitLock.Lock()
	return server, pc.LocalAddr().String(), nil
}

// CoreDNSServer returns a test server.
// The ports can be retreived with server.LocalAddr(). The testserver itself can be stopped
// with Stop(). It just takes a normal Corefile as input.
func CoreDNSServer(corefile string) ([]caddy.Server, error) {

	serverBlocks, err := caddyfile.Parse("testCorefile", strings.NewReader(corefile), dnsserver.Directives)
	if err != nil {
		return nil, err
	}

	h := dnsserver.TestNewContext()
	serverBlocks, err = h.InspectServerBlocks("testCoreFile", serverBlocks)
	if err != nil {
		return nil, err
	}

	s, err := h.MakeServers()

	return s, err
}

// StartCoreDNSserver starts a server and return the udp and tcp listen addresses.
func StartCoreDNSServer(srv caddy.Server) (tcp, udp string) {
	go func() { l, _ := srv.Listen(); srv.Serve(l) }()
	go func() { p, _ := srv.ListenPacket(); srv.ServePacket(p) }()

	// TODO(miek): proper wait on the thing.
	time.Sleep(1 * time.Second) // I regret nothing!

	t := srv.(*dnsserver.Server).LocalAddr()
	u := srv.(*dnsserver.Server).LocalAddrPacket()

	return t.String(), u.String()
}

// StopCoreDNSSserver stops a server.
func StopCoreDNSServer(srv caddy.Server) {
	srv.(*dnsserver.Server).Stop()
}
