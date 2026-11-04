package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/peer"
	"github.com/Crank-Git/wingman/internal/sig"
)

// blockingReader feeds bytes to a scanner on demand and returns io.EOF once
// done is closed. It lets a test provide a chat input line only after the
// far peer has registered its message handler, avoiding a connect race.
type blockingReader struct {
	ch   chan []byte
	done chan struct{}
	rest []byte
}

func newBlockingReader() *blockingReader {
	return &blockingReader{ch: make(chan []byte, 1), done: make(chan struct{})}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if len(r.rest) == 0 {
		select {
		case data := <-r.ch:
			r.rest = data
		case <-r.done:
			return 0, io.EOF
		}
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

// lockedBuffer is a concurrency-safe io.Writer backed by bytes.Buffer. chat
// writes the "connected" notice from its main goroutine and the "received"
// notice from the peer's message-delivery goroutine, so both writers must be
// serialized for the test to observe the buffer without a data race.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitOut polls buf until it contains want or the deadline elapses.
func waitOut(t *testing.T, b *lockedBuffer, want string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		if strings.Contains(b.String(), want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %q; out=%q", want, b.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestRunExitCodes checks that run returns 0 on success and 1 on error.
func TestRunExitCodes(t *testing.T) {
	ctx := context.Background()
	if code := run(ctx, []string{"help"}); code != 0 {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	if code := run(ctx, []string{"bogus"}); code != 1 {
		t.Fatalf("unknown command exit code = %d, want 1", code)
	}
	if code := run(ctx, nil); code != 1 {
		t.Fatalf("no command exit code = %d, want 1", code)
	}
}

// TestDispatchRouting checks command selection and error handling.
func TestDispatchRouting(t *testing.T) {
	ctx := context.Background()
	if err := dispatch(ctx, []string{"help"}); err != nil {
		t.Fatalf("dispatch help: %v", err)
	}
	if err := dispatch(ctx, []string{"nope"}); err == nil {
		t.Fatal("dispatch bogus: expected error")
	}
	if err := dispatch(ctx, nil); err == nil {
		t.Fatal("dispatch nil: expected error")
	}
}

// TestRunServeBadFlag checks that an unknown flag is reported.
func TestRunServeBadFlag(t *testing.T) {
	ctx := context.Background()
	if err := runServe(ctx, []string{"--nope"}); err == nil {
		t.Fatal("runServe bad flag: expected error")
	}
}

// TestRunServeStartsAndStops checks that serve starts a server and returns when
// ctx is cancelled.
func TestRunServeStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if err := runServe(ctx, nil); err != nil {
		t.Fatalf("runServe: %v", err)
	}
}

// TestStartServeRoundTrip starts a server and proves it serves by publishing
// and polling a fragment through the transport.
func TestStartServeRoundTrip(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()
	if srv.Addr() == "" {
		t.Fatal("serve started with an empty address")
	}

	client := &dns.WriteOverQuery{
		Resolver:     &dns.Dialer{Addr: srv.Addr(), Net: "udp", Timeout: 2 * time.Second},
		Zone:         defaultZone,
		PollInterval: 10 * time.Millisecond,
	}
	msg := []byte("hello, signaling world")
	if err := client.Publish(context.Background(), "room-a", sig.Offer, msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, err := client.Poll(context.Background(), "room-a", sig.Offer)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("round trip mismatch: got %q want %q", got, msg)
	}
}

// TestRunOfferMissingRoom checks that offer without --room is rejected.
func TestRunOfferMissingRoom(t *testing.T) {
	ctx := context.Background()
	err := runOffer(ctx, []string{"--addr", "127.0.0.1:5353"})
	if err == nil {
		t.Fatal("runOffer without --room: expected error")
	}
}

// TestRunOfferBadAddr checks that a connect failure is surfaced.
func TestRunOfferBadAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runOffer(ctx, []string{"--addr", "127.0.0.1:1", "--room", "room"})
	if err == nil {
		t.Fatal("runOffer with unreachable server: expected error")
	}
}

// TestConnectOfferAnswerEndToEnd drives a full offer/answer handshake and a
// data-channel message exchange.
func TestConnectOfferAnswerEndToEnd(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "e2e-room"
	received := make(chan string, 1)
	ready := make(chan struct{})
	go func() {
		ans, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman")
		if err != nil {
			t.Errorf("connectAnswer: %v", err)
			close(ready)
			return
		}
		ans.OnMessage(func(data []byte) { received <- string(data) })
		close(ready)
	}()

	offer, err := connectOffer(ctx, srv.Addr(), defaultZone, room, "wingman")
	if err != nil {
		t.Fatalf("connectOffer: %v", err)
	}
	<-ready // ensure the answer registered its handler before we send

	if err := offer.SendText("hello from offer"); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case got := <-received:
		if got != "hello from offer" {
			t.Fatalf("answer received %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

// TestRunPeerHappyPath drives the full offer/answer path through runPeer and
// chat, sending one line after the answer has registered its handler.
func TestRunPeerHappyPath(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "runpeer-room"
	received := make(chan string, 1)
	ready := make(chan struct{})
	go func() {
		ans, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman")
		if err != nil {
			t.Errorf("connectAnswer: %v", err)
			close(ready)
			return
		}
		ans.OnMessage(func(data []byte) { received <- string(data) })
		close(ready)
	}()

	br := newBlockingReader()
	out := &bytes.Buffer{}
	errCh := make(chan error, 1)
	go func() {
		opts := peerOpts{addr: srv.Addr(), zone: defaultZone, room: room, label: "wingman"}
		errCh <- runPeer(ctx, peer.RoleOfferer, opts, br, out)
	}()

	<-ready // answer registered before we release the input line
	br.ch <- []byte("hello from run\n")
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("message not received before EOF")
	}
	close(br.done) // let the scanner goroutine exit

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runPeer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runPeer did not return")
	}
	if !strings.Contains(out.String(), "offer: connected") {
		t.Fatalf("missing connected notice: %q", out.String())
	}
}

// TestDispatchRoutesCommands checks that dispatch reaches each handler. serve
// is given a cancellable ctx so runServe returns after starting; offer/answer
// are given bad flags so they fail fast in flag parsing.
func TestDispatchRoutesCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := dispatch(ctx, []string{"serve"}); err != nil {
		t.Fatalf("dispatch serve: %v", err)
	}

	if err := dispatch(context.Background(), []string{"offer", "--bogus"}); err == nil {
		t.Fatal("dispatch offer --bogus: expected error")
	}
	if err := dispatch(context.Background(), []string{"answer", "--bogus"}); err == nil {
		t.Fatal("dispatch answer --bogus: expected error")
	}
}

// TestRunPeerAnswerer drives the answerer through runPeer and chat.
func TestRunPeerAnswerer(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "answerer-room"
	offerDone := make(chan struct{})
	go func() {
		defer close(offerDone)
		if _, err := connectOffer(ctx, srv.Addr(), defaultZone, room, "wingman"); err != nil {
			t.Errorf("connectOffer: %v", err)
		}
	}()

	br := newBlockingReader()
	// Let the handshake proceed, then EOF the chat so it returns.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(br.done)
	}()

	out := &bytes.Buffer{}
	opts := peerOpts{addr: srv.Addr(), zone: defaultZone, room: room, label: "wingman"}
	if err := runPeer(ctx, peer.RoleAnswerer, opts, br, out); err != nil {
		t.Fatalf("runPeer answerer: %v", err)
	}
	<-offerDone
	if !strings.Contains(out.String(), "answer: connected") {
		t.Fatalf("missing connected notice: %q", out.String())
	}
}

// TestConnectAnswerCancel checks that a cancelled context surfaces as a connect
// error.
func TestConnectAnswerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := connectAnswer(ctx, "127.0.0.1:1", defaultZone, "room", "wingman")
	if err == nil {
		t.Fatal("connectAnswer with cancelled ctx: expected error")
	}
}

// TestChatSendTextError checks that chat surfaces a SendText failure when the
// data channel is closed.
func TestChatSendTextError(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "chat-senderr-room"
	go func() {
		if _, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman"); err != nil {
			t.Errorf("connectAnswer: %v", err)
		}
	}()

	offer, err := connectOffer(ctx, srv.Addr(), defaultZone, room, "wingman")
	if err != nil {
		t.Fatalf("connectOffer: %v", err)
	}
	// Close the data channel so the next SendText fails.
	if dc, ok := offer.DataChannel(); ok {
		_ = dc.Close()
	}

	out := &bytes.Buffer{}
	errCh := make(chan error, 1)
	go func() { errCh <- chat(ctx, offer, peer.RoleOfferer, strings.NewReader("hello\n"), out) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a SendText error from chat")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not return")
	}
}

