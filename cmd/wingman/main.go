// Command wingman is a local peer CLI for the Wingman WebRTC signaling system.
//
// It provides three subcommands:
//
//	serve   start an in-process DNS signaling server and print its address
//	offer   run the offerer role: create a data channel, then type messages to send
//	answer  run the answerer role: receive the data channel and print incoming messages
//
// For a local single-host demo, start one server and point both peers at it:
//
//	wingman serve --local-demo                 # prints "listening on 127.0.0.1:PORT"
//	wingman offer --addr 127.0.0.1:PORT --room demo
//	wingman answer --addr 127.0.0.1:PORT --room demo
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/https"
	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/peer"
	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/Crank-Git/wingman/internal/turn"
	"github.com/Crank-Git/wingman/internal/turnserver"
)

// defaultZone is the DNS zone peers query when none is supplied.
const defaultZone = "example.com"

// defaultDialTimeout bounds a single DNS exchange for a peer.
const defaultDialTimeout = 5 * time.Second

const defaultHandshakeTimeout = 30 * time.Second

// defaultPollInterval is the delay between poll attempts while a peer waits for
// the other side's SDP.
const defaultPollInterval = 50 * time.Millisecond

// stringSliceFlag is a flag.Value that accumulates repeated --flags into a
// slice, used for the repeatable --turn server list.
type stringSliceFlag []string

// String implements flag.Value; it joins the accumulated values for help output.
func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

// Set implements flag.Value; it appends one value to the slice.
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}

// run executes the selected subcommand and returns a process exit code. It is
// separated from main so command handling can be tested without os.Exit.
func run(ctx context.Context, args []string) int {
	if err := dispatch(ctx, args); err != nil {
		fmt.Fprintln(os.Stderr, "wingman:", err)
		return 1
	}
	return 0
}

// dispatch routes a subcommand to its handler.
func dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return errors.New("no command given")
	}
	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:])
	case "offer":
		return runOffer(ctx, args[1:])
	case "answer":
		return runAnswer(ctx, args[1:])
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q (run 'wingman help')", args[0])
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `wingman - WebRTC signaling over DNS

Usage:
  wingman serve --local-demo         start a local DNS signaling server
  wingman offer  [flags]             run the offerer: create a data channel, then type messages
  wingman answer [flags]             run the answerer: receive the data channel, print incoming messages

offer/answer flags:
  --addr HOST:PORT        DNS server address (required for dns and dynupdate)
  --dns-tsig-key-file PATH  restricted TSIG JSON file for DNS queries
  --room NAME             signaling room shared by both peers (required)
  --zone example.com      authoritative DNS zone (required explicitly for dynupdate; default "example.com" otherwise)
  --label wingman         data channel label (default "wingman")
  --signaling dns|https|dynupdate  signaling transport (default "dns")
  --signaling-url URL     HTTPS signaling base URL (required for --signaling https)
  --signaling-allow-loopback-http  permit loopback HTTP for development and tests
  --signaling-secret S    shared secret for --signaling https (authenticates the channel)
  --signaling-secret-file PATH  restricted token file for HTTPS authentication
  --turn URI              TURN/STUN server URI, repeatable (enables srflx/relay ICE)
  --user NAME             TURN username for --turn servers
  --pass PASS             TURN password for --turn servers
  --pass-file PATH        restricted TURN password file (instead of --pass)
  --metrics               print connection metrics (Prometheus format) on exit
  --handshake-timeout D    positive handshake duration (default "30s")

serve flags:
  --listen 127.0.0.1:0    DNS UDP and TCP listen address (literal IP and port)
  --local-demo            permit unauthenticated loopback DNS
  --dns-tsig-key-file PATH  restricted TSIG JSON file for DNS authentication
  --https-listen ADDR     optional TLS signaling listen address
  --tls-cert PATH         TLS certificate PEM file
  --tls-key PATH          TLS private key PEM file
  --signaling-secret-file PATH  restricted token file for HTTPS authentication
  --zone example.com      authoritative DNS zone this server serves
  --metrics-addr ADDR     also serve Prometheus /metrics on ADDR (e.g. 127.0.0.1:9090)
  --turn-server ADDR      also run a self-hosted TURN/STUN server on ADDR (e.g. 127.0.0.1:3478)
  --turn-user NAME        TURN username for the self-hosted server (enables relay candidates)
  --turn-pass PASS        TURN password for the self-hosted server
  --turn-pass-file PATH   restricted TURN password file (instead of --turn-pass)
  --turn-external ADDR    public IP peers dial the server on (required when binding 0.0.0.0)
`)
}

