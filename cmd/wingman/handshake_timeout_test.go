package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/peer"
	"github.com/Crank-Git/wingman/internal/sig"
)

// pendingDeadlineContext models a socket deadline that precedes the context timer callback.
type pendingDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c pendingDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

type failedPollTransport struct {
	sig.Transport
	err error
}

func (tr failedPollTransport) Poll(context.Context, string, sig.Kind) ([]byte, error) {
	return nil, tr.err
}

func TestStartSessionPreservesSocketDeadlineCause(t *testing.T) {
	socketErr := &net.OpError{Op: "read", Net: "udp", Err: os.ErrDeadlineExceeded}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	conn, dialErr := (&net.Dialer{}).DialContext(expired, "udp", "127.0.0.1:1")
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("dial succeeds after deadline")
	}
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		cause    error
		deadline bool
	}{
		{"socket expires before context callback", pendingDeadlineContext{context.Background(), time.Now().Add(-time.Second)}, socketErr, true},
		{"dial timeout names context deadline", pendingDeadlineContext{context.Background(), time.Now().Add(-time.Second)}, dialErr, true},
		{"socket expires before handshake deadline", pendingDeadlineContext{context.Background(), time.Now().Add(time.Hour)}, socketErr, false},
		{"socket expires without handshake deadline", context.Background(), socketErr, false},
		{"other failure follows handshake deadline", pendingDeadlineContext{context.Background(), time.Now().Add(-time.Second)}, errors.New("transport failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ctx.Err() != nil {
				t.Fatal("context callback already ran")
			}
			_, err := startSession(tc.ctx, peer.RoleAnswerer, peer.Config{
				Room: "deadline", Transport: failedPollTransport{err: fmt.Errorf("poll: %w", tc.cause)},
			}, metrics.New())
			if !errors.Is(err, tc.cause) {
				t.Fatalf("lost transport cause: %v", err)
			}
			if errors.Is(err, context.DeadlineExceeded) != tc.deadline {
				t.Fatalf("deadline cause: got %v, want %v", err, tc.deadline)
			}
			if tc.deadline && !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
				t.Fatalf("deadline diagnostic omits context cause: %v", err)
			}
		})
	}
}
