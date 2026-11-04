package dns

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

// Server is an in-process authoritative server implementing write-over-query
// semantics. Fragment-write queries (names that parse as a rendezvous fragment)
// are stored; poll queries (the room poll name) return the stored fragments as
// TXT records. It is safe for concurrent use.
type Server struct {
	TSIGKey       *TSIGKey   // Set before Start or requests.
	mu            sync.Mutex // mu guards entries, fragments, and byte counts.
	frags         map[storageKey]*fragmentEntry
	retainedBytes int
	limits        storageLimits // Set limits before Start or requests.
	now           func() time.Time
	newTicker     func(time.Duration) (<-chan time.Time, func()) // Set clock hooks before Start or requests.
	listenPacket  func(string, string) (net.PacketConn, error)   // Set bind hooks before Start.
	listenTCP     func(string, string) (net.Listener, error)

	lifecycleMu sync.Mutex // lifecycleMu guards listeners, address, and lifecycle channels.
	dns         *dns.Server
	tcp         *dns.Server
	addr        string
	stop        chan struct{}
	done        chan struct{}
}

const fragmentLifetime = 120 * time.Second

const listenerPairAttempts = 10

type fragmentEntry struct {
	createdAt time.Time
	fragments map[uint16]sig.Fragment
	bytes     int
}

type storageKey struct {
	zone string
	room string
	kind sig.Kind
}

// NewServer returns an empty Server.
func NewServer() *Server {
	return &Server{
		frags:        make(map[storageKey]*fragmentEntry),
		limits:       defaultStorageLimits(),
		now:          time.Now,
		listenPacket: net.ListenPacket,
		listenTCP:    net.Listen,
		newTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
	}
}

// Addr returns the listener address (valid after Start).
func (s *Server) Addr() string {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.addr
}

// Start serves DNS on UDP and TCP listeners at the same loopback address.
// Context cancellation stops the listeners and the one-second expiry sweep.
// Entries expire 120 seconds after their first fragment arrives.
// Start tries at most ten UDP ports if TCP reports an address collision.
// Call Stop before another Start. Start returns listener setup errors.
func (s *Server) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stop != nil {
		return fmt.Errorf("start DNS server: call Stop before another Start")
	}
	if s.TSIGKey != nil {
		if err := s.TSIGKey.validate(); err != nil {
			return err
		}
	}
	pc, tcpLn, err := s.listenPair()
	if err != nil {
		return err
	}
	s.addr = pc.LocalAddr().String()
	s.dns = &dns.Server{PacketConn: pc, Handler: s.makeHandler(edns0UDPSize), TsigSecret: s.TSIGKey.secrets()}
	s.tcp = &dns.Server{Listener: tcpLn, Handler: s.makeHandler(dns.MaxMsgSize), TsigSecret: s.TSIGKey.secrets()}

	if err := activateListener(s.dns); err != nil {
		_ = pc.Close()
		_ = tcpLn.Close()
		return fmt.Errorf("activate DNS UDP: %w", err)
	}
	if err := activateListener(s.tcp); err != nil {
		_ = s.dns.Shutdown()
		_ = tcpLn.Close()
		return fmt.Errorf("activate DNS TCP: %w", err)
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	ticks, stopTicker := s.newTicker(time.Second)
	go s.run(ctx, ticks, stopTicker)
	return nil
}

func (s *Server) listenPair() (net.PacketConn, net.Listener, error) {
	for attempt := 1; ; attempt++ {
		pc, err := s.listenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, fmt.Errorf("listen DNS UDP: %w", err)
		}
		ln, err := s.listenTCP("tcp", pc.LocalAddr().String())
		if err == nil {
			return pc, ln, nil
		}
		// UDP can select a port that TCP already uses.
		if closeErr := pc.Close(); closeErr != nil {
			return nil, nil, errors.Join(fmt.Errorf("listen DNS TCP: %w", err), fmt.Errorf("close DNS UDP: %w", closeErr))
		}
		if !errors.Is(err, syscall.EADDRINUSE) || attempt == listenerPairAttempts {
			return nil, nil, fmt.Errorf("listen DNS TCP: %w", err)
		}
	}
}

func activateListener(server *dns.Server) error {
	// Shutdown requires ActivateAndServe to finish its listener setup.
	ready := make(chan struct{})
	result := make(chan error, 1)
	server.NotifyStartedFunc = func() { close(ready) }
	go func() { result <- server.ActivateAndServe() }()
	select {
	case <-ready:
		return nil
	case err := <-result:
		return err
	}
}

func (s *Server) run(ctx context.Context, ticks <-chan time.Time, stopTicker func()) {
	defer close(s.done)
	defer func() { _ = s.tcp.Shutdown() }()
	defer func() { _ = s.dns.Shutdown() }()
	defer stopTicker()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticks:
			s.expire(s.now())
		}
	}
}

func (s *Server) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.frags {
		s.expireKey(key, now)
	}
}

// expireKey requires mu because deletion also releases the retained bytes.
func (s *Server) expireKey(key storageKey, now time.Time) {
	if entry := s.frags[key]; entry != nil && !now.Before(entry.createdAt.Add(fragmentLifetime)) {
		s.retainedBytes -= entry.bytes
		delete(s.frags, key)
	}
}

