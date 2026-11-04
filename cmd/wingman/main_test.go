package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/https"
	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/peer"
	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/Crank-Git/wingman/internal/turnserver"
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

// TestStartTurnServerDisabled confirms that with no --turn-server the helper is
// a no-op: nil server, nil error, no output.
func TestStartTurnServerDisabled(t *testing.T) {
	var buf strings.Builder
	s, err := startTurnServer(&buf, turnserver.Config{})
	if err != nil {
		t.Fatalf("startTurnServer (disabled): %v", err)
	}
	if s != nil {
		t.Fatal("startTurnServer (disabled): got server, want nil")
	}
	if buf.Len() != 0 {
		t.Fatalf("startTurnServer (disabled): wrote %q", buf.String())
	}
}

// TestStartTurnServerSTUNOnly confirms a credentialless server advertises only
// a STUN URI.
func TestStartTurnServerSTUNOnly(t *testing.T) {
	var buf strings.Builder
	s, err := startTurnServer(&buf, turnserver.Config{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("startTurnServer: %v", err)
	}
	defer s.Close()
	if s.TURNURI() != "" {
		t.Errorf("TURNURI() without creds = %q, want empty", s.TURNURI())
	}
	if !strings.Contains(buf.String(), s.STUNURI()) {
		t.Errorf("output missing STUN URI %q:\n%s", s.STUNURI(), buf.String())
	}
}

// TestStartTurnServerWithCreds confirms a server with credentials advertises a
// TURN URI and prints the peer hint with the credentials.
func TestStartTurnServerWithCreds(t *testing.T) {
	var buf strings.Builder
	s, err := startTurnServer(&buf, turnserver.Config{Addr: "127.0.0.1:0", Username: "alice", Password: "s3cret"})
	if err != nil {
		t.Fatalf("startTurnServer: %v", err)
	}
	defer s.Close()
	if s.TURNURI() == "" {
		t.Fatal("TURNURI() with creds = empty, want a turn: URI")
	}
	if !strings.Contains(buf.String(), s.TURNURI()) {
		t.Errorf("output missing TURN URI %q:\n%s", s.TURNURI(), buf.String())
	}
	if !strings.Contains(buf.String(), "--user alice --pass s3cret") {
		t.Errorf("relay hint missing credentials:\n%s", buf.String())
	}
}

// TestStartTurnServerRejectsWildcard confirms binding a wildcard interface
// without an explicit external address is rejected.
func TestStartTurnServerRejectsWildcard(t *testing.T) {
	var buf strings.Builder
	if _, err := startTurnServer(&buf, turnserver.Config{Addr: "0.0.0.0:0"}); err == nil {
		t.Fatal("startTurnServer wildcard without external: expected error")
	}
}

// TestRunServeTurnServer runs the serve subcommand with a self-hosted TURN
// server and confirms it starts and stops cleanly on ctx cancellation.
func TestRunServeTurnServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	if err := runServe(ctx, []string{"--turn-server", "127.0.0.1:0", "--turn-user", "u", "--turn-pass", "p"}); err != nil {
		t.Fatalf("runServe with --turn-server: %v", err)
	}
}

