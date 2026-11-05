package dns

import (
	"context"
	"errors"
	"fmt"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

// Publish replaces the complete TXT RRset in one authenticated TCP UPDATE.
// It rejects invalid configuration and bounded-size failures before network access.
func (t *DynUpdate) Publish(ctx context.Context, room string, kind sig.Kind, msg []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("DynUpdate publish: %w", err)
	}
	owner, err := t.owner(room, kind)
	if err != nil {
		return err
	}
	if len(msg) > maxMessageBytes {
		return &DynUpdateSizeError{Operation: "message", Size: len(msg), Limit: maxMessageBytes}
	}
	rrs, err := dynUpdateRecords(owner, msg)
	if err != nil {
		return fmt.Errorf("DynUpdate publish fragments: %w", err)
	}
	m := new(dns.Msg)
	m.SetUpdate(dns.Fqdn(canonicalReplyName(t.Zone)))
	m.RemoveRRset([]dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: owner, Rrtype: dns.TypeTXT}}})
	m.Insert(rrs)
	if err := t.checkWireSize("UPDATE", m); err != nil {
		return err
	}
	query := new(dns.Msg)
	query.SetQuestion(owner, dns.TypeTXT)
	answer := new(dns.Msg)
	answer.SetReply(query)
	answer.Answer = rrs
	// DynUpdate uses TCP without OPT, so Poll has the same response shape.
	if err := t.checkWireSize("RRset response", answer); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("DynUpdate publish: %w", err)
	}
	reply, err := t.Resolver.Exchange(ctx, m)
	if err != nil {
		return dynUpdateExchangeError("publish", err)
	}
	if reply == nil {
		return errors.New("DynUpdate publish: nil resolver response")
	}
	if reply.Rcode != dns.RcodeSuccess {
		return &DynUpdateRcodeError{Operation: "publish", Rcode: reply.Rcode}
	}
	return nil
}

func dynUpdateRecords(owner string, msg []byte) ([]dns.RR, error) {
	fragments, err := sig.Fragments(msg, 0)
	if err != nil {
		return nil, err
	}
	rrs := make([]dns.RR, 0, len(fragments))
	for _, f := range fragments {
		payload, err := sig.EncodeLabel(f.Data)
		if err != nil {
			return nil, err
		}
		rrs = append(rrs, &dns.TXT{Hdr: dns.RR_Header{Name: owner, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 1}, Txt: []string{fmt.Sprintf("%d-%d:%s", f.Seq, f.Total, payload)}})
	}
	return rrs, nil
}
