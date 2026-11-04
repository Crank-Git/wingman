package dns

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

type pollReply struct {
	reply  *mdns.Msg
	calls  int
	cancel context.CancelFunc
}

func (r *pollReply) Exchange(_ context.Context, m *mdns.Msg) (*mdns.Msg, error) {
	r.calls++
	if m.IsEdns0() != nil || m.Question[0].Name != "room.offer.sig.example.com." {
		panic("invalid Poll query")
	}
	if r.cancel != nil {
		r.cancel()
	}
	return r.reply, nil
}

func pollAnswer(t *testing.T, msg []byte) *mdns.Msg {
	t.Helper()
	records, err := dynUpdateRecords("room.offer.sig.example.com.", msg)
	if err != nil {
		t.Fatal(err)
	}
	m := new(mdns.Msg)
	m.Answer = records
	return m
}

func TestDynUpdatePollReconstructsShuffledAnswer(t *testing.T) {
	msg := bytes.Repeat([]byte("exact bytes"), 70)
	m := pollAnswer(t, msg)
	for i, j := 0, len(m.Answer)-1; i < j; i, j = i+1, j-1 {
		m.Answer[i], m.Answer[j] = m.Answer[j], m.Answer[i]
	}
	tr := newPublishTransport(t, "localhost:53")
	tr.Resolver = &pollReply{reply: m}
	got, err := tr.Poll(context.Background(), "RoOm", sig.Offer)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("reconstruction: %v", err)
	}
}

func TestDynUpdatePollRejectsInvalidAnswers(t *testing.T) {
	for _, mode := range []string{"owner", "type", "class", "duplicate", "incomplete", "grammar", "crc", "label", "total", "sequence", "parts", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			m := pollAnswer(t, bytes.Repeat([]byte("safe"), 20))
			rr := m.Answer[0].(*mdns.TXT)
			switch mode {
			case "owner":
				rr.Hdr.Name = "room.answer.sig.example.com."
			case "type":
				rr.Hdr.Rrtype = mdns.TypeA
			case "class":
				rr.Hdr.Class = mdns.ClassCHAOS
			case "duplicate":
				m.Answer = append(m.Answer, m.Answer[0])
			case "incomplete":
				m.Answer = m.Answer[:1]
			case "grammar":
				rr.Txt = []string{"00-3:private-sdp"}
			case "crc":
				m.Answer[len(m.Answer)-1].(*mdns.TXT).Txt = []string{"2-3:aaaaaaa"}
			case "label":
				rr.Txt = []string{"0-3:!private-sdp"}
			case "total":
				rr.Txt = []string{"0-65535:aa"}
			case "sequence":
				rr.Txt = []string{"3-3:aa"}
			case "parts":
				rr.Txt = append(rr.Txt, "extra")
			case "truncated":
				m.Truncated = true
			}
			tr := newPublishTransport(t, "localhost:53")
			r := &pollReply{reply: m}
			tr.Resolver = r
			if _, err := tr.Poll(context.Background(), "room", sig.Offer); err == nil || strings.Contains(err.Error(), "private-sdp") || r.calls != 1 {
				t.Fatalf("validation: %v calls=%d", err, r.calls)
			}
		})
	}
}

func TestDynUpdatePollRetriesOnlyMisses(t *testing.T) {
	for _, code := range []int{mdns.RcodeSuccess, mdns.RcodeNameError} {
		tr := newPublishTransport(t, "localhost:53")
		r := &pollReply{reply: &mdns.Msg{MsgHdr: mdns.MsgHdr{Rcode: code}}}
		tr.Resolver = r
		ctx, cancel := context.WithTimeout(context.Background(), 280*time.Millisecond)
		_, err := tr.Poll(ctx, "room", sig.Offer)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || r.calls < 2 {
			t.Fatalf("retry: %v calls=%d", err, r.calls)
		}
	}
	tr := newPublishTransport(t, "localhost:53")
	tr.Resolver = &updateFailure{rcode: mdns.RcodeRefused}
	var rejected *DynUpdateRcodeError
	if _, err := tr.Poll(context.Background(), "room", sig.Offer); !errors.As(err, &rejected) {
		t.Fatalf("rcode: %v", err)
	}
}

func TestDynUpdatePollBoundsActualResponse(t *testing.T) {
	tr := newPublishTransport(t, "localhost:53")
	for _, size := range []int{60*1024 + 1, 65536} {
		m, _ := sizedSignedMessage(t, tr, "RRset response", size)
		m.SetTsig(tr.key.Name, tr.key.Algorithm, 300, 0)
		m.IsTsig().MAC = strings.Repeat("00", 32)
		m.IsTsig().MACSize = 32
		tr.Resolver = &pollReply{reply: m}
		var bounded *DynUpdateSizeError
		if _, err := tr.Poll(context.Background(), "room", sig.Offer); !errors.As(err, &bounded) {
			t.Fatalf("wire bound: %v", err)
		}
	}
	tr.Resolver = &pollReply{reply: pollAnswer(t, make([]byte, maxMessageBytes+1))}
	var bounded *DynUpdateSizeError
	if _, err := tr.Poll(context.Background(), "room", sig.Offer); !errors.As(err, &bounded) {
		t.Fatalf("decoded bound: %v", err)
	}
}