// runServe starts the in-process DNS signaling server and blocks until ctx is
// cancelled (Ctrl-C). It prints the address peers must dial.
func runServe(ctx context.Context, args []string) (result error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	zone := fs.String("zone", defaultZone, "authoritative DNS zone this server serves")
	listen := fs.String("listen", "127.0.0.1:0", "DNS UDP and TCP listen address")
	localDemo := fs.Bool("local-demo", false, "permit unauthenticated loopback DNS")
	tsigFile := fs.String("dns-tsig-key-file", "", "restricted TSIG JSON file")
	httpsListen := fs.String("https-listen", "", "optional TLS signaling listen address")
	tlsCert := fs.String("tls-cert", "", "TLS certificate PEM file")
	tlsKey := fs.String("tls-key", "", "TLS private key PEM file")
	secretFile := fs.String("signaling-secret-file", "", "restricted HTTPS token file")
	metricsAddr := fs.String("metrics-addr", "", "optional address to serve Prometheus /metrics on, e.g. 127.0.0.1:9090 (disabled by default)")
	turnAddr := fs.String("turn-server", "", "also run a self-hosted TURN/STUN server on ADDR (e.g. 127.0.0.1:3478) for srflx/relay ICE")
	turnUser := fs.String("turn-user", "", "TURN username for the self-hosted server (enables relay candidates)")
	turnPass := fs.String("turn-pass", "", "TURN password for the self-hosted server")
	turnPassFile := fs.String("turn-pass-file", "", "restricted TURN password file")
	turnExternal := fs.String("turn-external", "", "public IP peers dial the server on (required when binding a wildcard interface)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	password, err := loadTURNPassword(*turnPass, *turnPassFile)
	if err != nil {
		return err
	}
	if *zone == "" {
		return errors.New("DNS zone must not be empty")
	}
	key, err := loadOptionalTSIG(*tsigFile)
	if err != nil {
		return err
	}
	if *localDemo {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil {
			return fmt.Errorf("local demo listen address: %w", err)
		}
		if !net.ParseIP(host).IsLoopback() {
			return errors.New("local demo requires a loopback IP listen address")
		}
	} else if key == nil {
		return errors.New("DNS requires --dns-tsig-key-file or explicit --local-demo")
	}
	return serveListeners(ctx, serveConfig{
		zone: *zone, listen: *listen, key: key,
		https:       https.ServerConfig{ListenAddr: *httpsListen, CertFile: *tlsCert, KeyFile: *tlsKey, SecretFile: *secretFile, LocalDemo: *localDemo},
		metricsAddr: *metricsAddr,
		turn:        turnserver.Config{Addr: *turnAddr, Username: *turnUser, Password: password, ExternalAddr: *turnExternal},
	}, os.Stdout)
}

// peerOpts holds the flags shared by the offer and answer subcommands.
type peerOpts struct {
	dnsTSIGKeyFile      string
	addr                string
	zone                string
	room                string
	label               string
	signaling           string
	signalingURL        string
	signalingSecret     string
	signalingSecretFile string
	allowLoopbackHTTP   bool
	turn                []string
	user                string
	pass                string
	metrics             bool
	handshakeTimeout    time.Duration
}

// parsePeerFlags parses and validates the offer/answer flags.
func parsePeerFlags(name string, args []string) (peerOpts, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tsigFile := fs.String("dns-tsig-key-file", "", "restricted TSIG JSON file")
	addr := fs.String("addr", "", "DNS server address, e.g. 127.0.0.1:5353 (required for dns and dynupdate)")
	zone := fs.String("zone", defaultZone, "authoritative DNS zone")
	room := fs.String("room", "", "signaling room shared with the other peer (required)")
	label := fs.String("label", "wingman", "data channel label")
	signaling := fs.String("signaling", "dns", "signaling transport: dns, https, or dynupdate")
	signalingURL := fs.String("signaling-url", "", "HTTPS signaling base URL (required for --signaling https)")
	signalingSecret := fs.String("signaling-secret", "", "shared secret for --signaling https (authenticates the signaling channel)")
	signalingSecretFile := fs.String("signaling-secret-file", "", "restricted HTTPS token file")
	allowLoopbackHTTP := fs.Bool("signaling-allow-loopback-http", false, "permit loopback HTTP for development and tests")
	var turnServers stringSliceFlag
	fs.Var(&turnServers, "turn", "TURN/STUN server URI, repeatable, e.g. --turn turn:stun.example.com:3478")
	user := fs.String("user", "", "TURN username for --turn servers")
	pass := fs.String("pass", "", "TURN password for --turn servers")
	passFile := fs.String("pass-file", "", "restricted TURN password file")
	metricsFlag := fs.Bool("metrics", false, "print connection metrics (Prometheus format) on exit")
	handshakeTimeout := fs.Duration("handshake-timeout", defaultHandshakeTimeout, "positive handshake duration")
	if err := fs.Parse(args); err != nil {
		return peerOpts{}, err
	}
	password, err := loadTURNPassword(*pass, *passFile)
	if err != nil {
		return peerOpts{}, err
	}
	if *room == "" {
		return peerOpts{}, fmt.Errorf("%s: --room is required", name)
	}
	if *handshakeTimeout <= 0 {
		return peerOpts{}, fmt.Errorf("%s: --handshake-timeout must be positive", name)
	}
	if *signaling == "dns" && *addr == "" {
		return peerOpts{}, fmt.Errorf("%s: --addr is required for --signaling dns", name)
	}
	if *signaling == "https" && strings.TrimSpace(*signalingURL) == "" {
		return peerOpts{}, fmt.Errorf("%s: --signaling-url is required for --signaling https", name)
	}
	if *signaling == "dynupdate" {
		zoneSet := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "zone" {
				zoneSet = true
			}
		})
		if strings.TrimSpace(*addr) == "" || !zoneSet || strings.TrimSpace(*zone) == "" || strings.TrimSpace(*tsigFile) == "" {
			return peerOpts{}, fmt.Errorf("%s: dynupdate requires --addr, explicit --zone, and --dns-tsig-key-file", name)
		}
	}
	return peerOpts{
		dnsTSIGKeyFile: *tsigFile, addr: *addr, zone: *zone, room: *room, label: *label,
		signaling: *signaling, signalingURL: *signalingURL, signalingSecret: *signalingSecret,
		signalingSecretFile: *signalingSecretFile,
		allowLoopbackHTTP:   *allowLoopbackHTTP,
		turn:                turnServers, user: *user, pass: password, metrics: *metricsFlag,
		handshakeTimeout: *handshakeTimeout,
	}, nil
}