// TestRunServeServesMetrics starts the serve subcommand with --metrics-addr and
// proves the /metrics endpoint responds with Prometheus output before the
// command shuts down on ctx cancellation.
func TestRunServeServesMetrics(t *testing.T) {
	// Grab a free port, then reuse it for the metrics server.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() { done <- runServe(ctx, []string{"--metrics-addr", addr}) }()

	url := "http://" + addr + "/metrics"
	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("metrics endpoint never came up")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "wingman_connections_attempted") {
		t.Errorf("metrics body missing expected metric\n%s", body)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServe did not return after ctx cancel")
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
	remoteClosed := make(chan struct{})
	go func() {
		ans, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman")
		if err != nil {
			t.Errorf("connectAnswer: %v", err)
			close(ready)
			return
		}
		ans.OnMessage(func(data []byte) { received <- string(data) })
		t.Cleanup(func() { _ = ans.Close() })
		dc, _ := ans.DataChannel()
		dc.OnClose(func() { close(remoteClosed) })
		close(ready)
	}()

	br := newBlockingReader()
	out := &bytes.Buffer{}
	errCh := make(chan error, 1)
	go func() {
		opts := peerOpts{addr: srv.Addr(), zone: defaultZone, room: room, label: "wingman", handshakeTimeout: 2 * time.Second}
		errCh <- runPeer(ctx, peer.RoleOfferer, opts, br, out)
	}()

	<-ready // answer registered before we release the input line
	// The completed handshake deadline must not close the live channel.
	time.Sleep(2100 * time.Millisecond)
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
	select {
	case <-remoteClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("runPeer leaves the remote channel open after EOF")
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
	var workers sync.WaitGroup
	defer func() {
		// Cancellation releases setup and chat before cleanup waits for them.
		cancel()
		workers.Wait()
	}()

	room := "chat-senderr-room"
	answerDone := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		_, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman")
		answerDone <- err
	}()

	offer, err := connectOffer(ctx, srv.Addr(), defaultZone, room, "wingman")
	if err != nil {
		t.Fatalf("connectOffer: %v", err)
	}
	// Both peers must finish setup before channel closure can test SendText.
	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("connectAnswer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connectAnswer did not return")
	}
	dc, ok := offer.DataChannel()
	if !ok {
		t.Fatal("offer has no data channel")
	}
	if err := dc.Close(); err != nil {
		t.Fatalf("close data channel: %v", err)
	}

	out := &bytes.Buffer{}
	errCh := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		errCh <- chat(ctx, offer, peer.RoleOfferer, strings.NewReader("hello\n"), out)
	}()

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
	srv, err := startServe()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "answer", "--addr", srv.Addr(), "--room", "cli-timeout", "--handshake-timeout", "50ms")
	out, err = cmd.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("context deadline exceeded")) {
		t.Fatalf("stalled handshake: err=%v output=%s", err, out)
	}
}

func TestParsePeerFlagsHandshakeTimeout(t *testing.T) {
	for _, role := range []string{"offer", "answer"} {
		for _, tc := range []struct {
			value string
			want  time.Duration
		}{
			{"", 30 * time.Second},
			{"90s", 90 * time.Second},
			{"1m30s", 90 * time.Second},
		} {
			args := []string{"--addr", "127.0.0.1:5353", "--room", "r"}
			if tc.value != "" {
				args = append(args, "--handshake-timeout", tc.value)
			}
			opts, err := parsePeerFlags(role, args)
			if err != nil || opts.handshakeTimeout != tc.want {
				t.Fatalf("%s duration %q: opts=%+v err=%v", role, tc.value, opts, err)
			}
		}
		for _, value := range []string{"0", "-1s", "invalid", "999999999999999999999s"} {
			_, err := parsePeerFlags(role, []string{"--addr", "127.0.0.1:5353", "--room", "r", "--handshake-timeout", value})
			if err == nil {
				t.Fatalf("%s accepts duration %q", role, value)
			}
		}
	}
}

func TestRunPeerHandshakePreservesContextCause(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	for _, role := range []peer.Role{peer.RoleOfferer, peer.RoleAnswerer} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		opts := peerOpts{addr: srv.Addr(), zone: defaultZone, room: role.String(), handshakeTimeout: 50 * time.Millisecond}
		start := time.Now()
		err := runPeer(ctx, role, opts, strings.NewReader(""), io.Discard)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("%s timeout: err=%v elapsed=%v", role, err, time.Since(start))
		}
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
		err = runPeer(ctx, role, opts, strings.NewReader(""), io.Discard)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation: %v", role, err)
		}
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

