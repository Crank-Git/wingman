package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Crank-Git/wingman/internal/dns"
)

func buildDynUpdate(opts peerOpts) (*dns.DynUpdate, error) {
	key, err := loadOptionalTSIG(opts.dnsTSIGKeyFile)
	if err != nil {
		return nil, err
	}
	tr, err := dns.NewDynUpdate(opts.addr, opts.zone, key)
	if err != nil {
		return nil, err
	}
	tr.OnCleanupFailure = func() {
		// A diagnostic write failure must not discard the validated Poll result.
		_, _ = fmt.Fprintln(os.Stderr, "wingman: automatic DynUpdate cleanup failed; run wingman cleanup for this room.")
	}
	return tr, nil
}

func runCleanup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var opts peerOpts
	fs.StringVar(&opts.addr, "addr", "", "authoritative DNS server address")
	fs.StringVar(&opts.zone, "zone", "", "explicit authoritative DNS zone")
	fs.StringVar(&opts.room, "room", "", "room to clean")
	fs.StringVar(&opts.dnsTSIGKeyFile, "dns-tsig-key-file", "", "restricted TSIG JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("cleanup: unexpected positional arguments")
	}
	for _, value := range []string{opts.addr, opts.zone, opts.room, opts.dnsTSIGKeyFile} {
		if strings.TrimSpace(value) == "" {
			return errors.New("cleanup requires --addr, explicit --zone, --room, and --dns-tsig-key-file")
		}
	}
	tr, err := buildDynUpdate(opts)
	if err != nil {
		return err
	}
	if err := tr.Cleanup(ctx, opts.room); err != nil {
		return err
	}
	// The confirmation is best effort after the authoritative UPDATE succeeds.
	_, _ = fmt.Fprintln(os.Stdout, "DynUpdate cleanup complete.")
	return nil
}
