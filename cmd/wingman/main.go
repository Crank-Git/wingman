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
//	wingman serve                              # prints "listening on 127.0.0.1:PORT"
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
)

// defaultZone is the DNS zone peers query when none is supplied.
const defaultZone = "example.com"

// defaultDialTimeout bounds a single DNS exchange for a peer.
const defaultDialTimeout = 5 * time.Second

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
  wingman serve                      start a local DNS signaling server
  wingman offer  [flags]             run the offerer: create a data channel, then type messages
  wingman answer [flags]             run the answerer: receive the data channel, print incoming messages

offer/answer flags:
  --addr 127.0.0.1:PORT   DNS server address printed by 'serve' (required for --signaling dns)
  --room NAME             signaling room shared by both peers (required)
  --zone example.com      authoritative DNS zone (default "example.com")
  --label wingman         data channel label (default "wingman")
  --signaling dns|https   signaling transport (default "dns")
  --signaling-url URL     HTTPS signaling base URL (required for --signaling https)
  --turn URI              TURN/STUN server URI, repeatable (enables srflx/relay ICE)
  --user NAME             TURN username for --turn servers
  --pass PASS             TURN password for --turn servers
  --metrics               print connection metrics (Prometheus format) on exit
`)
}

// runServe starts the in-process DNS signaling server and blocks until ctx is
// cancelled (Ctrl-C). It prints the address peers must dial.
func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	zone := fs.String("zone", defaultZone, "authoritative DNS zone this server serves")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv, err := startServe()
	if err != nil {
		return err
	}
	defer srv.Stop()
	fmt.Printf("wingman server listening on %s (zone %q)\n", srv.Addr(), *zone)
	<-ctx.Done()
	return nil
}

// peerOpts holds the flags shared by the offer and answer subcommands.
type peerOpts struct {
	addr         string
	zone         string
	room         string
	label        string
	signaling    string
	signalingURL string
	turn         []string
	user         string
	pass         string
	metrics      bool
}

// parsePeerFlags parses and validates the offer/answer flags.
func parsePeerFlags(name string, args []string) (peerOpts, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", "", "DNS server address, e.g. 127.0.0.1:5353 (required for --signaling dns)")
	zone := fs.String("zone", defaultZone, "authoritative DNS zone")
	room := fs.String("room", "", "signaling room shared with the other peer (required)")
	label := fs.String("label", "wingman", "data channel label")
	signaling := fs.String("signaling", "dns", "signaling transport: dns or https")
	signalingURL := fs.String("signaling-url", "", "HTTPS signaling base URL (required for --signaling https)")
	var turnServers stringSliceFlag
	fs.Var(&turnServers, "turn", "TURN/STUN server URI, repeatable, e.g. --turn turn:stun.example.com:3478")
	user := fs.String("user", "", "TURN username for --turn servers")
	pass := fs.String("pass", "", "TURN password for --turn servers")
	metricsFlag := fs.Bool("metrics", false, "print connection metrics (Prometheus format) on exit")
	if err := fs.Parse(args); err != nil {
		return peerOpts{}, err
	}
	if *room == "" {
		return peerOpts{}, fmt.Errorf("%s: --room is required", name)
	}
	if *signaling == "dns" && *addr == "" {
		return peerOpts{}, fmt.Errorf("%s: --addr is required for --signaling dns", name)
	}
	if *signaling == "https" && strings.TrimSpace(*signalingURL) == "" {
		return peerOpts{}, fmt.Errorf("%s: --signaling-url is required for --signaling https", name)
	}
	return peerOpts{
		addr: *addr, zone: *zone, room: *room, label: *label,
		signaling: *signaling, signalingURL: *signalingURL,
		turn: turnServers, user: *user, pass: *pass, metrics: *metricsFlag,
	}, nil
}

// runPeer builds the signaling transport and ICE servers from opts, connects a
// session for the given role (recording metrics), and runs the send/receive
// loop. It prints the metrics snapshot when opts.metrics is set.
func runPeer(ctx context.Context, role peer.Role, opts peerOpts, in io.Reader, out io.Writer) error {
	transport, err := buildTransport(opts)
	if err != nil {
		return err
	}
	iceServers, err := buildICEServers(opts)
	if err != nil {
		return err
	}
	m := metrics.New()
	sess, err := startSession(ctx, role, peer.Config{
		Room:       opts.room,
		Zone:       opts.zone,
		Transport:  transport,
		ICEServers: iceServers,
		Label:      opts.label,
	}, m)
	if err != nil {
		return err
	}
	err = chat(ctx, sess, role, in, out)
	if opts.metrics {
		fmt.Fprint(out, m.Text())
	}
	return err
}

// startSession creates a session for role with cfg, runs the handshake, and
// records the outcome in m. On success it records the handshake latency; on
// failure it marks the attempt failed and records a failure.
func startSession(ctx context.Context, role peer.Role, cfg peer.Config, m *metrics.Metrics) (*peer.Session, error) {
	m.ConnectionAttempted()
	start := time.Now()
	sess := peer.NewSession(role, cfg)
	if _, err := sess.Start(ctx); err != nil {
		m.ConnectionFailed()
		m.Failure()
		return nil, err
	}
	m.ConnectionSucceeded()
	m.RecordHandshakeLatency(time.Since(start))
	return sess, nil
}

// buildTransport selects the signaling transport from opts. DNS is the default;
// https requires a --signaling-url.
func buildTransport(opts peerOpts) (sig.Transport, error) {
	switch opts.signaling {
	case "", "dns":
		return newTransport(opts.addr, opts.zone), nil
	case "https":
		return newHTTPSTransport(opts.signalingURL)
	default:
		return nil, fmt.Errorf("unknown signaling %q (want dns or https)", opts.signaling)
	}
}

// newHTTPSTransport builds an HTTPS transport from the given base URL.
func newHTTPSTransport(baseURL string) (sig.Transport, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("https: --signaling-url is required for --signaling https")
	}
	return &https.Transport{BaseURL: strings.TrimRight(baseURL, "/")}, nil
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
func startServe() (*dns.Server, error) {
	srv := dns.NewServer()
	if err := srv.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}
	return srv, nil
}

// newTransport builds the DNS write-over-query transport pointed at srvAddr.
func newTransport(srvAddr, zone string) sig.Transport {
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
