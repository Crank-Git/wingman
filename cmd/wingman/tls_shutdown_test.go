package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestShutdownHTTPSDrainsActiveRequest(t *testing.T) {
	cert, key, roots := cliCertificate(t)
	certificate, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, stopping := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := &http.Server{TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; fmt.Fprint(w, "complete") })}
	server.RegisterOnShutdown(func() { close(stopping) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.ServeTLS(listener, "", "") }()
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		resp, err := client.Get("https://" + listener.Addr().String())
		if err == nil {
			defer resp.Body.Close()
			var body []byte
			body, err = io.ReadAll(resp.Body)
			if err == nil && string(body) != "complete" {
				err = fmt.Errorf("body: %q", body)
			}
		}
		response <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- shutdownHTTPS(ctx, server, served, nil) }()
	select {
	case <-stopping:
	case err := <-done:
		close(release)
		<-response
		t.Fatalf("cleanup returns before request drain: %v", err)
	case <-ctx.Done():
		close(release)
		t.Fatal(ctx.Err())
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type closeErrorListener struct {
	net.Listener
	cause error
}

func (l closeErrorListener) Close() error { return errors.Join(l.Listener.Close(), l.cause) }

func TestShutdownHTTPSPreservesCleanupAndEarlierCauses(t *testing.T) {
	cause := errors.New("listener close fails")
	prior := errors.New("earlier serve failure")
	for _, earlier := range []error{nil, prior} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan struct{})
		server := &http.Server{BaseContext: func(net.Listener) context.Context { close(started); return context.Background() }}
		served := make(chan error, 1)
		go func() { served <- server.Serve(closeErrorListener{listener, cause}) }()
		<-started
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = shutdownHTTPS(ctx, server, served, earlier)
		cancel()
		if !errors.Is(err, cause) || earlier != nil && !errors.Is(err, earlier) {
			t.Fatalf("lost cause: %v", err)
		}
	}
}

func TestShutdownHTTPSForcesCloseAfterDeadline(t *testing.T) {
	entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; close(exited) })}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	response := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
		response <- err
	}()
	<-entered
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err = shutdownHTTPS(ctx, server, served, nil)
	close(release)
	<-exited
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost shutdown cause: %v", err)
	}
	if err := <-response; err == nil {
		t.Fatal("active connection stays open")
	}
}

func TestShutdownHTTPSReturnsServeCause(t *testing.T) {
	cause := errors.New("serve fails")
	served := make(chan error, 1)
	served <- cause
	if err := shutdownHTTPS(context.Background(), &http.Server{}, served, nil); !errors.Is(err, cause) {
		t.Fatalf("lost serve cause: %v", err)
	}
}

func TestCloseHTTPSListenerPreservesCauses(t *testing.T) {
	cause, prior := errors.New("listener close fails"), errors.New("startup fails")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	err = closeHTTPSListener(closeErrorListener{listener, cause}, prior)
	if !errors.Is(err, cause) || !errors.Is(err, prior) {
		t.Fatalf("lost cause: %v", err)
	}
	if err := closeHTTPSListener(listener, nil); err != nil {
		t.Fatal(err)
	}
	if err := closeHTTPSListener(nil, prior); err != prior {
		t.Fatal(err)
	}
}