// TestBuildTransport checks signaling transport selection for each --signaling
// value, including the https requirement for a --signaling-url.
func TestBuildTransport(t *testing.T) {
	t.Run("dns default", func(t *testing.T) {
		tr, err := buildTransport(peerOpts{addr: "127.0.0.1:5353", zone: defaultZone, room: "r"})
		if err != nil {
			t.Fatalf("buildTransport dns: %v", err)
		}
		if _, ok := tr.(*dns.WriteOverQuery); !ok {
			t.Fatalf("expected *dns.WriteOverQuery, got %T", tr)
		}
	})

	t.Run("https", func(t *testing.T) {
		tr, err := buildTransport(peerOpts{signaling: "https", signalingURL: "https://sig.example.com"})
		if err != nil {
			t.Fatalf("buildTransport https: %v", err)
		}
		htr, ok := tr.(*https.Transport)
		if !ok {
			t.Fatalf("expected *https.Transport, got %T", tr)
		}
		if htr.BaseURL != "https://sig.example.com" {
			t.Errorf("BaseURL = %q, want https://sig.example.com", htr.BaseURL)
		}
	})

	t.Run("https trims trailing slash", func(t *testing.T) {
		tr, err := buildTransport(peerOpts{signaling: "https", signalingURL: "https://sig.example.com/"})
		if err != nil {
			t.Fatalf("buildTransport https: %v", err)
		}
		if tr.(*https.Transport).BaseURL != "https://sig.example.com" {
			t.Errorf("BaseURL = %q, want https://sig.example.com", tr.(*https.Transport).BaseURL)
		}
	})

	t.Run("https without url", func(t *testing.T) {
		if _, err := buildTransport(peerOpts{signaling: "https"}); err == nil {
			t.Fatal("expected error for https without --signaling-url")
		}
	})

	t.Run("https with secret", func(t *testing.T) {
		tr, err := buildTransport(peerOpts{
			signaling: "https", signalingURL: "https://sig.example.com",
			signalingSecret: "s3cr3t",
		})
		if err != nil {
			t.Fatalf("buildTransport https: %v", err)
		}
		htr, ok := tr.(*https.Transport)
		if !ok {
			t.Fatalf("expected *https.Transport, got %T", tr)
		}
		if htr.Secret != "s3cr3t" {
			t.Errorf("Secret = %q, want s3cr3t", htr.Secret)
		}
	})

	t.Run("unknown signaling", func(t *testing.T) {
		if _, err := buildTransport(peerOpts{signaling: "smtp"}); err == nil {
			t.Fatal("expected error for unknown signaling")
		}
	})
}

// TestBuildICEServers checks that --turn servers become pion ICE servers and
// that invalid URIs are rejected.
func TestBuildICEServers(t *testing.T) {
	t.Run("no turn returns nil", func(t *testing.T) {
		ice, err := buildICEServers(peerOpts{})
		if err != nil {
			t.Fatalf("buildICEServers: %v", err)
		}
		if ice != nil {
			t.Errorf("expected nil ICE servers without --turn, got %v", ice)
		}
	})

	t.Run("valid turn server", func(t *testing.T) {
		ice, err := buildICEServers(peerOpts{turn: []string{"turn:stun.example.com:3478"}, user: "bob", pass: "secret"})
		if err != nil {
			t.Fatalf("buildICEServers: %v", err)
		}
		if len(ice) != 1 {
			t.Fatalf("expected 1 ICE server, got %d", len(ice))
		}
		if ice[0].URLs[0] != "turn:stun.example.com:3478" {
			t.Errorf("URLs[0] = %q, want turn:stun.example.com:3478", ice[0].URLs[0])
		}
		if ice[0].Username != "bob" {
			t.Errorf("Username = %q, want bob", ice[0].Username)
		}
		if ice[0].Credential != "secret" {
			t.Errorf("Credential = %q, want secret", ice[0].Credential)
		}
	})

	t.Run("multiple turn servers", func(t *testing.T) {
		ice, err := buildICEServers(peerOpts{turn: []string{"turn:a.example.com:3478", "stun:b.example.com:3478"}, user: "u", pass: "p"})
		if err != nil {
			t.Fatalf("buildICEServers: %v", err)
		}
		if len(ice) != 2 {
			t.Errorf("expected 2 ICE servers, got %d", len(ice))
		}
	})

	t.Run("invalid uri", func(t *testing.T) {
		if _, err := buildICEServers(peerOpts{turn: []string{"not a uri"}}); err == nil {
			t.Fatal("expected error for invalid turn URI")
		}
	})
}

