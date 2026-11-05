package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

func TestDynUpdateExchangeAddsSafeAuthenticationGuidance(t *testing.T) {
	for _, operation := range []string{"publish", "poll", "cleanup"} {
		t.Run(operation, func(t *testing.T) {
			cause := fmt.Errorf("resolver: %w", mdns.ErrAuth)
			tr := newPublishTransport(t, "localhost:53")
			tr.Resolver = &updateFailure{err: cause}
			err := runDynUpdateOperation(tr, operation)
			if !errors.Is(err, cause) || !errors.Is(err, mdns.ErrAuth) {
				t.Fatalf("lost authentication cause: %v", err)
			}
			for _, text := range []string{"DynUpdate " + operation + " exchange:", "zone", "authoritative endpoint", "key identity/file", "update policy"} {
				if !strings.Contains(err.Error(), text) {
					t.Errorf("missing %q: %v", text, err)
				}
			}
			for _, private := range []string{tr.key.Secret, "private-sdp", "DNS rcode", "NOTAUTH"} {
				if strings.Contains(err.Error(), private) {
					t.Errorf("error contains private data or an untrusted cause: %v", err)
				}
			}
		})
	}
}

func TestDynUpdateExchangeKeepsUnrelatedErrors(t *testing.T) {
	for _, operation := range []string{"publish", "poll", "cleanup"} {
		tr := newPublishTransport(t, "localhost:53")
		cause := errors.New("route unavailable")
		tr.Resolver = &updateFailure{err: cause}
		err := runDynUpdateOperation(tr, operation)
		if !errors.Is(err, cause) || err.Error() != "DynUpdate "+operation+" exchange: route unavailable" {
			t.Fatalf("changed unrelated error: %v", err)
		}
	}
}

func runDynUpdateOperation(tr *DynUpdate, operation string) error {
	switch operation {
	case "publish":
		return tr.Publish(context.Background(), "room", sig.Offer, []byte("private-sdp"))
	case "poll":
		_, err := tr.Poll(context.Background(), "room", sig.Offer)
		return err
	default:
		return tr.Cleanup(context.Background(), "room")
	}
}