func TestDynUpdatePollPreservesFailures(t *testing.T) {
	cause := errors.New("route unavailable")
	for _, r := range []*updateFailure{{err: cause}, {nilReply: true}} {
		tr := newPublishTransport(t, "localhost:53")
		tr.Resolver = r
		_, err := tr.Poll(context.Background(), "room", sig.Offer)
		if err == nil || r.err != nil && !errors.Is(err, cause) {
			t.Fatalf("cause: %v", err)
		}
	}
	tr := newPublishTransport(t, "localhost:53")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &pollReply{}
	tr.Resolver = r
	if _, err := tr.Poll(ctx, "room", sig.Offer); !errors.Is(err, context.Canceled) || r.calls != 0 {
		t.Fatalf("cancel: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	r.cancel = cancel
	r.reply = new(mdns.Msg)
	if _, err := tr.Poll(ctx, "room", sig.Offer); !errors.Is(err, context.Canceled) || r.calls != 1 {
		t.Fatalf("retry cancel: %v", err)
	}
	var zero DynUpdate
	if _, err := zero.Poll(context.Background(), "room", sig.Offer); err == nil {
		t.Fatal("zero configuration accepted")
	}
}

func TestDynUpdatePollAuthenticatesTCPRoundtrip(t *testing.T) {
	for _, mode := range []string{"valid", "unsigned", "bad signature"} {
		t.Run(mode, func(t *testing.T) {
			key := testTSIGKey()
			server := &mdns.Server{TsigSecret: key.secrets(), MsgAcceptFunc: func(mdns.Header) mdns.MsgAcceptAction { return mdns.MsgAccept }}
			fixture := &updateFixture{t: t, mode: mode, key: key}
			server.Handler = fixture
			if mode == "bad signature" {
				server.TsigSecret = map[string]string{key.Name: "b3RoZXI="}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server.Listener = listener
			if err := activateListener(server); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := server.Shutdown(); err != nil {
					t.Error(err)
				}
			})
			tr := newPublishTransport(t, listener.Addr().String())
			msg := bytes.Repeat([]byte("signed bytes"), 40)
			if mode == "valid" {
				if err := tr.Publish(context.Background(), "room", sig.Offer, msg); err != nil {
					t.Fatal(err)
				}
			} else {
				fixture.stored = pollAnswer(t, msg).Answer
			}
			got, err := tr.Poll(context.Background(), "room", sig.Offer)
			if mode == "valid" {
				if err != nil || !bytes.Equal(got, msg) {
					t.Fatalf("roundtrip: %v", err)
				}
			} else if err == nil {
				t.Fatal("unauthenticated response accepted")
			}
		})
	}
}

func TestDynUpdatePollValidatesBoundsBeforeContent(t *testing.T) {
	tr := newPublishTransport(t, "localhost:53")
	m := pollAnswer(t, []byte("safe"))
	m.SetTsig(tr.key.Name, tr.key.Algorithm, 300, 0)
	m.IsTsig().OtherData = strings.Repeat("00", 60*1024)
	m.IsTsig().OtherLen = 60 * 1024
	tr.Resolver = &pollReply{reply: m}
	var bounded *DynUpdateSizeError
	if _, err := tr.Poll(context.Background(), "room", sig.Offer); !errors.As(err, &bounded) {
		t.Fatalf("actual TSIG bound: %v", err)
	}
	m = pollAnswer(t, make([]byte, maxMessageBytes))
	tr.Resolver = &pollReply{reply: m}
	if got, err := tr.Poll(context.Background(), "room", sig.Offer); err != nil || len(got) != maxMessageBytes {
		t.Fatalf("message boundary: %v", err)
	}
	m = pollAnswer(t, []byte("safe"))
	m.Answer[0].(*mdns.TXT).Txt = []string{strings.Repeat("x", 256)}
	tr.Resolver = &pollReply{reply: m}
	if _, err := tr.Poll(context.Background(), "room", sig.Offer); err == nil {
		t.Fatal("invalid wire data accepted")
	}
	if _, err := reassembleDynUpdate("room.offer.sig.example.com.", make([]mdns.RR, maxMessageBytes+5)); err == nil {
		t.Fatal("fragment count accepted")
	}
}
