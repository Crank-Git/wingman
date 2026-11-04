package dns

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/miekg/dns"

	"github.com/Crank-Git/wingman/internal/sig"
)

func TestPublishStopsAfterCancellation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cancelBefore bool
		wantCalls    int
	}{
		{name: "cancelled before publication", cancelBefore: true, wantCalls: 0},
		{name: "cancelled after first fragment", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelBefore {
				cancel()
			}
			resolver := &cancelPublishResolver{cancel: cancel}
			tr := &WriteOverQuery{Resolver: resolver, Zone: "example.com"}
			msg := bytes.Repeat([]byte("fragment"), 100)
			if err := tr.Publish(ctx, "room", sig.Offer, msg); !errors.Is(err, context.Canceled) {
				t.Errorf("Publish error = %v, want context.Canceled", err)
			}
			if resolver.calls != tc.wantCalls {
				t.Errorf("resolver calls = %d, want %d", resolver.calls, tc.wantCalls)
			}
		})
	}
}

type cancelPublishResolver struct {
	cancel context.CancelFunc
	calls  int
	next   Resolver
}

func (r *cancelPublishResolver) Exchange(ctx context.Context, query *dns.Msg) (*dns.Msg, error) {
	r.calls++
	response := new(dns.Msg).SetReply(query)
	var err error
	if r.next != nil {
		response, err = r.next.Exchange(ctx, query)
	}
	r.cancel()
	return response, err
}

func TestPublishCancellationLeavesDNSMessageIncomplete(t *testing.T) {
	srv := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolver := &cancelPublishResolver{cancel: cancel, next: &Dialer{Addr: srv.Addr()}}
	tr := &WriteOverQuery{Resolver: resolver, Zone: "example.com"}
	if err := tr.Publish(ctx, "cancelled", sig.Offer, bytes.Repeat([]byte("fragment"), 100)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish error = %v, want context.Canceled", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	reader := newClient(srv)
	reader.PollRetries = 1
	_, err := reader.Poll(context.Background(), "cancelled", sig.Offer)
	var pollError *PollError
	if !errors.As(err, &pollError) {
		t.Fatalf("Poll error = %v, want incomplete publication to exhaust retries", err)
	}
}
