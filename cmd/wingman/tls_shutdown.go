package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

func closeHTTPSListener(listener net.Listener, earlier error) error {
	if listener != nil {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return errors.Join(earlier, fmt.Errorf("HTTPS listener close: %w", err))
		}
	}
	return earlier
}

// shutdownHTTPS drains active requests before it joins the serve goroutine.
// The caller supplies the shutdown deadline. Earlier errors retain their causes.
func shutdownHTTPS(ctx context.Context, server *http.Server, served <-chan error, earlier error) error {
	if err := server.Shutdown(ctx); err != nil {
		earlier = errors.Join(earlier, fmt.Errorf("HTTPS shutdown: %w", err))
		if err := server.Close(); err != nil {
			earlier = errors.Join(earlier, fmt.Errorf("HTTPS force close: %w", err))
		}
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		earlier = errors.Join(earlier, fmt.Errorf("HTTPS serve: %w", err))
	}
	return earlier
}
