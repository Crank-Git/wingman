package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Crank-Git/wingman/internal/https"
	"github.com/Crank-Git/wingman/internal/sig"
)

func TestPeerFlagsPermitExplicitLoopbackHTTP(t *testing.T) {
	srv := httptest.NewServer(https.NewHandler())
	defer srv.Close()
	opts, err := parsePeerFlags("offer", []string{"--room", "r", "--signaling", "https", "--signaling-url", srv.URL, "--signaling-allow-loopback-http"})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := buildTransport(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Publish(context.Background(), "r", sig.Offer, []byte("sdp")); err != nil {
		t.Fatal(err)
	}
	got, err := tr.Poll(context.Background(), "r", sig.Offer)
	if err != nil || string(got) != "sdp" {
		t.Fatalf("Poll = %q, %v", got, err)
	}
}