// runPeer builds the signaling transport and ICE servers from opts, connects a
// session for the given role (recording metrics), and runs the send/receive
// loop. It prints the metrics snapshot when opts.metrics is set.
func runPeer(ctx context.Context, role peer.Role, opts peerOpts, in io.Reader, out io.Writer) (err error) {
	transport, err := buildTransport(opts)
	if err != nil {
		return err
	}
	iceServers, err := buildICEServers(opts)
	if err != nil {
		return err
	}
	m := metrics.New()
	timeout := opts.handshakeTimeout
	if timeout == 0 {
		timeout = defaultHandshakeTimeout
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, timeout)
	sess, err := startSession(handshakeCtx, role, peer.Config{
		Room:       opts.room,
		Zone:       opts.zone,
		Transport:  transport,
		ICEServers: iceServers,
		Label:      opts.label,
	}, m)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		err = closePeerSession(sess.Close, err)
	}()
	err = chat(ctx, sess, role, in, out)
	if opts.metrics {
		fmt.Fprint(out, m.Text())
	}
	return err
}

func closePeerSession(closeSession func() error, result error) error {
	closeErr := closeSession()
	if result != nil {
		return result
	}
	if closeErr != nil {
		return fmt.Errorf("close session: %w", closeErr)
	}
	return nil
}

