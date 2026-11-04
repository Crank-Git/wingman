package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/https"
	"github.com/Crank-Git/wingman/internal/sig"
	mdns "github.com/miekg/dns"
)

const doctorPayload = "wingman diagnostic probe"

type doctorCheck struct {
	Name       string
	Success    bool
	DurationMS int64
	ErrorCode  string
	Correction string
}
type doctorReport struct {
	Transport string
	Checks    []doctorCheck
	Success   bool
}
type doctorOptions struct {
	peerOpts
	timeout    time.Duration
	jsonOutput bool
}
type doctorStage struct {
	name string
	run  func(context.Context) error
}
type doctorPlan struct {
	name          string
	transport     sig.Transport
	stages        []doctorStage
	authenticated bool
	cleanupFailed *atomic.Bool
}

func parseDoctorFlags(args []string) (doctorOptions, error) {
	var opts doctorOptions
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.addr, "addr", "", "selected DNS endpoint")
	fs.StringVar(&opts.zone, "zone", defaultZone, "authoritative DNS zone")
	fs.StringVar(&opts.signaling, "signaling", "dns", "dns, dynupdate, or https")
	fs.StringVar(&opts.dnsTSIGKeyFile, "dns-tsig-key-file", "", "restricted TSIG JSON file")
	fs.StringVar(&opts.signalingURL, "signaling-url", "", "HTTPS endpoint")
	fs.StringVar(&opts.signalingSecret, "signaling-secret", "", "HTTPS secret")
	fs.StringVar(&opts.signalingSecretFile, "signaling-secret-file", "", "restricted HTTPS token file")
	fs.BoolVar(&opts.allowLoopbackHTTP, "signaling-allow-loopback-http", false, "permit loopback HTTP")
	fs.DurationVar(&opts.timeout, "timeout", 5*time.Second, "positive probe deadline; cleanup permits one additional second")
	fs.BoolVar(&opts.jsonOutput, "json", false, "emit version 1 JSON results")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.timeout <= 0 || fs.NArg() != 0 {
		return opts, errors.New("doctor requires a positive --timeout and no positional arguments")
	}
	if opts.signaling != "https" && opts.addr == "" {
		return opts, errors.New("doctor requires --addr for DNS")
	}
	if opts.signaling == "dynupdate" {
		explicit := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "zone" {
				explicit = true
			}
		})
		if !explicit {
			return opts, errors.New("doctor requires explicit --zone for dynupdate")
		}
	}
	return opts, nil
}

func runDoctor(ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(os.Stdout, "doctor mutates only a random disposable probe room. Use --signaling dns|dynupdate|https with its endpoint and credential flags. --timeout defaults to 5s; supported cleanup permits at most one additional second. --json emits version 1 results; exit 0 means every applicable check passes.")
		return err
	}
	opts, err := parseDoctorFlags(args)
	if err != nil {
		return err
	}
	report := executeDoctor(ctx, opts)
	render := writeDoctorReport
	if opts.jsonOutput {
		render = writeDoctorJSON
	}
	if err := render(os.Stdout, report); err != nil {
		return err
	}
	if !report.Success {
		return errors.New("doctor: a requested check failed")
	}
	return nil
}

func executeDoctor(ctx context.Context, opts doctorOptions) doctorReport {
	plan, err := buildDoctorPlan(opts)
	if err != nil {
		return doctorReport{Transport: opts.signaling, Checks: []doctorCheck{{Name: "configuration", ErrorCode: "configuration", Correction: "Check the selected endpoint and credential files."}}}
	}
	return probeDoctor(ctx, plan, opts.timeout, doctorRoom)
}

func doctorRoom() string {
	var random [16]byte
	// crypto/rand.Read terminates the process if the system random source fails.
	_, _ = rand.Read(random[:])
	return "doctor-" + hex.EncodeToString(random[:])
}