// TestRunAnswerHappyPath drives the answerer through runAnswer with stdin
// redirected to an empty file so the chat scanner hits EOF and returns.
func TestRunAnswerHappyPath(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "answer-happy-room"
	offerDone := make(chan struct{})
	go func() {
		defer close(offerDone)
		if _, err := connectOffer(ctx, srv.Addr(), defaultZone, room, "wingman"); err != nil {
			t.Errorf("connectOffer: %v", err)
		}
	}()

	// Redirect stdin to an empty file so the chat scanner hits EOF immediately.
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	f, err := os.CreateTemp("", "wingman-stdin")
	if err != nil {
		t.Fatalf("temp stdin: %v", err)
	}
	defer f.Close()
	os.Stdin = f

	if err := runAnswer(ctx, []string{"--addr", srv.Addr(), "--room", room}); err != nil {
		t.Fatalf("runAnswer: %v", err)
	}
	<-offerDone
}

// TestCLIExec builds the binary and runs it end to end. This exercises main()
// and the run()/dispatch() exit-code paths that cannot be reached otherwise.
func TestCLIExec(t *testing.T) {
	dir, err := os.MkdirTemp("", "wingman-build")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "wingman")

	// Build the binary from the current module.
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build wingman: %v\n%s", err, out)
	}

	// help should exit 0 and print the usage banner.
	helpCmd := exec.Command(bin, "help")
	out, err := helpCmd.Output()
	if err != nil {
		t.Fatalf("run help: %v", err)
	}
	if !bytes.Contains(out, []byte("wingman")) {
		t.Fatalf("help output missing banner: %q", out)
	}

	// an unknown command should exit non-zero.
	if err := exec.Command(bin, "bogus").Run(); err == nil {
		t.Fatal("run bogus: expected non-zero exit")
	}
}

