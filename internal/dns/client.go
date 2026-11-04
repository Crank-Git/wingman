// Package dns implements the DNS signaling transport for Wingman. It provides
// the WriteOverQuery transport (writes encoded as DNS queries) plus an
// in-process authoritative server used for local runs and tests.
package dns

import (
	"context"
	"time"

	"github.com/miekg/dns"
)

// Resolver exchanges DNS messages with a server.
type Resolver interface {
	Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error)
}

// Dialer is a Resolver that sends queries over UDP/TCP to a server address.
type Dialer struct {
	Addr    string
	Net     string
	Timeout time.Duration
}

func (d *Dialer) withDefaults() *Dialer {
	if d.Addr == "" {
		d.Addr = "127.0.0.1:53"
	}
	if d.Net == "" {
		d.Net = "udp"
	}
	if d.Timeout == 0 {
		d.Timeout = 2 * time.Second
	}
	return d
}

// Exchange sends m to the server over UDP, and falls back to TCP if the UDP
// response is truncated (TC bit). TCP has no size limit, so a poll that returns
// many fragments rides over TCP while ordinary queries stay on UDP.
func (d *Dialer) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	c := d.Client()
	resp, _, err := c.ExchangeContext(ctx, m, d.withDefaults().Addr)
	if err != nil {
		return nil, err
	}
	if resp.Truncated {
		tc := &dns.Client{Net: "tcp", Timeout: d.withDefaults().Timeout}
		resp, _, err = tc.ExchangeContext(ctx, m, d.withDefaults().Addr)
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// Client returns a dns.Client configured with the dialer's settings.
func (d *Dialer) Client() *dns.Client {
	d = d.withDefaults()
	return &dns.Client{Net: d.Net, Timeout: d.Timeout}
}
