//go:build namespace

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
	"github.com/miekg/dns"
)

const keyName = "fixture.example."
const secret = "bmFtZXNwYWNlLWZpeHR1cmU="

func exchange(network, name, auth string, code int, payload string) error {
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
	c := &dns.Client{Net: network, Timeout: 2 * time.Second}
	if auth != "" {
		q.SetTsig(keyName, dns.HmacSHA256, 300, time.Now().Unix())
		c.TsigSecret = map[string]string{keyName: auth}
	}
	r, _, err := c.Exchange(q, "10.203.0.1:5353")
	if r == nil || r.Rcode != code {
		return fmt.Errorf("%s request: expected rcode %d, response %v, error %v", network, code, r != nil, err)
	}
	if code == dns.RcodeNotAuth {
		if len(r.Answer) != 0 {
			return fmt.Errorf("rejected request returns data")
		}
		return nil
	}
	if code != dns.RcodeSuccess && len(r.Answer) != 0 {
		return fmt.Errorf("rejected request returns data")
	}
	if err != nil {
		return fmt.Errorf("verify response: %w", err)
	}
	if auth != "" && r.IsTsig() == nil {
		return fmt.Errorf("response lacks TSIG")
	}
	if payload != "" {
		if len(r.Answer) != 1 {
			return fmt.Errorf("response lacks payload")
		}
		txt, ok := r.Answer[0].(*dns.TXT)
		if !ok || len(txt.Txt) != 1 || txt.Txt[0] != payload {
			return fmt.Errorf("response payload differs")
		}
	}
	return nil
}

func scenario(network, phase string) error {
	room := "namespace-" + network
	encoded, err := sig.EncodeLabel([]byte("fixture-message"))
	if err != nil {
		return err
	}
	write := func(kind sig.Kind) string { return sig.RendezvousName("example.com", room, kind, 0, 1, encoded) }
	poll := func(kind sig.Kind) string { return sig.PollName("example.com", room, kind) }
	switch phase {
	case "offer":
		return exchange(network, write(sig.Offer), secret, dns.RcodeSuccess, "ok")
	case "answer":
		if err := exchange(network, poll(sig.Offer), secret, dns.RcodeSuccess, write(sig.Offer)); err != nil {
			return err
		}
		return exchange(network, write(sig.Answer), secret, dns.RcodeSuccess, "ok")
	case "confirm":
		if err := exchange(network, poll(sig.Answer), secret, dns.RcodeSuccess, write(sig.Answer)); err != nil {
			return err
		}
		for _, auth := range []string{"", "d3Jvbmcta2V5"} {
			for _, name := range []string{poll(sig.Offer), write(sig.Offer)} {
				if err := exchange(network, name, auth, dns.RcodeNotAuth, ""); err != nil {
					return err
				}
			}
		}
		for _, name := range []string{sig.PollName("other.test", room, sig.Offer), sig.RendezvousName("other.test", room, sig.Offer, 0, 1, encoded)} {
			if err := exchange(network, name, secret, dns.RcodeRefused, ""); err != nil {
				return err
			}
		}
		return exchange(network, poll(sig.Offer), secret, dns.RcodeSuccess, write(sig.Offer))
	default:
		return fmt.Errorf("unknown probe phase")
	}
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: probe udp|tcp offer|answer|confirm")
		os.Exit(1)
	}
	if err := scenario(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("%s %s: passes\n", os.Args[1], os.Args[2])
}
