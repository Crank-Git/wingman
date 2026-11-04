package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/https"
	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

type doctorFake struct {
	room        string
	publishErr  error
	cleanupErr  error
	cleaned     bool
	expired     bool
	waitPublish bool
	waitCleanup bool
	mismatch    bool
}

func TestDoctorProbesDNSProtocols(t *testing.T) {
	srv, err := startServe(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	opts := doctorOptions{peerOpts: peerOpts{signaling: "dns", addr: srv.Addr(), zone: defaultZone}, timeout: time.Second}
	report := executeDoctor(context.Background(), opts)
	if !report.Success || len(report.Checks) != 4 || report.Checks[0].Name != "dns_udp" || report.Checks[1].Name != "dns_tcp" {
		t.Fatalf("%+v", report)
	}
	if err := runDoctor(context.Background(), []string{"--addr", srv.Addr()}); err != nil {
		t.Fatal(err)
	}
	if doctorRoom() == doctorRoom() {
		t.Fatal("rooms repeat")
	}
}

func TestDoctorProbesHTTPSAuthentication(t *testing.T) {
	for _, secret := range []string{"", "configured"} {
		t.Run("secret="+secret, func(t *testing.T) {
			handler := https.NewHandler()
			handler.Secret = secret
			srv := httptest.NewServer(handler)
			defer srv.Close()
			opts := doctorOptions{peerOpts: peerOpts{signaling: "https", signalingURL: srv.URL, allowLoopbackHTTP: true, signalingSecret: secret}, timeout: time.Second}
			report := executeDoctor(context.Background(), opts)
			want := 2
			if secret != "" {
				want = 3
			}
			if !report.Success || len(report.Checks) != want {
				t.Fatalf("%+v", report)
			}
			if secret != "" {
				opts.signalingSecret = "wrong-secret"
				report = executeDoctor(context.Background(), opts)
				if report.Success || report.Checks[0].Name != "auth" || report.Checks[0].Success {
					t.Fatalf("%+v", report)
				}
			}
		})
	}
}

func TestDoctorReportsTLSFailure(t *testing.T) {
	srv := httptest.NewTLSServer(https.NewHandler())
	defer srv.Close()
	report := executeDoctor(context.Background(), doctorOptions{peerOpts: peerOpts{signaling: "https", signalingURL: srv.URL}, timeout: time.Second})
	if report.Success || report.Checks[0].Name != "tls" || report.Checks[0].Success {
		t.Fatalf("%+v", report)
	}
}

func TestDoctorValidatesConfiguration(t *testing.T) {
	for _, args := range [][]string{{}, {"--signaling", "dynupdate", "--addr", "127.0.0.1:1"}, {"--timeout", "oops"}} {
		if _, err := parseDoctorFlags(args); err == nil {
			t.Fatalf("accepts %v", args)
		}
	}
	for _, opts := range []peerOpts{
		{signaling: "other"}, {signaling: "dns", dnsTSIGKeyFile: "absent"},
		{signaling: "https", signalingURL: "http://example.com"},
		{signaling: "https", signalingURL: "https://user:secret@example.com"},
		{signaling: "https", signalingURL: "https://%zz"},
	} {
		report := executeDoctor(context.Background(), doctorOptions{peerOpts: opts, timeout: time.Second})
		if report.Success || report.Checks[0].Name != "configuration" {
			t.Fatalf("%+v", report)
		}
	}
	if err := runDoctor(context.Background(), []string{"--timeout", "0"}); err == nil {
		t.Fatal("accepts zero")
	}
	if err := runDoctor(context.Background(), []string{"--help"}); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorBuildsSignedTCPPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"fixture.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := parseDoctorFlags([]string{"--signaling", "dynupdate", "--addr", "127.0.0.1:1", "--zone", "example.test", "--dns-tsig-key-file", path})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildDoctorPlan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.stages) != 1 || plan.stages[0].name != "dns_tcp" || !plan.authenticated {
		t.Fatalf("%+v", plan)
	}
	report := probeDoctor(context.Background(), plan, 20*time.Millisecond, doctorRoom)
	if report.Success {
		t.Fatal("unreachable TCP endpoint succeeds")
	}
	var out bytes.Buffer
	if err := writeDoctorReport(&out, report); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "c2VjcmV0") {
		t.Fatal("report contains secret")
	}
	if err := writeDoctorReport(doctorErrorWriter{}, report); err == nil {
		t.Fatal("ignores output failure")
	}
}

type doctorErrorWriter struct{}

func (doctorErrorWriter) Write(p []byte) (int, error) { return 0, errors.New("output failed") }

func TestDoctorClassifiesCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		check := doctorResult(context.Background(), "publication", time.Now(), cause)
		if check.Success || check.ErrorCode == "probe_failed" {
			t.Fatalf("%+v", check)
		}
	}
}