// startSession creates a session for role with cfg, runs the handshake, and
// records the outcome in m. On success it records the handshake latency; on
// failure it marks the attempt failed and records a failure.
func startSession(ctx context.Context, role peer.Role, cfg peer.Config, m *metrics.Metrics) (*peer.Session, error) {
	m.ConnectionAttempted()
	start := time.Now()
	sess := peer.NewSession(role, cfg).WithMetrics(m)
	if _, err := sess.Start(ctx); err != nil {
		m.ConnectionFailed()
		m.Failure()
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			cause := ctx.Err()
			// Socket deadlines can expire before the context timer callback runs.
			if deadline, ok := ctx.Deadline(); cause == nil && ok && !time.Now().Before(deadline) {
				cause = context.DeadlineExceeded
			}
			if cause != nil {
				err = errors.Join(err, cause)
			}
		}
		return nil, err
	}
	m.ConnectionSucceeded()
	m.RecordHandshakeLatency(time.Since(start))
	return sess, nil
}

// buildTransport returns the selected transport after credential validation.
func buildTransport(opts peerOpts) (sig.Transport, error) {
	switch opts.signaling {
	case "dynupdate":
		key, err := loadOptionalTSIG(opts.dnsTSIGKeyFile)
		if err != nil {
			return nil, err
		}
		return dns.NewDynUpdate(opts.addr, opts.zone, key)
	case "", "dns":
		key, err := loadOptionalTSIG(opts.dnsTSIGKeyFile)
		if err != nil {
			return nil, err
		}
		tr := newTransport(opts.addr, opts.zone)
		tr.Resolver.(*dns.Dialer).TSIGKey = key
		return tr, nil
	case "https":
		secret, err := https.LoadSecret(opts.signalingSecret, opts.signalingSecretFile)
		if err != nil {
			return nil, err
		}
		tr, err := newHTTPSTransport(opts.signalingURL, secret)
		if err != nil {
			return nil, err
		}
		tr.AllowLoopbackHTTP = opts.allowLoopbackHTTP
		return tr, nil
	default:
		return nil, fmt.Errorf("unknown signaling %q (want dns, https, or dynupdate)", opts.signaling)
	}
}

// newHTTPSTransport builds an HTTPS transport from the given base URL.
func newHTTPSTransport(baseURL, secret string) (*https.Transport, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("https: --signaling-url is required for --signaling https")
	}
	return &https.Transport{BaseURL: strings.TrimRight(baseURL, "/"), Secret: secret}, nil
}

// buildICEServers converts the parsed --turn servers into a pion ICE server
// slice. With no --turn servers it returns a nil slice (host candidates only).
func buildICEServers(opts peerOpts) ([]webrtc.ICEServer, error) {
	if len(opts.turn) == 0 {
		return nil, nil
	}
	servers, err := turn.ParseServers(opts.turn, opts.user, opts.pass)
	if err != nil {
		return nil, err
	}
	cfg := turn.Config{Servers: servers}
	return cfg.ICEServers(), nil
}