func buildDoctorPlan(opts doctorOptions) (doctorPlan, error) {
	tr, err := buildTransport(opts.peerOpts)
	if err != nil {
		return doctorPlan{}, err
	}
	plan := doctorPlan{name: opts.signaling, transport: tr}
	switch typed := tr.(type) {
	case *dns.WriteOverQuery:
		dialer := typed.Resolver.(*dns.Dialer)
		plan.authenticated = dialer.TSIGKey != nil
		for _, network := range []string{"udp", "tcp"} {
			copy := *dialer
			copy.Net = network
			plan.stages = append(plan.stages, doctorStage{"dns_" + network, func(ctx context.Context) error {
				m := new(mdns.Msg)
				m.SetQuestion(mdns.Fqdn(opts.zone), mdns.TypeSOA)
				_, err := copy.Exchange(ctx, m)
				return err
			}})
		}
	case *dns.DynUpdate:
		plan.authenticated = true
		// Doctor reports cleanup failure without the peer-specific stderr output.
		plan.cleanupFailed = &atomic.Bool{}
		typed.OnCleanupFailure = func() { plan.cleanupFailed.Store(true) }
		plan.stages = []doctorStage{{"dns_tcp", func(ctx context.Context) error { return doctorConnect(ctx, opts.addr, false) }}}
	case *https.Transport:
		plan.authenticated = typed.Secret != ""
		u, err := url.Parse(typed.BaseURL)
		if err != nil || u.Hostname() == "" || u.User != nil {
			return doctorPlan{}, errors.New("invalid HTTPS endpoint")
		}
		port := u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		addr := net.JoinHostPort(u.Hostname(), port)
		if u.Scheme == "https" {
			plan.stages = []doctorStage{{"tls", func(ctx context.Context) error { return doctorConnect(ctx, addr, true) }}}
		} else if u.Scheme != "http" || !typed.AllowLoopbackHTTP {
			return doctorPlan{}, errors.New("HTTPS is required")
		}
	}
	return plan, nil
}

func doctorConnect(ctx context.Context, addr string, secure bool) error {
	var conn net.Conn
	var err error
	if secure {
		conn, err = (&tls.Dialer{}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	return conn.Close()
}

func doctorResult(ctx context.Context, name string, start time.Time, err error) doctorCheck {
	check := doctorCheck{Name: name, Success: err == nil, DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		check.ErrorCode = "probe_failed"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			check.ErrorCode = "deadline_exceeded"
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			check.ErrorCode = "canceled"
		}
		check.Correction = "Check the selected endpoint, network access, and configured credentials."
	}
	return check
}

func probeDoctor(parent context.Context, plan doctorPlan, timeout time.Duration, roomName func() string) doctorReport {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	report := doctorReport{Transport: plan.name, Success: true}
	room := roomName()
	for _, stage := range plan.stages {
		start := time.Now()
		err := stage.run(ctx)
		report.Checks = append(report.Checks, doctorResult(ctx, stage.name, start, err))
	}
	start := time.Now()
	err := ctx.Err()
	attempted := err == nil
	if attempted {
		err = plan.transport.Publish(ctx, room, sig.Offer, []byte(doctorPayload))
	}
	if plan.authenticated {
		report.Checks = append(report.Checks, doctorResult(ctx, "auth", start, err))
	}
	report.Checks = append(report.Checks, doctorResult(ctx, "publication", start, err))
	start = time.Now()
	if err == nil {
		var got []byte
		got, err = plan.transport.Poll(ctx, room, sig.Offer)
		if err == nil && !bytes.Equal(got, []byte(doctorPayload)) {
			err = errors.New("probe data mismatch")
		}
	}
	report.Checks = append(report.Checks, doctorResult(ctx, "retrieval", start, err))
	if cleaner, ok := plan.transport.(interface {
		Cleanup(context.Context, string) error
	}); ok && attempted {
		cleanupCtx, stop := context.WithTimeout(context.Background(), time.Second)
		start = time.Now()
		err = cleaner.Cleanup(cleanupCtx, room)
		check := doctorResult(cleanupCtx, "cleanup", start, err)
		if err == nil && plan.cleanupFailed != nil && plan.cleanupFailed.Load() {
			err = errors.New("automatic cleanup failed")
			check = doctorResult(cleanupCtx, "cleanup", start, err)
		}
		stop()
		if err != nil {
			check.Correction = "Run wingman cleanup with the original configuration and --room " + room + "."
		}
		report.Checks = append(report.Checks, check)
	}
	for _, check := range report.Checks {
		report.Success = report.Success && check.Success
	}
	return report
}
