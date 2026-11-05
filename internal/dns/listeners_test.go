package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
)

func TestServerRetriesTCPPortCollision(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	abandoned, err := net.ListenPacket("udp", occupied.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = abandoned.Close() })
	s := NewServer()
	attempts := 0
	s.listenPacket = func(network, address string) (net.PacketConn, error) {
		attempts++
		if attempts == 1 {
			return abandoned, nil
		}
		assertPacketClosed(t, abandoned)
		return net.ListenPacket(network, address)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)
	if attempts < 2 || s.Addr() == occupied.Addr().String() {
		t.Fatalf("attempts = %d, address = %s", attempts, s.Addr())
	}
	for _, network := range []string{"udp", "tcp"} {
		query := new(mdns.Msg)
		query.SetQuestion("room.offer.sig.example.com.", mdns.TypeTXT)
		client := &mdns.Client{Net: network, Timeout: time.Second}
		if _, _, err := client.Exchange(query, s.Addr()); err != nil {
			t.Fatalf("%s query: %v", network, err)
		}
	}
	assertPacketClosed(t, abandoned)
}

func TestServerBoundsListenerRetries(t *testing.T) {
	s := NewServer()
	var packets []net.PacketConn
	s.listenPacket = func(network, address string) (net.PacketConn, error) {
		for _, pc := range packets {
			assertPacketClosed(t, pc)
		}
		pc, err := net.ListenPacket(network, address)
		if err == nil {
			packets = append(packets, pc)
			t.Cleanup(func() { _ = pc.Close() })
		}
		return pc, err
	}
	var final error
	s.listenTCP = func(_, _ string) (net.Listener, error) {
		final = fmt.Errorf("attempt %d: %w", len(packets), syscall.EADDRINUSE)
		return nil, final
	}
	err := s.Start(context.Background())
	if len(packets) != listenerPairAttempts {
		t.Fatalf("attempts = %d, want %d", len(packets), listenerPairAttempts)
	}
	if !errors.Is(err, final) || !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Start error = %v, want final cause %v", err, final)
	}
	for _, pc := range packets {
		assertPacketClosed(t, pc)
	}
}

func TestServerReturnsOtherBindFailures(t *testing.T) {
	for _, bindNetwork := range []string{"udp", "tcp"} {
		t.Run(bindNetwork, func(t *testing.T) {
			s := NewServer()
			cause := fmt.Errorf("bind denied: %w", syscall.EACCES)
			attempts := 0
			var packet net.PacketConn
			s.listenPacket = func(network, address string) (net.PacketConn, error) {
				attempts++
				if bindNetwork == "udp" {
					return nil, cause
				}
				var err error
				packet, err = net.ListenPacket(network, address)
				return packet, err
			}
			s.listenTCP = func(_, _ string) (net.Listener, error) { return nil, cause }
			err := s.Start(context.Background())
			if !errors.Is(err, cause) || attempts != 1 {
				t.Fatalf("Start error = %v, attempts = %d", err, attempts)
			}
			if packet != nil {
				defer packet.Close()
				assertPacketClosed(t, packet)
			}
		})
	}
}

func TestServerStopsAfterListenerCleanupFailure(t *testing.T) {
	for _, bindCause := range []error{syscall.EADDRINUSE, syscall.EACCES} {
		t.Run(bindCause.Error(), func(t *testing.T) {
			pc, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pc.Close() })
			closeCause := errors.New("UDP close fails")
			packet := &closeFailurePacket{PacketConn: pc, cause: closeCause}
			s := NewServer()
			attempts := 0
			s.listenPacket = func(_, _ string) (net.PacketConn, error) {
				attempts++
				return packet, nil
			}
			s.listenTCP = func(_, _ string) (net.Listener, error) {
				return nil, fmt.Errorf("TCP bind fails: %w", bindCause)
			}
			err = s.Start(context.Background())
			if attempts != 1 || packet.closes != 1 {
				t.Errorf("allocation attempts = %d, close attempts = %d, want one each", attempts, packet.closes)
			}
			if !errors.Is(err, closeCause) || !errors.Is(err, bindCause) {
				t.Errorf("Start error = %v, want cleanup and bind causes", err)
			}
		})
	}
}

type closeFailurePacket struct {
	net.PacketConn
	cause  error
	closes int
}

func (pc *closeFailurePacket) Close() error {
	pc.closes++
	return pc.cause
}

func TestServerReleasesListenerPair(t *testing.T) {
	for _, shutdown := range []string{"Stop", "cancel"} {
		t.Run(shutdown, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := NewServer()
			if err := s.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Stop)
			if s.dns.PacketConn.LocalAddr().String() != s.Addr() || s.tcp.Listener.Addr().String() != s.Addr() {
				t.Fatal("listeners use different addresses")
			}
			if shutdown == "Stop" {
				s.Stop()
			} else {
				cancel()
				select {
				case <-s.done:
				case <-time.After(time.Second):
					t.Fatal("cancellation retains listeners")
				}
			}
			pc, err := net.ListenPacket("udp", s.Addr())
			if err != nil {
				t.Fatalf("rebind UDP: %v", err)
			}
			defer pc.Close()
			ln, err := net.Listen("tcp", s.Addr())
			if err != nil {
				t.Fatalf("rebind TCP: %v", err)
			}
			defer ln.Close()
		})
	}
}

func assertPacketClosed(t *testing.T, pc net.PacketConn) {
	t.Helper()
	if _, err := pc.WriteTo([]byte("closed"), pc.LocalAddr()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("abandoned socket error = %v, want net.ErrClosed", err)
	}
}
