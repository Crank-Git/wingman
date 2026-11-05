//go:build bindintegration

package dns

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

func bindTransport(t *testing.T) *DynUpdate {
	t.Helper()
	key, err := LoadTSIGKey(os.Getenv("WINGMAN_BIND_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewDynUpdate(os.Getenv("WINGMAN_BIND_ADDR"), "example.test", key)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func bindQuery(t *testing.T, tr *DynUpdate, owner string, kind uint16) []mdns.RR {
	t.Helper()
	m := new(mdns.Msg)
	m.SetQuestion(owner, kind)
	reply, err := tr.Resolver.Exchange(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Rcode != mdns.RcodeSuccess && reply.Rcode != mdns.RcodeNameError {
		t.Fatalf("query rcode: %s", mdns.RcodeToString[reply.Rcode])
	}
	return reply.Answer
}

func bindPublish(t *testing.T, tr *DynUpdate, room string, kind sig.Kind, msg []byte) {
	t.Helper()
	if err := tr.Publish(context.Background(), room, kind, msg); err != nil {
		t.Fatal(err)
	}
}

func TestBINDReplacesLongRRsetAndConsumesBothKinds(t *testing.T) {
	tr := bindTransport(t)
	for _, kind := range []sig.Kind{sig.Offer, sig.Answer} {
		bindPublish(t, tr, "replacement", kind, bytes.Repeat([]byte("long message"), 100))
		want := []byte("short " + kind.String())
		bindPublish(t, tr, "replacement", kind, want)
		owner, err := tr.owner("replacement", kind)
		if err != nil {
			t.Fatal(err)
		}
		if got := bindQuery(t, tr, owner, mdns.TypeTXT); len(got) != 1 {
			t.Fatalf("replacement has %d records", len(got))
		}
		got, err := tr.Poll(context.Background(), "replacement", kind)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("replacement Poll: %q, %v", got, err)
		}
		if len(bindQuery(t, tr, owner, mdns.TypeTXT)) != 0 {
			t.Fatal("Poll leaves consumed records")
		}
	}
}

type bindShuffledResolver struct{ Resolver }

func (r bindShuffledResolver) Exchange(ctx context.Context, m *mdns.Msg) (*mdns.Msg, error) {
	reply, err := r.Resolver.Exchange(ctx, m)
	if err == nil && m.Opcode == mdns.OpcodeQuery {
		// The dialer authenticates the actual BIND reply before this order change.
		for i, j := 0, len(reply.Answer)-1; i < j; i, j = i+1, j-1 {
			reply.Answer[i], reply.Answer[j] = reply.Answer[j], reply.Answer[i]
		}
	}
	return reply, err
}

func TestBINDReconstructsShuffledRecords(t *testing.T) {
	tr := bindTransport(t)
	want := bytes.Repeat([]byte("unordered bytes"), 100)
	bindPublish(t, tr, "shuffled", sig.Offer, want)
	tr.Resolver = bindShuffledResolver{tr.Resolver}
	got, err := tr.Poll(context.Background(), "shuffled", sig.Offer)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("shuffled Poll: %v", err)
	}
}

func TestBINDRejectsUnauthorizedUpdates(t *testing.T) {
	tr := bindTransport(t)
	for _, mode := range []string{"outside subtree", "non TXT", "wrong zone", "wrong secret", "unknown key", "unsigned"} {
		t.Run(mode, func(t *testing.T) {
			m := new(mdns.Msg)
			m.SetUpdate("example.test.")
			rr := mdns.RR(&mdns.TXT{Hdr: mdns.RR_Header{Name: "reject.offer.sig.example.test.", Rrtype: mdns.TypeTXT, Class: mdns.ClassINET, Ttl: 1}, Txt: []string{"reject"}})
			dialer := *tr.Resolver.(*Dialer)
			key := tr.key
			dialer.TSIGKey = &key
			switch mode {
			case "outside subtree":
				rr.Header().Name = "outside.example.test."
			case "non TXT":
				rr = &mdns.A{Hdr: mdns.RR_Header{Name: rr.Header().Name, Rrtype: mdns.TypeA, Class: mdns.ClassINET, Ttl: 1}, A: net.ParseIP("192.0.2.2")}
			case "wrong zone":
				m.SetUpdate("other.test.")
			case "wrong secret":
				key.Secret = "d3Jvbmc="
			case "unknown key":
				key.Name = "unknown.example."
			case "unsigned":
				dialer.TSIGKey = nil
			}
			m.Insert([]mdns.RR{rr})
			reply, err := dialer.Exchange(context.Background(), m)
			if err == nil && (reply == nil || reply.Rcode == mdns.RcodeSuccess) {
				t.Fatal("BIND accepts unauthorized update")
			}
			if mode == "outside subtree" || mode == "non TXT" {
				if err != nil || reply.Rcode != mdns.RcodeRefused {
					t.Fatalf("policy does not return signed REFUSED: %v, %v", reply, err)
				}
			}
		})
	}
	if len(bindQuery(t, tr, "reject.offer.sig.example.test.", mdns.TypeTXT)) != 0 {
		t.Fatal("rejected update leaves records")
	}
}

func TestBINDBoundsMessagesAndCancellation(t *testing.T) {
	tr := bindTransport(t)
	var size *DynUpdateSizeError
	if err := tr.Publish(context.Background(), "bounds", sig.Offer, make([]byte, 16385)); !errors.As(err, &size) {
		t.Fatalf("message bound: %v", err)
	}
	large := *tr
	large.Zone = strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + ".test"
	if err := large.Publish(context.Background(), "bounds", sig.Offer, make([]byte, 16384)); !errors.As(err, &size) {
		t.Fatalf("wire bound: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	if _, err := tr.Poll(ctx, "absent", sig.Offer); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Poll deadline: %v", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if err := tr.Publish(canceled, "bounds", sig.Offer, []byte("data")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish cancellation: %v", err)
	}
}

func TestBINDFailsWithoutTCPRoute(t *testing.T) {
	tr := bindTransport(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	tr.Resolver.(*Dialer).Addr = address
	err = tr.Publish(context.Background(), "no-route", sig.Offer, []byte("data"))
	var network *net.OpError
	if !errors.As(err, &network) || network.Net != "tcp" {
		t.Fatalf("missing TCP route: %v", err)
	}
}

func TestBINDCleanupPreservesUnrelatedRecords(t *testing.T) {
	tr := bindTransport(t)
	for _, room := range []string{"selected", "preserved"} {
		for _, kind := range []sig.Kind{sig.Offer, sig.Answer} {
			bindPublish(t, tr, room, kind, []byte("orphaned data"))
		}
	}
	command := exec.Command(os.Getenv("WINGMAN_BIND_CLI"), "cleanup", "--addr", os.Getenv("WINGMAN_BIND_ADDR"), "--zone", "example.test", "--room", "selected", "--dns-tsig-key-file", os.Getenv("WINGMAN_BIND_KEY"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("explicit CLI cleanup: %v: %s", err, output)
	}
	for _, kind := range []sig.Kind{sig.Offer, sig.Answer} {
		owner, err := tr.owner("selected", kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(bindQuery(t, tr, owner, mdns.TypeTXT)) != 0 {
			t.Fatal("cleanup leaves selected records")
		}
		owner, err = tr.owner("preserved", kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(bindQuery(t, tr, owner, mdns.TypeTXT)) != 1 {
			t.Fatal("cleanup removes another room")
		}
	}
	for owner, kind := range map[string]uint16{"unrelated.example.test.": mdns.TypeTXT, "selected.offer.sig.example.test.": mdns.TypeA} {
		if len(bindQuery(t, tr, owner, kind)) != 1 {
			t.Fatal("cleanup removes unrelated data")
		}
	}
	if err := tr.Cleanup(context.Background(), "preserved"); err != nil {
		t.Fatal(err)
	}
}

type bindPeer struct {
	command *exec.Cmd
	input   io.WriteCloser
	log     string
}

func startBINDPeer(t *testing.T, ctx context.Context, role string) bindPeer {
	t.Helper()
	path := filepath.Join(t.TempDir(), role+".log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	command := exec.CommandContext(ctx, os.Getenv("WINGMAN_BIND_CLI"), role, "--signaling", "dynupdate", "--addr", os.Getenv("WINGMAN_BIND_ADDR"), "--zone", "example.test", "--room", "bind-cli", "--dns-tsig-key-file", os.Getenv("WINGMAN_BIND_KEY"), "--handshake-timeout", "20s")
	command.Stdout, command.Stderr = file, file
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Shutdown after a failed assertion must also release the child and its pipe.
		_ = input.Close()
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return bindPeer{command, input, path}
}

func waitBINDNotice(t *testing.T, ctx context.Context, peer bindPeer, notice string) {
	t.Helper()
	for {
		log, err := os.ReadFile(peer.log)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(log), notice) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("peer does not report %q: %s", notice, log)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestBINDCLIExchangesTextAndCleansBothKinds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	offer := startBINDPeer(t, ctx, "offer")
	answer := startBINDPeer(t, ctx, "answer")
	waitBINDNotice(t, ctx, offer, "offer: connected")
	waitBINDNotice(t, ctx, answer, "answer: connected")
	if _, err := io.WriteString(offer.input, "BIND message from offer\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(answer.input, "BIND message from answer\n"); err != nil {
		t.Fatal(err)
	}
	waitBINDNotice(t, ctx, answer, `answer: received "BIND message from offer"`)
	waitBINDNotice(t, ctx, offer, `offer: received "BIND message from answer"`)
	for _, peer := range []bindPeer{offer, answer} {
		if err := peer.input.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, peer := range []bindPeer{offer, answer} {
		if err := peer.command.Wait(); err != nil {
			t.Fatalf("peer exit: %v", err)
		}
	}
	tr := bindTransport(t)
	for _, kind := range []sig.Kind{sig.Offer, sig.Answer} {
		owner, err := tr.owner("bind-cli", kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(bindQuery(t, tr, owner, mdns.TypeTXT)) != 0 {
			t.Fatal("CLI exchange leaves signaling records")
		}
	}
}
