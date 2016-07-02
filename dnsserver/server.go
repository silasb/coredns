package dnsserver

import (
	"fmt"
	"log"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/miekg/coredns/middleware"
	"github.com/miekg/coredns/middleware/chaos"
	"github.com/miekg/coredns/middleware/metrics"

	"github.com/miekg/dns"
	"golang.org/x/net/context"
)

// Server represents an instance of a server, which serves
// DNS requests at a particular address (host and port). A
// server is capable of serving numerous zones on
// the same address and the listener may be stopped for
// graceful termination (POSIX only).
type Server struct {
	Addr   string // Address we listen on
	mux    *dns.ServeMux
	server [2]*dns.Server // 0 is a net.Listener, 1 is a net.PacketConn (a *UDPConn) in our case.

	l net.Listener
	p net.PacketConn
	m sync.Mutex // protects listener and packetconn

	zones       map[string]zone // zones keyed by their address
	dnsWg       sync.WaitGroup  // used to wait on outstanding connections
	connTimeout time.Duration   // the maximum duration of a graceful shutdown
}

const (
	tcp = 0
	udp = 1
)

// Do not re-use a server (start, stop, then start again). We
// could probably add more locking to make this possible, but
// as it stands, you should dispose of a server after stopping it.
// The behavior of serving with a spent server is undefined.
func New(addr string, configs []Config, gracefulTimeout time.Duration) (*Server, error) {

	s := &Server{
		Addr:        addr,
		zones:       make(map[string]zone),
		connTimeout: gracefulTimeout,
	}
	mux := dns.NewServeMux()
	mux.Handle(".", s) // wildcard handler, everything will go through here
	s.mux = mux

	// We have to bound our wg with one increment
	// to prevent a "race condition" that is hard-coded
	// into sync.WaitGroup.Wait() - basically, an add
	// with a positive delta must be guaranteed to
	// occur before Wait() is called on the wg.
	// In a way, this kind of acts as a safety barrier.
	s.dnsWg.Add(1)

	// Set up each zone
	for _, conf := range configs {
		if _, exists := s.zones[conf.Host]; exists {
			return nil, fmt.Errorf("cannot serve %s - host already defined for address %s", conf.Address(), s.Addr)
		}

		z := zone{config: conf}

		// Build middleware stack
		z.buildStack()
		s.zones[conf.Host] = z

		// A bit of a hack. Loop through the middlewares of this zone and check if
		// they have enabled the chaos middleware. If so add the special chaos zones.
	Middleware:
		for _, mid := range z.config.Middleware {
			fn := mid(nil)
			if _, ok := fn.(chaos.Chaos); ok {
				for _, ch := range []string{"authors.bind.", "version.bind.", "version.server.", "hostname.bind.", "id.server."} {
					s.zones[ch] = z
				}
				break Middleware
			}
		}
	}

	return s, nil
}

// LocalAddr return the addresses where the server is bound to.
func (s *Server) LocalAddr() net.Addr {
	s.m.Lock()
	defer s.m.Unlock()
	return s.tcp.Addr()
}

// LocalAddrPacket return the net.PacketConn address where the server is bound to.
func (s *Server) LocalAddrPacket() net.Addr {
	s.m.Lock()
	defer s.m.Lock()
	return s.udp.LocalAddr()
}

// Serve starts the server with an existing listener. It blocks until the server stops.
func (s *Server) Serve(l net.Listener) error {
	s.m.Lock()
	s.server[tcp] = &dns.Server{Listener: l, Net: "tcp", Handler: s.mux}
	s.m.Unlock()

	return s.server[tcp].ActivateAndServe()
}

// ServePacket starts the server with an existing packetconn. It blocks until the server stops.
func (s *Server) ServePacket(p net.PacketConn) error {
	if err != nil {
		close(s.startChan) // MUST defer so error is properly reported, same with all cases in this file
		return err
	}
	s.m.Lock()
	s.server[udp] = &dns.Server{PacketConn: p, Net: "udp", Handler: s.mux}
	s.m.Unlock()

	return s.server[udp].ActivateAndServe()
}

func (s *Server) Listen() (net.Listener, error) {
	l, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return nil, err
	}
	s.listenerMu.Lock()
	s.tcp = l
	s.listenerMu.Unlock()
	return l, nil
}

