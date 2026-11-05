package main

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

	wdns "github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

func cleanupCLIKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"wingman.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDynUpdateCleanupRequiresScopedInputs(t *testing.T) {
	flags := []string{"--addr", "localhost:53", "--zone", "example.com", "--room", "room", "--dns-tsig-key-file", cleanupCLIKey(t)}
	for i := 0; i < len(flags); i += 2 {
		args := append([]string{}, flags[:i]...)
		args = append(args, flags[i+2:]...)
		if err := dispatch(context.Background(), append([]string{"cleanup"}, args...)); err == nil || !strings.Contains(err.Error(), "requires") {
			t.Fatalf("missing %s: %v", flags[i], err)
		}
	}
	for _, args := range [][]string{{"--bogus"}, append(append([]string{}, flags...), "extra"), {"--addr", "localhost:53", "--zone", "example.com", "--room", "room", "--dns-tsig-key-file", "absent"}} {
		if err := dispatch(context.Background(), append([]string{"cleanup"}, args...)); err == nil {
			t.Fatal("invalid cleanup accepted")
		}
	}
	for _, replacement := range []struct {
		index int
		value string
	}{{1, "bad-address"}, {5, "bad.room"}} {
		args := append([]string{}, flags...)
		args[replacement.index] = replacement.value
		if err := dispatch(context.Background(), append([]string{"cleanup"}, args...)); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

type warningCleanupResolver struct{}

func (warningCleanupResolver) Exchange(_ context.Context, m *mdns.Msg) (*mdns.Msg, error) {
	if m.Opcode == mdns.OpcodeUpdate {
		return nil, errors.New("private-server-error c2VjcmV0 private-sdp")
	}
	frags, err := sig.Fragments([]byte("private-sdp"), 0)
	if err != nil {
		return nil, err
	}
	label, err := sig.EncodeLabel(frags[0].Data)
	if err != nil {
		return nil, err
	}
	return &mdns.Msg{Answer: []mdns.RR{&mdns.TXT{Hdr: mdns.RR_Header{Name: m.Question[0].Name, Rrtype: mdns.TypeTXT, Class: mdns.ClassINET}, Txt: []string{"0-1:" + label}}}}, nil
}

func TestDynUpdateCleanupWarningOmitsPrivateData(t *testing.T) {
	tr, err := buildTransport(peerOpts{signaling: "dynupdate", addr: "localhost:53", zone: "example.com", dnsTSIGKeyFile: cleanupCLIKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	tr.(*wdns.DynUpdate).Resolver = warningCleanupResolver{}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	got, pollErr := tr.Poll(context.Background(), "room", sig.Offer)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if closeErr := r.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if pollErr != nil || string(got) != "private-sdp" || string(out) != "wingman: automatic DynUpdate cleanup failed; run wingman cleanup for this room.\n" {
		t.Fatalf("warning: %q poll error: %v", out, pollErr)
	}
}

func startCleanupCLIServer(t *testing.T) (string, <-chan *mdns.Msg) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	updates := make(chan *mdns.Msg, 2)
	server := &mdns.Server{Listener: ln, TsigSecret: map[string]string{"wingman.example.": "c2VjcmV0"}, NotifyStartedFunc: func() { close(ready) }, MsgAcceptFunc: func(mdns.Header) mdns.MsgAcceptAction { return mdns.MsgAccept }}
	server.Handler = mdns.HandlerFunc(func(w mdns.ResponseWriter, req *mdns.Msg) {
		if req.IsTsig() == nil || w.TsigStatus() != nil {
			t.Error("unsigned cleanup")
		}
		updates <- req.Copy()
		reply := new(mdns.Msg)
		reply.SetReply(req)
		if req.Ns[0].Header().Name == "refused.offer.sig.example.com." {
			reply.Rcode = mdns.RcodeRefused
		}
		reply.SetTsig("wingman.example.", mdns.HmacSHA256, 300, time.Now().Unix())
		if err := w.WriteMsg(reply); err != nil {
			t.Error(err)
		}
	})
	go func() {
		if err := server.ActivateAndServe(); err != nil {
			t.Error(err)
		}
	}()
	<-ready
	t.Cleanup(func() {
		if err := server.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return ln.Addr().String(), updates
}

func checkDynUpdateCleanupCLI(t *testing.T, bin string) {
	t.Helper()
	addr, updates := startCleanupCLIServer(t)
	key := cleanupCLIKey(t)
	for _, room := range []string{"room", "refused"} {
		args := []string{"cleanup", "--addr", addr, "--zone", "example.com", "--room", room, "--dns-tsig-key-file", key}
		result := dispatch(context.Background(), args)
		if (result != nil) != (room == "refused") {
			t.Fatalf("dispatch cleanup: %v", result)
		}
		<-updates
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		cancel()
		if room == "room" && (err != nil || !bytes.Contains(out, []byte("cleanup complete"))) {
			t.Fatalf("cleanup: %v %s", err, out)
		}
		if room == "refused" && (err == nil || !bytes.Contains(out, []byte("DNS rcode 5"))) {
			t.Fatalf("rejection: %v %s", err, out)
		}
		if bytes.Contains(out, []byte("c2VjcmV0")) {
			t.Fatal("cleanup exposes the key")
		}
		req := <-updates
		if req.Opcode != mdns.OpcodeUpdate || len(req.Ns) != 2 || req.Ns[0].Header().Name != room+".offer.sig.example.com." || req.Ns[1].Header().Name != room+".answer.sig.example.com." {
			t.Fatalf("cleanup scope: %v", req)
		}
	}
}