// runOffer runs the offerer subcommand.
func runOffer(ctx context.Context, args []string) error {
	opts, err := parsePeerFlags("offer", args)
	if err != nil {
		return err
	}
	return runPeer(ctx, peer.RoleOfferer, opts, os.Stdin, os.Stdout)
}

// runAnswer runs the answerer subcommand.
func runAnswer(ctx context.Context, args []string) error {
	opts, err := parsePeerFlags("answer", args)
	if err != nil {
		return err
	}
	return runPeer(ctx, peer.RoleAnswerer, opts, os.Stdin, os.Stdout)
}

// startServe starts an in-process DNS signaling server and returns it. Callers
// stop it with srv.Stop().
func startServe(keys ...*dns.TSIGKey) (*dns.Server, error) {
	srv := dns.NewServer()
	if len(keys) > 0 {
		srv.TSIGKey = keys[0]
	}
	if err := srv.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}
	return srv, nil
}

// startTurnServer starts a self-hosted TURN/STUN server and prints the URIs
// peers should use. It returns a nil server (with a nil error) when addr is
// empty, so callers can treat "no TURN server" and "success" uniformly.
func startTurnServer(out io.Writer, cfg turnserver.Config) (*turnserver.Server, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, nil
	}
	s, err := turnserver.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("turn-server: %w", err)
	}
	fmt.Fprintf(out, "turn-server listening on %s\n", s.Addr())
	fmt.Fprintf(out, "  srflx: --turn %s\n", s.STUNURI())
	if uri := s.TURNURI(); uri != "" {
		fmt.Fprintf(out, "  relay: --turn %s --user <USERNAME> --pass-file <PATH>\n", uri)
	} else {
		fmt.Fprintln(out, "  relay: set --turn-user and --turn-pass-file to enable relay candidates")
	}
	return s, nil
}

// newTransport builds the DNS write-over-query transport pointed at srvAddr.
func newTransport(srvAddr, zone string) *dns.WriteOverQuery {
	return &dns.WriteOverQuery{
		Resolver:     &dns.Dialer{Addr: srvAddr, Net: "udp", Timeout: defaultDialTimeout},
		Zone:         zone,
		PollInterval: defaultPollInterval,
	}
}

// connectOffer completes the offerer handshake over DNS and returns the session.
// It is the DNS-only path used by tests; runPeer builds the full path via
// startSession.
func connectOffer(ctx context.Context, addr, zone, room, label string) (*peer.Session, error) {
	return startSession(ctx, peer.RoleOfferer, peer.Config{
		Room:      room,
		Zone:      zone,
		Transport: newTransport(addr, zone),
		Label:     label,
	}, metrics.New())
}

// connectAnswer completes the answerer handshake over DNS and returns the
// session. See connectOffer.
func connectAnswer(ctx context.Context, addr, zone, room, label string) (*peer.Session, error) {
	return startSession(ctx, peer.RoleAnswerer, peer.Config{
		Room:      room,
		Zone:      zone,
		Transport: newTransport(addr, zone),
		Label:     label,
	}, metrics.New())
}

// chat runs a simple send/receive loop over the session's data channel. It
// prints a connected notice, sends each line read from in, and prints messages
// received over the channel until ctx is cancelled or in is exhausted.
func chat(ctx context.Context, sess *peer.Session, role peer.Role, in io.Reader, out io.Writer) error {
	sess.OnMessage(func(data []byte) {
		fmt.Fprintf(out, "%s: received %q\n", role, string(data))
	})
	fmt.Fprintf(out, "%s: connected\n", role)

	readerDone := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			if err := ctx.Err(); err != nil {
				readerDone <- err
				return
			}
			if err := sess.SendText(sc.Text()); err != nil {
				readerDone <- err
				return
			}
		}
		readerDone <- sc.Err()
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-readerDone:
		return err
	}
}

func loadOptionalTSIG(path string) (*dns.TSIGKey, error) {
	if path == "" {
		return nil, nil
	}
	return dns.LoadTSIGKey(path)
}