func (s *Server) ListenPacket() (net.PacketConn, error) {
	p, err := net.ListenPacket("udp", s.Addr)
	if err != nil {
		return nil, err
	}

	s.listenerMu.Lock()
	s.udp = p
	s.listenerMu.Unlock()
	return p, nil
}

// Stop stops the server. It blocks until the server is
// totally stopped. On POSIX systems, it will wait for
// connections to close (up to a max timeout of a few
// seconds); on Windows it will close the listener
// immediately.
func (s *Server) Stop() (err error) {

	if runtime.GOOS != "windows" {
		// force connections to close after timeout
		done := make(chan struct{})
		go func() {
			s.dnsWg.Done() // decrement our initial increment used as a barrier
			s.dnsWg.Wait()
			close(done)
		}()

		// Wait for remaining connections to finish or
		// force them all to close after timeout
		select {
		case <-time.After(s.connTimeout):
		case <-done:
		}
	}

	// Close the listener now; this stops the server without delay
	s.listenerMu.Lock()
	if s.tcp != nil {
		err = s.tcp.Close()
	}
	if s.udp != nil {
		err = s.udp.Close()
	}

	for _, s1 := range s.server {
		err = s1.Shutdown()
	}
	s.listenerMu.Unlock()
	return
}

// ServeDNS is the entry point for every request to the address that s
// is bound to. It acts as a multiplexer for the requests zonename as
// defined in the request so that the correct zone
// (configuration and middleware stack) will handle the request.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	defer func() {
		// In case the user doesn't enable error middleware, we still
		// need to make sure that we stay alive up here
		if rec := recover(); rec != nil {
			DefaultErrorFunc(w, r, dns.RcodeServerFailure)
		}
	}()

	if m, err := middleware.Edns0Version(r); err != nil { // Wrong EDNS version, return at once.
		rc := middleware.RcodeToString(dns.RcodeBadVers)
		state := middleware.State{W: w, Req: r}

		metrics.Report(state, metrics.Dropped, rc, m.Len(), time.Now())
		w.WriteMsg(m)
		return
	}

	q := r.Question[0].Name
	b := make([]byte, len(q))
	off, end := 0, false
	ctx := context.Background()

	for {
		l := len(q[off:])
		for i := 0; i < l; i++ {
			b[i] = q[off+i]
			// normalize the name for the lookup
			if b[i] >= 'A' && b[i] <= 'Z' {
				b[i] |= ('a' - 'A')
			}
		}

		if h, ok := s.zones[string(b[:l])]; ok {
			if r.Question[0].Qtype != dns.TypeDS {
				rcode, _ := h.stack.ServeDNS(ctx, w, r)
				if RcodeNoClientWrite(rcode) {
					DefaultErrorFunc(w, r, rcode)
				}
				return
			}
		}
		off, end = dns.NextLabel(q, off)
		if end {
			break
		}
	}
	// Wildcard match, if we have found nothing try the root zone as a last resort.
	if h, ok := s.zones["."]; ok {
		rcode, _ := h.stack.ServeDNS(ctx, w, r)
		if RcodeNoClientWrite(rcode) {
			DefaultErrorFunc(w, r, rcode)
		}
		return
	}

	// Still here? Error out with REFUSED and some logging
	remoteHost := w.RemoteAddr().String()
	DefaultErrorFunc(w, r, dns.RcodeRefused)
	log.Printf("[INFO] \"%s %s %s\" - No such zone at %s (Remote: %s)", dns.Type(r.Question[0].Qtype), dns.Class(r.Question[0].Qclass), q, s.Addr, remoteHost)
}

// DefaultErrorFunc responds to an DNS request with an error.
func DefaultErrorFunc(w dns.ResponseWriter, r *dns.Msg, rcode int) {
	state := middleware.State{W: w, Req: r}
	rc := middleware.RcodeToString(rcode)

	answer := new(dns.Msg)
	answer.SetRcode(r, rcode)
	state.SizeAndDo(answer)

	metrics.Report(state, metrics.Dropped, rc, answer.Len(), time.Now())
	w.WriteMsg(answer)
}

func RcodeNoClientWrite(rcode int) bool {
	switch rcode {
	case dns.RcodeServerFailure:
		fallthrough
	case dns.RcodeRefused:
		fallthrough
	case dns.RcodeFormatError:
		fallthrough
	case dns.RcodeNotImplemented:
		return true
	}
	return false
}
