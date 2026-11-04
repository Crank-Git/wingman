package https

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
)

func TestHandlerBoundsBody(t *testing.T) {
	for _, size := range []int{16384, 16385, 100000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			h := NewHandler()
			h.msgs["r/offer"] = entry{data: []byte("original"), at: time.Now()}
			body := &countBody{Reader: strings.NewReader(strings.Repeat("x", size))}
			req := httptest.NewRequest(http.MethodPost, route+"?room=r&kind=offer", body)
			req.ContentLength = -1
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			want := http.StatusOK
			if size > 16384 {
				want = http.StatusRequestEntityTooLarge
				if string(h.msgs["r/offer"].data) != "original" {
					t.Error("oversized body replaces the stored message")
				}
			} else if len(h.msgs["r/offer"].data) != size {
				t.Error("handler does not retain the accepted body")
			}
			if rec.Code != want || body.read > 16385 {
				t.Fatalf("status = %d, bytes read = %d; want %d, at most 16385", rec.Code, body.read, want)
			}
		})
	}
}

func TestHandlerDoesNotRetainOversizedBody(t *testing.T) {
	h := NewHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route+"?room=r&kind=offer", strings.NewReader(strings.Repeat("x", 16385))))
	if rec.Code != http.StatusRequestEntityTooLarge || len(h.msgs) != 0 {
		t.Fatalf("status = %d, entries = %d; want 413, 0", rec.Code, len(h.msgs))
	}
}

func TestHandlerBodyLimitOverTLS(t *testing.T) {
	h := NewHandler()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	tr := &Transport{BaseURL: srv.URL, Client: srv.Client(), PollRetries: 1, Secret: "secret"}
	h.Secret = tr.Secret
	ctx := context.Background()
	want := strings.Repeat("x", 16384)
	if err := tr.Publish(ctx, "r", sig.Offer, []byte(want)); err != nil {
		t.Fatal(err)
	}
	if err := tr.Publish(ctx, "r", sig.Offer, []byte(want+"x")); err == nil || !strings.Contains(err.Error(), "413") {
		t.Fatalf("oversized publish error = %v, want 413", err)
	}
	got, err := tr.Poll(ctx, "r", sig.Offer)
	if err != nil || string(got) != want {
		t.Fatalf("poll bytes = %d, error = %v; want the original message", len(got), err)
	}
}

type countBody struct {
	io.Reader
	read int
}

func (b *countBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func TestHandlerAuthenticatesBeforeInput(t *testing.T) {
	for _, query := range []string{"?room=r&kind=offer", "?room=&kind=invalid"} {
		h := NewHandler()
		h.Secret = "secret"
		h.msgs["stale/offer"] = entry{at: time.Now().Add(-time.Hour)}
		body := &countBody{Reader: strings.NewReader("body")}
		req := httptest.NewRequest(http.MethodPost, route+query, body)
		req.Header.Set(secretHeader, "wrong")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || body.read != 0 || len(h.msgs) != 1 {
			t.Fatalf("status = %d, bytes read = %d, entries = %d; want 401, 0, 1", rec.Code, body.read, len(h.msgs))
		}
	}
}

func TestHandlerListsAllowedMethods(t *testing.T) {
	h := NewHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, route+"?room=r&kind=offer", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("status = %d, Allow = %q; want 405, GET, POST", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestHandlerRejectsMalformedRooms(t *testing.T) {
	for _, room := range []string{"", "two.labels", `escaped\label`, " leading", "trailing\t", strings.Repeat("x", 64)} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			t.Run(method+"/"+room, func(t *testing.T) {
				h := NewHandler()
				body := &countBody{Reader: strings.NewReader("body")}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, route+"?room="+url.QueryEscape(room)+"&kind=offer", body))
				if rec.Code != http.StatusBadRequest || len(h.msgs) != 0 || body.read != 0 {
					t.Fatalf("status = %d, entries = %d, bytes read = %d; want 400, 0, 0", rec.Code, len(h.msgs), body.read)
				}
			})
		}
	}
}

func TestHandlerAcceptsStructuralRoomLabels(t *testing.T) {
	for _, room := range []string{"under_score", "-edge-", strings.Repeat("x", 63), "MixedCase"} {
		h := NewHandler()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, route+"?room="+url.QueryEscape(room)+"&kind=offer", strings.NewReader("body")))
		if rec.Code != http.StatusOK || string(h.msgs[room+"/offer"].data) != "body" {
			t.Fatalf("room %q: status = %d, entries = %v", room, rec.Code, h.msgs)
		}
	}
}