// TestParsePeerFlagsSignaling checks that the M5 flags parse into peerOpts.
func TestParsePeerFlagsSignaling(t *testing.T) {
	opts, err := parsePeerFlags("offer", []string{
		"--addr", "127.0.0.1:5353",
		"--room", "r",
		"--signaling", "https",
		"--signaling-url", "https://sig.example.com",
		"--turn", "turn:a.example.com:3478",
		"--turn", "stun:b.example.com:3478",
		"--user", "bob",
		"--pass", "secret",
		"--metrics",
	})
	if err != nil {
		t.Fatalf("parsePeerFlags: %v", err)
	}
	if opts.signaling != "https" {
		t.Errorf("signaling = %q, want https", opts.signaling)
	}
	if opts.signalingURL != "https://sig.example.com" {
		t.Errorf("signalingURL = %q", opts.signalingURL)
	}
	if len(opts.turn) != 2 {
		t.Errorf("turn = %v, want two servers", opts.turn)
	}
	if opts.user != "bob" || opts.pass != "secret" {
		t.Errorf("user/pass = %q/%q, want bob/secret", opts.user, opts.pass)
	}
	if !opts.metrics {
		t.Error("metrics flag not parsed")
	}
}

// TestParsePeerFlagsValidation checks the cross-flag requirements: https needs a
// URL and dns needs an addr.
func TestParsePeerFlagsValidation(t *testing.T) {
	t.Run("https without url", func(t *testing.T) {
		if _, err := parsePeerFlags("offer", []string{"--room", "r", "--signaling", "https"}); err == nil {
			t.Fatal("expected error for https without --signaling-url")
		}
	})

	t.Run("dns without addr", func(t *testing.T) {
		if _, err := parsePeerFlags("offer", []string{"--room", "r"}); err == nil {
			t.Fatal("expected error for dns without --addr")
		}
	})

	t.Run("missing room", func(t *testing.T) {
		if _, err := parsePeerFlags("offer", []string{"--addr", "127.0.0.1:5353"}); err == nil {
			t.Fatal("expected error for missing --room")
		}
	})
}

// TestStartSessionSuccessMetrics checks that a successful handshake records one
// attempt, one success, and a handshake latency sample.
func TestStartSessionSuccessMetrics(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	m := metrics.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	room := "metrics-success-room"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman"); err != nil {
			t.Errorf("connectAnswer: %v", err)
		}
	}()
	sess, err := startSession(ctx, peer.RoleOfferer, peer.Config{
		Room:      room,
		Zone:      defaultZone,
		Transport: newTransport(srv.Addr(), defaultZone),
	}, m)
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	if sess == nil {
		t.Fatal("startSession returned nil session")
	}
	if got := m.ConnectionsAttempted(); got != 1 {
		t.Errorf("ConnectionsAttempted = %d, want 1", got)
	}
	if got := m.ConnectionsSucceeded(); got != 1 {
		t.Errorf("ConnectionsSucceeded = %d, want 1", got)
	}
	if got := m.ConnectionsFailed(); got != 0 {
		t.Errorf("ConnectionsFailed = %d, want 0", got)
	}
	if got := m.HandshakeCount(); got != 1 {
		t.Errorf("HandshakeCount = %d, want 1", got)
	}
	if m.HandshakeLatencyTotal() <= 0 {
		t.Errorf("HandshakeLatencyTotal = %v, want > 0", m.HandshakeLatencyTotal())
	}

	// Wait for the answerer goroutine so it cannot call t.Errorf after the
	// test returns (ctx is still live here, so the handshake completes).
	wg.Wait()
}