// TestChatContextCancel checks that chat returns nil when ctx is cancelled
// while it is blocked waiting for input.
func TestChatContextCancel(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "chat-cancel-room"
	go func() {
		if _, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman"); err != nil {
			t.Errorf("connectAnswer: %v", err)
		}
	}()

	offer, err := connectOffer(ctx, srv.Addr(), defaultZone, room, "wingman")
	if err != nil {
		t.Fatalf("connectOffer: %v", err)
	}

	br := newBlockingReader()
	defer close(br.done) // unblock the scanner goroutine at test end
	out := &bytes.Buffer{}
	errCh := make(chan error, 1)
	go func() { errCh <- chat(ctx, offer, peer.RoleOfferer, br, out) }()

	time.Sleep(50 * time.Millisecond) // let chat start and block on input
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("chat: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chat did not return on ctx cancel")
	}
}

// TestChatMessageDelivery verifies that a line sent by one peer is delivered to
// the other over the full DNS-signaling + P2P data-channel path: the offerer's
// SendText reaches the answerer's chat OnMessage handler, which prints a
// "received" notice. Both peers are driven through runPeer with blockingReader
// inputs so the offerer cannot send until the answerer's message handler is
// registered (both "connected" notices are observed first), avoiding a connect
// race.
func TestChatMessageDelivery(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "chat-deliver-room"

	ansOut := &lockedBuffer{}
	brAns := newBlockingReader()
	defer close(brAns.done) // unblock the answerer's scanner at test end
	ansDone := make(chan error, 1)
	go func() {
		ansDone <- runPeer(ctx, peer.RoleAnswerer, peerOpts{addr: srv.Addr(), zone: defaultZone, room: room, label: "wingman"}, brAns, ansOut)
	}()

	offerOut := &lockedBuffer{}
	brOffer := newBlockingReader()
	defer close(brOffer.done) // unblock the offerer's scanner at test end
	offerDone := make(chan error, 1)
	go func() {
		offerDone <- runPeer(ctx, peer.RoleOfferer, peerOpts{addr: srv.Addr(), zone: defaultZone, room: room, label: "wingman"}, brOffer, offerOut)
	}()

	// Wait for both peers to finish the handshake and register their OnMessage
	// handlers before the offerer is allowed to send.
	waitOut(t, offerOut, "offer: connected", 5*time.Second)
	waitOut(t, ansOut, "answer: connected", 5*time.Second)

	// Now release one input line to the offerer's scanner; it is sent over the
	// channel and must be received by the answerer's OnMessage handler.
	brOffer.ch <- []byte("hello\n")

	waitOut(t, ansOut, "answer: received \"hello\"", 5*time.Second)
}
