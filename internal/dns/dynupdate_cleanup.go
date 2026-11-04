package dns

import (
	"context"
	"errors"
	"fmt"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

// Cleanup deletes the room's offer and answer TXT RRsets in one signed UPDATE.
// Invalid room or configuration returns an error before network access.
// Exchange failures preserve their causes. DNS rejections return DynUpdateRcodeError.
func (t *DynUpdate) Cleanup(ctx context.Context, room string) error {
	return t.cleanupKinds(ctx, room, sig.Offer, sig.Answer)
}

func (t *DynUpdate) cleanupKinds(ctx context.Context, room string, kinds ...sig.Kind) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("DynUpdate cleanup: %w", err)
	}
	rrs := make([]dns.RR, 0, len(kinds))
	for _, kind := range kinds {
		owner, err := t.owner(room, kind)
		if err != nil {
			return err
		}
		rrs = append(rrs, &dns.TXT{Hdr: dns.RR_Header{Name: owner, Rrtype: dns.TypeTXT}})
	}
	m := new(dns.Msg)
	m.SetUpdate(dns.Fqdn(canonicalReplyName(t.Zone)))
	m.RemoveRRset(rrs)
	if err := t.checkWireSize("cleanup UPDATE", m); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("DynUpdate cleanup: %w", err)
	}
	reply, err := t.Resolver.Exchange(ctx, m)
	if err != nil {
		return fmt.Errorf("DynUpdate cleanup exchange: %w", err)
	}
	if reply == nil {
		return errors.New("DynUpdate cleanup: nil resolver response")
	}
	if reply.Rcode != dns.RcodeSuccess {
		return &DynUpdateRcodeError{Operation: "cleanup", Rcode: reply.Rcode}
	}
	return nil
}