// makeHandler returns a dns.HandlerFunc that truncates responses to the given
// size. UDP responses truncate to edns0UDPSize (setting the TC bit so the
// client falls back to TCP); TCP responses truncate to MaxMsgSize (no effective
// truncation, since TCP has no size limit).
func (s *Server) makeHandler(truncateSize int) dns.HandlerFunc {
	return func(w dns.ResponseWriter, req *dns.Msg) {
		responseSize := truncateSize
		if s.TSIGKey != nil {
			t := req.IsTsig()
			if t == nil || t.Hdr.Name != s.TSIGKey.Name || t.Algorithm != s.TSIGKey.Algorithm || w.TsigStatus() != nil {
				// A failed rejection write needs no retry because storage remains inaccessible.
				_ = w.WriteMsg(setRcode(req, dns.RcodeNotAuth))
				return
			}
			w = &signedWriter{ResponseWriter: w, key: s.TSIGKey}
			// Reserve the signature before truncation because Truncate skips signed messages.
			responseSize -= 128 + len(s.TSIGKey.Name)
		}
		s.handle(w, req, responseSize)
	}
}

// handle routes an incoming query to fragment-store or poll logic. truncateSize
// is the maximum response size for this transport (0 disables truncation).
func (s *Server) handle(w dns.ResponseWriter, req *dns.Msg, truncateSize int) {
	if len(req.Question) != 1 || req.Question[0].Qtype != dns.TypeTXT || req.Question[0].Qclass != dns.ClassINET {
		_ = w.WriteMsg(setRcode(req, dns.RcodeFormatError))
		return
	}
	name := strings.ToLower(strings.TrimSuffix(req.Question[0].Name, "."))
	if !validName(name) {
		_ = w.WriteMsg(setRcode(req, dns.RcodeFormatError))
		return
	}

	if parsed, err := sig.ParseRendezvousName(name); err == nil {
		s.storeFragment(w, req, parsed)
		return
	}
	s.respondPoll(w, req, name, truncateSize)
}

func validName(name string) bool {
	// The root terminator and the first label length add two wire octets.
	if len(name)+2 > 255 || strings.Contains(name, "\\") || strings.TrimSpace(name) != name {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
	}
	return true
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
	key := storageKey{zone: parsed.Zone, room: parsed.Room, kind: parsed.Kind}

	s.mu.Lock()
	now := s.now()
	for existing := range s.frags {
		s.expireKey(existing, now)
	}
	entry := s.frags[key]
	if code := s.admitFragment(key, entry, frag); code != dns.RcodeSuccess {
		s.mu.Unlock()
		_ = w.WriteMsg(setRcode(req, code))
		return
	}
	if entry == nil {
		entry = &fragmentEntry{createdAt: now, fragments: make(map[uint16]sig.Fragment)}
		s.frags[key] = entry
	}
	if _, exists := entry.fragments[parsed.Seq]; !exists {
		entry.bytes += len(payload)
		s.retainedBytes += len(payload)
		entry.fragments[parsed.Seq] = frag
	}
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

// admitFragment requires mu so the quota check and retention stay atomic.
func (s *Server) admitFragment(key storageKey, entry *fragmentEntry, frag sig.Fragment) int {
	if entry == nil {
		if len(s.frags) >= s.limits.keys {
			return dns.RcodeServerFailure
		}
	} else {
		for seq, stored := range entry.fragments {
			if stored.Total != frag.Total || (seq == frag.Seq && !bytes.Equal(stored.Data, frag.Data)) {
				return dns.RcodeFormatError
			}
		}
		if _, exists := entry.fragments[frag.Seq]; exists {
			return dns.RcodeSuccess
		}
	}
	if len(frag.Data) > fragmentDataBytes || int(frag.Total) > fragmentLimit(s.limits) || !fitsResponse(key, frag.Total, s.limits) {
		return dns.RcodeServerFailure
	}
	storedBytes := 0
	if entry != nil {
		storedBytes = entry.bytes
	}
	if storedBytes+len(frag.Data) > s.limits.messageBytes+messageCRCBytes || s.retainedBytes+len(frag.Data) > s.limits.retainedBytes {
		return dns.RcodeServerFailure
	}
	return dns.RcodeSuccess
}

// respondPoll returns the stored fragments for a room as one TXT record each,
// or NXDOMAIN when the room is empty so the caller retries.
func (s *Server) respondPoll(w dns.ResponseWriter, req *dns.Msg, name string, truncateSize int) {
	fields := strings.Split(name, ".")
	// poll name: room.kind.sig.<zone...>
	if len(fields) < 4 || fields[2] != sig.SigLabel {
		_ = w.WriteMsg(setRcode(req, dns.RcodeFormatError))
		return
	}
	room := fields[0]
	kind, err := sig.ParseKind(fields[1])
	if err != nil {
		_ = w.WriteMsg(setRcode(req, dns.RcodeFormatError))
		return
	}
	zone := strings.Join(fields[3:], ".")
	key := storageKey{zone: zone, room: room, kind: kind}

	s.mu.Lock()
	s.expireKey(key, s.now())
	var frags []sig.Fragment
	if entry := s.frags[key]; entry != nil {
		frags = make([]sig.Fragment, 0, len(entry.fragments))
		for _, f := range entry.fragments {
			frags = append(frags, f)
		}
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

// Stop waits for the listeners and the expiry sweep to stop.
// Repeated calls are safe. Call Stop before another Start.
func (s *Server) Stop() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stop != nil {
		close(s.stop)
		<-s.done
		s.stop = nil
	}
}

type signedWriter struct {
	dns.ResponseWriter
	key *TSIGKey
}

func (w *signedWriter) WriteMsg(m *dns.Msg) error {
	m.SetTsig(w.key.Name, w.key.Algorithm, 300, time.Now().Unix())
	return w.ResponseWriter.WriteMsg(m)
}
