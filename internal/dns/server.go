package dns

import (
	"context"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

// Server is an in-process authoritative server implementing write-over-query
// semantics. Fragment-write queries (names that parse as a rendezvous fragment)
// are stored; poll queries (the room poll name) return the stored fragments as
// TXT records. It is safe for concurrent use.
type Server struct {
	mu    sync.Mutex
	frags map[string]map[uint16]sig.Fragment // key: room + "/" + kind
	dns   *dns.Server // UDP
	tcp   *dns.Server // TCP
	addr  string
}

// NewServer returns an empty Server.
func NewServer() *Server {
	return &Server{frags: make(map[string]map[uint16]sig.Fragment)}
}

// Addr returns the listener address (valid after Start).
func (s *Server) Addr() string { return s.addr }

// Start binds UDP and TCP listeners on 127.0.0.1:0 (same port) and begins
// serving. The listeners are shut down when ctx is cancelled. UDP is used for
// normal queries; TCP is the fallback when a UDP response is truncated (TC bit),
// which lets large fragment sets ride over TCP rather than being cut off by the
// UDP datagram limit.
func (s *Server) Start(ctx context.Context) error {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := pc.LocalAddr().(*net.UDPAddr)
	tcpLn, err := net.Listen("tcp", addr.String())
	if err != nil {
		_ = pc.Close()
		return err
	}
	s.addr = addr.String()
	s.dns = &dns.Server{PacketConn: pc, Handler: s.makeHandler(edns0UDPSize)}
	s.tcp = &dns.Server{Listener: tcpLn, Handler: s.makeHandler(dns.MaxMsgSize)}

	go func() {
		<-ctx.Done()
		_ = s.dns.Shutdown()
		_ = s.tcp.Shutdown()
	}()
	go func() { _ = s.dns.ActivateAndServe() }()
	go func() { _ = s.tcp.ActivateAndServe() }()
	return nil
}

// makeHandler returns a dns.HandlerFunc that truncates responses to the given
// size. UDP responses truncate to edns0UDPSize (setting the TC bit so the
// client falls back to TCP); TCP responses truncate to MaxMsgSize (no effective
// truncation, since TCP has no size limit).
func (s *Server) makeHandler(truncateSize int) dns.HandlerFunc {
	return func(w dns.ResponseWriter, req *dns.Msg) {
		s.handle(w, req, truncateSize)
	}
}

// handle routes an incoming query to fragment-store or poll logic. truncateSize
// is the maximum response size for this transport (0 disables truncation).
func (s *Server) handle(w dns.ResponseWriter, req *dns.Msg, truncateSize int) {
	if len(req.Question) == 0 {
		_ = w.WriteMsg(setRcode(req, dns.RcodeFormatError))
		return
	}
	name := strings.TrimSuffix(req.Question[0].Name, ".")

	if parsed, err := sig.ParseRendezvousName(name); err == nil {
		s.storeFragment(w, req, parsed)
		return
	}
	s.respondPoll(w, req, name, truncateSize)
}

// storeFragment stores a fragment received via a write query and acks it.
func (s *Server) storeFragment(w dns.ResponseWriter, req *dns.Msg, parsed sig.ParsedName) {
	payload, err := sig.DecodeLabel(parsed.Payload)
	if err != nil {
		// Malformed payload: reject with format error.
		_ = w.WriteMsg(setRcode(req, dns.RcodeFormatError))
		return
	}

	frag := sig.Fragment{Seq: parsed.Seq, Total: parsed.Total, Data: payload}
	key := parsed.Room + "/" + parsed.Kind.String()

	s.mu.Lock()
	if s.frags[key] == nil {
		s.frags[key] = make(map[uint16]sig.Fragment)
	}
	s.frags[key][parsed.Seq] = frag
	s.mu.Unlock()

	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.SetEdns0(edns0UDPSize, false)
	resp.Answer = append(resp.Answer, &dns.TXT{
		Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 1},
		Txt: []string{"ok"},
	})
	_ = w.WriteMsg(resp)
}

// respondPoll returns the stored fragments for a room as one TXT record each,
// or NXDOMAIN when the room is empty so the caller retries.
func (s *Server) respondPoll(w dns.ResponseWriter, req *dns.Msg, name string, truncateSize int) {
	fields := strings.Split(name, ".")
	// poll name: room.kind.sig.<zone...>
	if len(fields) < 4 || fields[2] != sig.SigLabel {
		_ = w.WriteMsg(setRcode(req, dns.RcodeNameError))
		return
	}
	room := fields[0]
	kind, err := sig.ParseKind(fields[1])
	if err != nil {
		_ = w.WriteMsg(setRcode(req, dns.RcodeNameError))
		return
	}
	zone := strings.Join(fields[3:], ".")
	key := room + "/" + kind.String()

	s.mu.Lock()
	stored := s.frags[key]
	frags := make([]sig.Fragment, 0, len(stored))
	for _, f := range stored {
		frags = append(frags, f)
	}
	s.mu.Unlock()
	// Sort by seq so that, if the response is truncated to fit the transport,
	// only the highest sequence numbers are dropped (a clean end-gap) rather
	// than a random middle fragment from the map's iteration order.
	sort.SliceStable(frags, func(i, j int) bool { return frags[i].Seq < frags[j].Seq })

	resp := new(dns.Msg)
	resp.SetReply(req)
	resp.SetEdns0(edns0UDPSize, false)

	if len(frags) == 0 {
		_ = w.WriteMsg(setRcode(req, dns.RcodeNameError))
		return
	}

	pollName := req.Question[0].Name
	for _, f := range frags {
		payload, err := sig.EncodeLabel(f.Data)
		if err != nil {
			continue
		}
		fragName := sig.RendezvousName(zone, room, kind, f.Seq, f.Total, payload)
		resp.Answer = append(resp.Answer, &dns.TXT{
			Hdr: dns.RR_Header{Name: pollName, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 1},
			Txt: []string{fragName},
		})
	}
	// Trim to the transport's size budget. Over UDP this sets the TC bit so the
	// client retries over TCP; over TCP truncateSize is MaxMsgSize and is a no-op.
	if truncateSize > 0 {
		resp.Truncate(truncateSize)
	}
	_ = w.WriteMsg(resp)
}

// setRcode returns a new message with the given rcode copied from req.
func setRcode(req *dns.Msg, rcode int) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetRcode(req, rcode)
	resp.SetEdns0(edns0UDPSize, false)
	return resp
}

// Stop shuts the server down.
func (s *Server) Stop() {
	if s.dns != nil {
		_ = s.dns.Shutdown()
	}
	if s.tcp != nil {
		_ = s.tcp.Shutdown()
	}
}