// TestStartSessionFailureMetrics checks that a failed handshake records a
// failure and a failed connection without recording a success.
func TestStartSessionFailureMetrics(t *testing.T) {
	m := metrics.New()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := startSession(ctx, peer.RoleOfferer, peer.Config{
		Room:      "nope",
		Zone:      defaultZone,
		Transport: newTransport("127.0.0.1:1", defaultZone),
	}, m)
	if err == nil {
		t.Fatal("expected a connect error against an unreachable server")
	}
	if got := m.ConnectionsFailed(); got != 1 {
		t.Errorf("ConnectionsFailed = %d, want 1", got)
	}
	if got := m.Failures(); got != 1 {
		t.Errorf("Failures = %d, want 1", got)
	}
	if got := m.ConnectionsSucceeded(); got != 0 {
		t.Errorf("ConnectionsSucceeded = %d, want 0", got)
	}
}

// TestRunPeerHTTPS drives a full offer/answer handshake and a data-channel
// message exchange over the HTTPS signaling transport, proving the fallback
// path works end to end.
func TestRunPeerHTTPS(t *testing.T) {
	h := https.NewHandler()
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	previousClient := http.DefaultClient
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = previousClient }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "https-room"
	ansOut := &lockedBuffer{}
	brAns := newBlockingReader()
	ansDone := make(chan error, 1)
	go func() {
		ansDone <- runPeer(ctx, peer.RoleAnswerer, peerOpts{
			room:         room,
			signaling:    "https",
			signalingURL: srv.URL,
		}, brAns, ansOut)
	}()

	offerOut := &lockedBuffer{}
	brOffer := newBlockingReader()
	offerDone := make(chan error, 1)
	go func() {
		offerDone <- runPeer(ctx, peer.RoleOfferer, peerOpts{
			room:         room,
			signaling:    "https",
			signalingURL: srv.URL,
		}, brOffer, offerOut)
	}()

	waitOut(t, offerOut, "offer: connected", 10*time.Second)
	waitOut(t, ansOut, "answer: connected", 10*time.Second)

	brOffer.ch <- []byte("hello over https\n")
	waitOut(t, ansOut, "answer: received \"hello over https\"", 10*time.Second)

	// Unblock both scanners so the runPeer goroutines can return.
	close(brOffer.done)
	close(brAns.done)

	select {
	case err := <-offerDone:
		if err != nil {
			t.Fatalf("runPeer offerer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("offerer did not return")
	}
	select {
	case err := <-ansDone:
		if err != nil {
			t.Fatalf("runPeer answerer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("answerer did not return")
	}
}

// TestRunPeerMetricsPrints checks that --metrics prints the Prometheus metrics
// text after a successful handshake.
func TestRunPeerMetricsPrints(t *testing.T) {
	srv, err := startServe()
	if err != nil {
		t.Fatalf("startServe: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := "metrics-print-room"
	go func() {
		if _, err := connectAnswer(ctx, srv.Addr(), defaultZone, room, "wingman"); err != nil {
			t.Errorf("connectAnswer: %v", err)
		}
	}()

	// An empty reader makes chat's scanner hit EOF immediately, so runPeer
	// returns right after the handshake completes and metrics are printed.
	out := &lockedBuffer{}
	opts := peerOpts{
		addr:    srv.Addr(),
		zone:    defaultZone,
		room:    room,
		label:   "wingman",
		metrics: true,
	}
	if err := runPeer(ctx, peer.RoleOfferer, opts, strings.NewReader(""), out); err != nil {
		t.Fatalf("runPeer with metrics: %v", err)
	}
	if !strings.Contains(out.String(), "wingman_connections_succeeded") {
		t.Fatalf("metrics output missing success counter: %q", out.String())
	}
	if !strings.Contains(out.String(), "# TYPE") {
		t.Fatalf("metrics output missing Prometheus TYPE lines: %q", out.String())
	}
}
