package https

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
)

func postCapacity(h *Handler, room, body string) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, route+"?room="+room+"&kind=offer", strings.NewReader(body)))
	return w.Code
}

func retainedCapacity(t *testing.T, h *Handler) (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bytes := 0
	for _, e := range h.msgs {
		bytes += len(e.data)
	}
	if h.payloadBytes != bytes {
		t.Fatalf("accounted bytes = %d, retained bytes = %d", h.payloadBytes, bytes)
	}
	return len(h.msgs), bytes
}

func fillCapacity(t *testing.T, h *Handler, body string) {
	t.Helper()
	for i := 0; i < 1024; i++ {
		if code := postCapacity(h, fmt.Sprintf("r%d", i), body); code != http.StatusOK {
			t.Fatalf("entry %d: status = %d, want 200", i, code)
		}
	}
}

func TestHandlerRejectsEntryCapacity(t *testing.T) {
	h := NewHandler()
	fillCapacity(t, h, "")
	for i := 0; i < 3; i++ {
		if code := postCapacity(h, "excess", "x"); code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", code)
		}
	}
	if entries, bytes := retainedCapacity(t, h); entries != 1024 || bytes != 0 {
		t.Fatalf("retained = %d entries, %d bytes; want 1024, 0", entries, bytes)
	}
}

func TestHandlerReplacesAtPayloadCapacity(t *testing.T) {
	h := NewHandler()
	body := strings.Repeat("x", 16384)
	fillCapacity(t, h, body)
	for _, replacement := range []string{body, "x", body} {
		if code := postCapacity(h, "r0", replacement); code != http.StatusOK {
			t.Fatalf("replacement status = %d, want 200", code)
		}
		if entries, bytes := retainedCapacity(t, h); entries != 1024 || bytes != 1023*16384+len(replacement) {
			t.Fatalf("retained = %d entries, %d bytes", entries, bytes)
		}
	}
	if code := postCapacity(h, "excess", body); code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
}

func TestHandlerReleasesExpiredCapacity(t *testing.T) {
	h := NewHandler()
	fillCapacity(t, h, strings.Repeat("x", 16384))
	h.mu.Lock()
	for key, e := range h.msgs {
		e.at = time.Now().Add(-time.Hour)
		h.msgs[key] = e
	}
	h.mu.Unlock()
	if code := postCapacity(h, "fresh", "new"); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if entries, bytes := retainedCapacity(t, h); entries != 1 || bytes != 3 {
		t.Fatalf("retained = %d entries, %d bytes; want 1, 3", entries, bytes)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, route+"?room=fresh&kind=offer", nil))
	fillCapacity(t, h, strings.Repeat("x", 16384))
}

func TestHandlerBoundsConcurrentWriters(t *testing.T) {
	h := NewHandler()
	var wg sync.WaitGroup
	codes := make(chan int, 1056)
	for i := 0; i < cap(codes); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- postCapacity(h, fmt.Sprintf("r%d", i), strings.Repeat("x", 16384))
		}(i)
	}
	wg.Wait()
	close(codes)
	accepted := 0
	for code := range codes {
		if code == http.StatusOK {
			accepted++
		} else if code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 200 or 503", code)
		}
	}
	if entries, bytes := retainedCapacity(t, h); accepted != 1024 || entries != 1024 || bytes != 16*1024*1024 {
		t.Fatalf("accepted = %d, retained = %d entries, %d bytes", accepted, entries, bytes)
	}
}

func TestHandlerCapacityOverTLS(t *testing.T) {
	h := NewHandler()
	fillCapacity(t, h, "x")
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	tr := &Transport{BaseURL: srv.URL, Client: srv.Client(), PollRetries: 1}
	ctx := context.Background()
	if err := tr.Publish(ctx, "excess", sig.Offer, []byte("new")); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("publish error = %v, want 503", err)
	}
	if _, err := tr.Poll(ctx, "r0", sig.Offer); err != nil {
		t.Fatal(err)
	}
	if err := tr.Publish(ctx, "excess", sig.Offer, []byte("new")); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerRejectsPayloadCapacity(t *testing.T) {
	h := NewHandler()
	// Exercise the byte limit independently from the entry and request limits.
	if !h.store("large/offer", make([]byte, 16*1024*1024-1)) || !h.store("small/offer", []byte("x")) {
		t.Fatal("store rejects payloads within the limit")
	}
	if h.store("small/offer", []byte("xx")) || h.store("excess/offer", []byte("x")) {
		t.Fatal("store accepts payloads above the limit")
	}
	if entries, bytes := retainedCapacity(t, h); entries != 2 || bytes != 16*1024*1024 || h.payloadBytes != bytes {
		t.Fatalf("retained = %d entries, %d bytes; accounted = %d", entries, bytes, h.payloadBytes)
	}
}
