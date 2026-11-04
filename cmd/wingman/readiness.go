package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Crank-Git/wingman/internal/dns"
	"github.com/Crank-Git/wingman/internal/https"
	"github.com/Crank-Git/wingman/internal/metrics"
	"github.com/Crank-Git/wingman/internal/turnserver"
)

type serveConfig struct {
	zone, listen, metricsAddr string
	key                       *dns.TSIGKey
	https                     https.ServerConfig
	turn                      turnserver.Config
}

type readiness struct {
	// Atomic access lets HTTP handlers observe the lifecycle owner's state.
	started atomic.Bool
	ready   atomic.Bool
	ctx     context.Context
}

func (s *readiness) handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", next)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !s.started.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() || s.ctx.Err() != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func serveListeners(ctx context.Context, cfg serveConfig, out io.Writer) (result error) {
	state := &readiness{ctx: ctx}
	// DNS owns an internal cancellation loop. Keep shutdown under this owner.
	srv := dns.NewServer()
	srv.ListenAddr, srv.Zone, srv.TSIGKey = cfg.listen, cfg.zone, cfg.key
	if err := srv.Start(context.Background()); err != nil {
		return err
	}
	defer srv.Stop()
	tlsServer, tlsListener, err := https.BindTLS(cfg.https)
	if err != nil {
		return err
	}
	defer func() { result = closeHTTPSListener(tlsListener, result) }()
	var metricsListener net.Listener
	if cfg.metricsAddr != "" {
		metricsListener, err = net.Listen("tcp", cfg.metricsAddr)
		if err != nil {
			return fmt.Errorf("metrics listen: %w", err)
		}
		defer func() { result = closeHTTPSListener(metricsListener, result) }()
	}
	var turnOutput bytes.Buffer
	turnServer, err := startTurnServer(&turnOutput, cfg.turn)
	if err != nil {
		return err
	}
	if turnServer != nil {
		defer func() {
			if err := turnServer.Close(); err != nil {
				result = errors.Join(result, fmt.Errorf("TURN close: %w", err))
			}
		}()
	}
	tlsResult := make(chan error, 1)
	metricsResult := make(chan error, 1)
	if tlsServer != nil {
		tlsServer.Handler = state.handler(tlsServer.Handler)
		go func() { tlsResult <- tlsServer.ServeTLS(tlsListener, "", "") }()
		defer func() { result = drainHTTP(tlsServer, tlsResult, result) }()
	}
	if metricsListener != nil {
		server := &http.Server{Handler: state.handler(metrics.New().MetricsHandler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
		go func() { metricsResult <- server.Serve(metricsListener) }()
		defer func() { result = drainHTTP(server, metricsResult, result) }()
	}
	defer state.ready.Store(false)
	state.started.Store(true)
	state.ready.Store(true)
	fmt.Fprintf(out, "wingman server listening on %s (zone %q)\n", srv.Addr(), cfg.zone)
	if tlsListener != nil {
		fmt.Fprintf(out, "HTTPS listening on https://%s\n", tlsListener.Addr())
	}
	if metricsListener != nil {
		fmt.Fprintf(out, "metrics listening on %s/metrics\n", metricsListener.Addr())
	}
	fmt.Fprint(out, turnOutput.String())
	select {
	case <-ctx.Done():
	case err := <-tlsResult:
		tlsResult <- err
	case err := <-metricsResult:
		metricsResult <- err
	}
	return nil
}

func drainHTTP(server *http.Server, served <-chan error, earlier error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return shutdownHTTPS(ctx, server, served, earlier)
}
