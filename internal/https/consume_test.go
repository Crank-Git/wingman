package https

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
)

type blockedConsumeWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	err     error
}

func (w *blockedConsumeWriter) WriteHeader(code int) {
	close(w.entered)
	<-w.release
	w.ResponseRecorder.WriteHeader(code)
}

func (w *blockedConsumeWriter) Write(body []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return w.ResponseRecorder.Write(body)
}

func TestHandlerConsumesOnceOverTLS(t *testing.T) {
	srv := httptest.NewTLSServer(NewHandler())
	t.Cleanup(srv.Close)
	tr := &Transport{BaseURL: srv.URL, Client: srv.Client(), PollRetries: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Publish(ctx, "consume", sig.Offer, []byte("old")); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			body, err := tr.Poll(ctx, "consume", sig.Offer)
			if err == nil && string(body) != "old" {
				err = errors.New("GET returns a different message")
			}
			results <- err
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		err := <-results
		var miss *PollError
		if err == nil {
			successes++
		} else if !errors.As(err, &miss) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful polls = %d, want 1", successes)
	}
	if err := tr.Publish(ctx, "consume", sig.Offer, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if body, err := tr.Poll(ctx, "consume", sig.Offer); err != nil || string(body) != "new" {
		t.Fatalf("replacement poll = %q, %v", body, err)
	}
}

func startBlockedConsume(t *testing.T, h *Handler, err error) (*blockedConsumeWriter, func()) {
	t.Helper()
	w := &blockedConsumeWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{}), err}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route+"?room=r0&kind=offer", nil))
	}()
	finish := func() {
		close(w.release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("GET does not finish")
		}
	}
	t.Cleanup(func() { finish() })
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("GET does not reach the response write")
	}
	return w, func() {
		// The test can inspect the completed response before cleanup.
		finish()
		finish = func() {}
	}
}

func TestHandlerConsumesBeforeConcurrentGET(t *testing.T) {
	h := NewHandler()
	if code := postCapacity(h, "r0", "old"); code != http.StatusOK {
		t.Fatal(code)
	}
	w, finish := startBlockedConsume(t, h, nil)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, route+"?room=r0&kind=offer", nil))
	finish()
	if w.Code != http.StatusOK || w.Body.String() != "old" || second.Code != http.StatusNotFound {
		t.Fatalf("first = %d %q, second = %d; want 200 old, 404", w.Code, w.Body.String(), second.Code)
	}
	if entries, bytes := retainedCapacity(t, h); entries != 0 || bytes != 0 {
		t.Fatalf("retained = %d entries, %d bytes; want 0, 0", entries, bytes)
	}
}

func TestHandlerPreservesReplacementDuringResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"successful_write", nil}, {"failed_write", io.ErrClosedPipe}} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler()
			if code := postCapacity(h, "r0", "old"); code != http.StatusOK {
				t.Fatal(code)
			}
			w, finish := startBlockedConsume(t, h, tc.err)
			if code := postCapacity(h, "r0", "replacement"); code != http.StatusOK {
				t.Fatal(code)
			}
			finish()
			if tc.err == nil && w.Body.String() != "old" {
				t.Fatalf("old response = %q", w.Body.String())
			}
			if entries, bytes := retainedCapacity(t, h); entries != 1 || bytes != len("replacement") {
				t.Fatalf("retained = %d entries, %d bytes; want 1, 11", entries, bytes)
			}
			got := httptest.NewRecorder()
			h.ServeHTTP(got, httptest.NewRequest(http.MethodGet, route+"?room=r0&kind=offer", nil))
			if got.Code != http.StatusOK || got.Body.String() != "replacement" {
				t.Fatalf("replacement = %d %q", got.Code, got.Body.String())
			}
		})
	}
}

func TestHandlerReleasesCapacityBeforeFailedResponse(t *testing.T) {
	h := NewHandler()
	fillCapacity(t, h, "old")
	_, finish := startBlockedConsume(t, h, io.ErrClosedPipe)
	if code := postCapacity(h, "fresh", "new"); code != http.StatusOK {
		t.Errorf("POST during response = %d, want 200", code)
	}
	finish()
	got := httptest.NewRecorder()
	h.ServeHTTP(got, httptest.NewRequest(http.MethodGet, route+"?room=r0&kind=offer", nil))
	if got.Code != http.StatusNotFound {
		t.Fatalf("GET after failed write = %d, want 404", got.Code)
	}
	if entries, bytes := retainedCapacity(t, h); entries != 1024 || bytes != 3072 {
		t.Fatalf("retained = %d entries, %d bytes; want 1024, 3072", entries, bytes)
	}
}
