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
	"syscall"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/peer"
	"github.com/Crank-Git/wingman/internal/sig"
)

// defaultZone is the DNS zone peers query when none is supplied.
const defaultZone = "example.com"

// defaultDialTimeout bounds a single DNS exchange for a peer.
const defaultDialTimeout = 5 * time.Second

// defaultPollInterval is the delay between poll attempts while a peer waits for
// the other side's SDP.
const defaultPollInterval = 50 * time.Millisecond

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
  --addr 127.0.0.1:PORT   DNS server address printed by 'serve' (required)
  --room NAME             signaling room shared by both peers (required)
  --zone example.com      authoritative DNS zone (default "example.com")
  --label wingman         data channel label (default "wingman")
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
	addr  string
	zone  string
	room  string
	label string
}

// parsePeerFlags parses and validates the offer/answer flags.
func parsePeerFlags(name string, args []string) (peerOpts, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", "", "DNS server address, e.g. 127.0.0.1:5353 (required)")
	zone := fs.String("zone", defaultZone, "authoritative DNS zone")
	room := fs.String("room", "", "signaling room shared with the other peer (required)")
	label := fs.String("label", "wingman", "data channel label")
	if err := fs.Parse(args); err != nil {
		return peerOpts{}, err
	}
	if *addr == "" || *room == "" {
		return peerOpts{}, fmt.Errorf("%s: --addr and --room are required", name)
	}
	return peerOpts{addr: *addr, zone: *zone, room: *room, label: *label}, nil
}

// runPeer connects a session for the given role and runs the send/receive loop.
func runPeer(ctx context.Context, role peer.Role, opts peerOpts, in io.Reader, out io.Writer) error {
	var (
		sess *peer.Session
		err  error
	)
	if role == peer.RoleOfferer {
		sess, err = connectOffer(ctx, opts.addr, opts.zone, opts.room, opts.label)
	} else {
		sess, err = connectAnswer(ctx, opts.addr, opts.zone, opts.room, opts.label)
	}
	if err != nil {
		return err
	}
	return chat(ctx, sess, role, in, out)
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

// connectOffer completes the offerer handshake and returns the session.
func connectOffer(ctx context.Context, srvAddr, zone, room, label string) (*peer.Session, error) {
	sess := peer.NewSession(peer.RoleOfferer, peer.Config{
		Room:      room,
		Zone:      zone,
		Transport: newTransport(srvAddr, zone),
		Label:     label,
	})
	if _, err := sess.Start(ctx); err != nil {
		return nil, err
	}
	return sess, nil
}

// connectAnswer completes the answerer handshake and returns the session.
func connectAnswer(ctx context.Context, srvAddr, zone, room, label string) (*peer.Session, error) {
	sess := peer.NewSession(peer.RoleAnswerer, peer.Config{
		Room:      room,
		Zone:      zone,
		Transport: newTransport(srvAddr, zone),
		Label:     label,
	})
	if _, err := sess.Start(ctx); err != nil {
		return nil, err
	}
	return sess, nil
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