func (f *doctorFake) Publish(ctx context.Context, room string, k sig.Kind, msg []byte) error {
	f.room = room
	if f.waitPublish {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.publishErr
}
func (f *doctorFake) Poll(ctx context.Context, room string, k sig.Kind) ([]byte, error) {
	if f.mismatch {
		return []byte("wrong"), nil
	}
	return []byte(doctorPayload), nil
}
func (f *doctorFake) Cleanup(ctx context.Context, room string) error {
	f.cleaned = true
	f.expired = ctx.Err() != nil
	if f.waitCleanup {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.cleanupErr
}

func TestDoctorBoundsCleanupAfterDeadline(t *testing.T) {
	f := &doctorFake{waitPublish: true, waitCleanup: true}
	start := time.Now()
	report := probeDoctor(context.Background(), doctorPlan{transport: f}, 10*time.Millisecond, doctorRoom)
	elapsed := time.Since(start)
	if elapsed > 1500*time.Millisecond || elapsed < time.Second || !f.cleaned || f.expired || report.Success {
		t.Fatalf("elapsed=%s report=%+v fake=%+v", elapsed, report, f)
	}
	if report.Checks[0].ErrorCode != "deadline_exceeded" {
		t.Fatalf("%+v", report)
	}
}

func TestDoctorRejectsProbeMismatch(t *testing.T) {
	report := probeDoctor(context.Background(), doctorPlan{transport: &doctorFake{mismatch: true}}, time.Second, doctorRoom)
	if report.Success || report.Checks[1].Success {
		t.Fatalf("%+v", report)
	}
}

func TestDoctorRequiresTCPWithUDP(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &mdns.Server{PacketConn: conn, Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, r *mdns.Msg) {
		reply := new(mdns.Msg)
		reply.SetReply(r)
		if err := w.WriteMsg(reply); err != nil {
			t.Error(err)
		}
	})}
	done := make(chan error, 1)
	go func() { done <- srv.ActivateAndServe() }()
	defer func() {
		if err := srv.Shutdown(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	report := executeDoctor(context.Background(), doctorOptions{peerOpts: peerOpts{signaling: "dns", addr: conn.LocalAddr().String(), zone: defaultZone}, timeout: 30 * time.Millisecond})
	if report.Success || !report.Checks[0].Success || report.Checks[1].Success {
		t.Fatalf("%+v", report)
	}
}

func TestDoctorAuthenticatesCustomDNS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, []byte(`{"name":"fixture.example.","algorithm":"hmac-sha256.","secret":"c2VjcmV0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	key, err := dns.LoadTSIGKey(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := startServe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	report := executeDoctor(context.Background(), doctorOptions{peerOpts: peerOpts{signaling: "dns", addr: srv.Addr(), zone: defaultZone, dnsTSIGKeyFile: path}, timeout: time.Second})
	if !report.Success || report.Checks[2].Name != "auth" {
		t.Fatalf("%+v", report)
	}
}

func TestDoctorCleansUncertainPublication(t *testing.T) {
	f := &doctorFake{publishErr: errors.New("private server payload")}
	report := probeDoctor(context.Background(), doctorPlan{transport: f}, time.Second, func() string { return "doctor-test" })
	if report.Success || !f.cleaned || f.expired || f.room != "doctor-test" {
		t.Fatalf("report=%+v fake=%+v", report, f)
	}
}

func TestDoctorFailsCleanup(t *testing.T) {
	f := &doctorFake{cleanupErr: errors.New("secret")}
	report := probeDoctor(context.Background(), doctorPlan{transport: f}, time.Second, func() string { return "doctor-test" })
	if report.Success || report.Checks[len(report.Checks)-1].Name != "cleanup" {
		t.Fatalf("report=%+v", report)
	}
}

func TestDoctorUsesFreshCleanupContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &doctorFake{}
	report := probeDoctor(ctx, doctorPlan{transport: f}, time.Second, func() string { return "doctor-test" })
	if report.Success || f.cleaned {
		t.Fatalf("report=%+v fake=%+v", report, f)
	}
}

func TestDoctorRejectsRoomAndInvalidTimeout(t *testing.T) {
	for _, args := range [][]string{{"--room", "user"}, {"--timeout", "0"}, {"--timeout", "-1s"}, {"extra"}} {
		if _, err := parseDoctorFlags(args); err == nil {
			t.Fatalf("accepts %v", args)
		}
	}
	opts, err := parseDoctorFlags([]string{"--addr", "127.0.0.1:1"})
	if err != nil || opts.timeout != 5*time.Second {
		t.Fatalf("opts=%+v err=%v", opts, err)
	}
}

func TestDoctorRejectsUnreachableEndpoint(t *testing.T) {
	if run(context.Background(), []string{"doctor", "--addr", "127.0.0.1:1", "--timeout", "20ms"}) == 0 {
		t.Fatal("unreachable endpoint succeeds")
	}
}
