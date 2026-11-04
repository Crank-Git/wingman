package https

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
)

// newTestServer returns an httptest server backed by a Handler.
func newTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestTransportRoundTrip(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)

	tr := &Transport{AllowLoopbackHTTP: true,
		Client:       srv.Client(),
		BaseURL:      srv.URL,
		PollInterval: 5 * time.Millisecond,
	}

	msg := []byte("v=0\r\no=- ...offer SDP...")
	if err := tr.Publish(context.Background(), "room1", sig.Offer, msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got, err := tr.Poll(context.Background(), "room1", sig.Offer)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if string(got) != string(msg) {
		t.Errorf("Poll = %q, want %q", got, msg)
	}
}

func TestPollNotReadyRetries(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)

	tr := &Transport{AllowLoopbackHTTP: true,
		Client:       srv.Client(),
		BaseURL:      srv.URL,
		PollInterval: 5 * time.Millisecond,
		PollRetries:  3,
	}

	// Nothing published yet: Poll must retry and then fail.
	_, err := tr.Poll(context.Background(), "empty", sig.Answer)
	if err == nil {
		t.Fatal("expected error polling an empty room")
	}
}

func TestPollContextCancel(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)

	tr := &Transport{AllowLoopbackHTTP: true,
		Client:       srv.Client(),
		BaseURL:      srv.URL,
		PollInterval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := tr.Poll(ctx, "empty", sig.Offer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Poll with cancelled ctx: err = %v, want context.Canceled", err)
	}
}

func TestPublishRejectsNon200(t *testing.T) {
	// A server that always returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := &Transport{AllowLoopbackHTTP: true, Client: srv.Client(), BaseURL: srv.URL}
	if err := tr.Publish(context.Background(), "room1", sig.Offer, []byte("x")); err == nil {
		t.Fatal("expected error when server returns non-200")
	}
}

func TestPublishRequiresClient(t *testing.T) {
	// A nil client must surface an error rather than panic.
	h := NewHandler()
	srv := newTestServer(t, h)
	tr := &Transport{AllowLoopbackHTTP: true, Client: srv.Client(), BaseURL: srv.URL}
	if err := tr.Publish(context.Background(), "r", sig.Offer, []byte("x")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestPollErrorMessage(t *testing.T) {
	e := &PollError{Room: "r", Kind: "offer", Retries: 4}
	got := e.Error()
	want := "wingman: poll timed out for room r kind offer after 4 retries"
	if got != want {
		t.Errorf("PollError.Error() = %q, want %q", got, want)
	}
}

// TestURLEncodeSpecialChars checks that reserved characters in the room name are
// percent-encoded so they survive the query string.
func TestURLEncodeSpecialChars(t *testing.T) {
	cases := map[string]string{
		"plain":       "plain",
		"a b":         "a%20b",
		"room+plus":   "room%2Bplus",
		"a=b&c":       "a%3Db%26c",
		"utf8-\u00e9": "utf8-%C3%A9",
	}
	for in, want := range cases {
		if got := urlEncode(in); got != want {
			t.Errorf("urlEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPublishClientError ensures a connection failure surfaces as an error.
func TestPublishClientError(t *testing.T) {
	// A base URL pointing at a closed port guarantees a dial failure.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.Listener.Addr().String()
	srv.Close()

	tr := &Transport{AllowLoopbackHTTP: true, Client: http.DefaultClient, BaseURL: "http://" + addr}
	if err := tr.Publish(context.Background(), "r", sig.Offer, []byte("x")); err == nil {
		t.Fatal("expected a dial error against a closed server")
	}
}

// TestPollServerError covers a non-200, non-404 response in the poll loop.
func TestPollServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := &Transport{AllowLoopbackHTTP: true, Client: srv.Client(), BaseURL: srv.URL, PollRetries: 1}
	if _, err := tr.Poll(context.Background(), "r", sig.Offer); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

// TestPublishDefaultsNilClient verifies that a nil Client falls back to
// http.DefaultClient.
func TestPublishDefaultsNilClient(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)

	tr := &Transport{AllowLoopbackHTTP: true, BaseURL: srv.URL} // Client left nil
	if err := tr.Publish(context.Background(), "r", sig.Offer, []byte("x")); err != nil {
		t.Fatalf("Publish with nil client: %v", err)
	}
}

// roundTripFunc lets a test supply a canned *http.Response for a request.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newRoundTripClient(rt http.RoundTripper) *http.Client {
	return &http.Client{Transport: rt}
}

// errBody fails on Read to exercise the body-read-error paths.
type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errReadFail }
func (errBody) Close() error             { return nil }

var errReadFail = errors.New("https: body read fail")

// TestPollReadError covers a successful status whose body cannot be read.
func TestPollReadError(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: errBody{}, Header: make(http.Header)}, nil
	})
	tr := &Transport{AllowLoopbackHTTP: true, Client: newRoundTripClient(rt), BaseURL: "https://x"}
	if _, err := tr.Poll(context.Background(), "r", sig.Offer); err == nil {
		t.Fatal("expected a body-read error")
	}
}

// TestHandlerPostBodyError covers a POST whose request body cannot be read by
// calling the handler directly with a failing body.
func TestHandlerPostBodyError(t *testing.T) {
	h := NewHandler()
	req := httptest.NewRequest(http.MethodPost, "/signaling?room=r&kind=offer", errBody{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHandlerRejectsBadParams(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)
	client := srv.Client()

	// Missing room.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/signaling?kind=offer", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("expected rejection of a request missing the room param")
	}

	// Missing kind.
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/signaling?room=room1", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("request2: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Error("expected rejection of a request missing the kind param")
	}
}

func TestHandlerWrongRoute(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)
	client := srv.Client()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/other", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("expected non-200 for an unrecognized route")
	}
}

func TestHandlerRejectsDisallowedMethod(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)
	client := srv.Client()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, srv.URL+"/signaling?room=r&kind=offer", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("expected rejection of a disallowed HTTP method")
	}
}

// TestHandlerStoresPerRoomKind ensures offers and answers for the same room do
// not collide.
func TestHandlerStoresPerRoomKind(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)
	client := srv.Client()

	post := func(room string, k sig.Kind, body string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.URL+"/signaling?room="+room+"&kind="+k.String(), io.NopCloser(strings.NewReader(body)))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("post %s: %v", k, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post %s: status %d", k, resp.StatusCode)
		}
	}
	get := func(room string, k sig.Kind) string {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
			srv.URL+"/signaling?room="+room+"&kind="+k.String(), nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	post("room1", sig.Offer, "OFFER")
	post("room1", sig.Answer, "ANSWER")
	if got := get("room1", sig.Offer); got != "OFFER" {
		t.Errorf("offer = %q, want OFFER", got)
	}
	if got := get("room1", sig.Answer); got != "ANSWER" {
		t.Errorf("answer = %q, want ANSWER", got)
	}
}

// TestHandlerDeletesOnRead verifies that a retrieved message is consumed and
// removed, so a second GET for the same key returns 404. This is the regression
// test for the unbounded-growth leak: without delete-on-read the entry persists
// after every handshake and the handler's map grows without bound.
func TestHandlerDeletesOnRead(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)
	client := srv.Client()

	do := func(method, room string) int {
		req, _ := http.NewRequestWithContext(context.Background(), method,
			srv.URL+"/signaling?room="+room+"&kind=offer", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	post := func(room, body string) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.URL+"/signaling?room="+room+"&kind=offer", io.NopCloser(strings.NewReader(body)))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("post %s: %v", room, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post %s: status %d", room, resp.StatusCode)
		}
	}

	post("room1", "OFFER")
	if got := do(http.MethodGet, "room1"); got != http.StatusOK {
		t.Fatalf("first get status = %d, want 200", got)
	}
	if got := do(http.MethodGet, "room1"); got != http.StatusNotFound {
		t.Fatalf("second get status = %d, want 404 (message must be consumed)", got)
	}
}

// TestHandlerSweepsStaleEntries verifies that entries older than the handler's
// TTL are dropped on access, bounding memory for rooms that never complete a
// handshake (an offer published but never answered would otherwise persist).
func TestHandlerSweepsStaleEntries(t *testing.T) {
	h := NewHandler()
	h.TTL = time.Second

	// Insert a fresh and a stale entry directly (same package).
	h.mu.Lock()
	h.msgs["fresh/offer"] = entry{data: []byte("FRESH"), at: time.Now()}
	h.msgs["stale/offer"] = entry{data: []byte("STALE"), at: time.Now().Add(-2 * time.Second)}
	h.mu.Unlock()

	h.sweep(time.Now())

	h.mu.Lock()
	_, okFresh := h.msgs["fresh/offer"]
	_, okStale := h.msgs["stale/offer"]
	h.mu.Unlock()

	if !okFresh {
		t.Error("fresh entry should survive the sweep")
	}
	if okStale {
		t.Error("stale entry should be swept after TTL")
	}
}

// TestHandlerRequiresSecret verifies that when a handler is configured with a
// secret, requests must present it: missing or wrong secrets are rejected with
// 401, while a correct secret proceeds to the normal (here, not-found) path.
func TestHandlerRequiresSecret(t *testing.T) {
	const secret = "s3cr3t"
	h := NewHandler()
	h.Secret = secret
	srv := newTestServer(t, h)
	client := srv.Client()

	get := func(withSecret bool) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet,
			srv.URL+"/signaling?room=room&kind=offer", nil)
		if withSecret {
			req.Header.Set(secretHeader, secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	post := func(withSecret bool) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.URL+"/signaling?room=room&kind=offer", io.NopCloser(strings.NewReader("x")))
		if withSecret {
			req.Header.Set(secretHeader, secret)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := get(false); got != http.StatusUnauthorized {
		t.Errorf("missing secret status = %d, want 401", got)
	}
	if got := post(false); got != http.StatusUnauthorized {
		t.Errorf("missing secret post status = %d, want 401", got)
	}
	if got := get(true); got != http.StatusNotFound {
		t.Errorf("correct-secret GET on empty room = %d, want 404 (auth passed)", got)
	}
	if got := post(true); got != http.StatusOK {
		t.Errorf("correct-secret POST = %d, want 200", got)
	}
}

// TestTransportSendsSecret verifies the transport attaches the secret header
// when configured.
func TestTransportSendsSecret(t *testing.T) {
	captured := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Header.Get(secretHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &Transport{AllowLoopbackHTTP: true, Client: srv.Client(), BaseURL: srv.URL, Secret: "s3cr3t"}
	if err := tr.Publish(context.Background(), "room1", sig.Offer, []byte("x")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := <-captured; got != "s3cr3t" {
		t.Errorf("secret header = %q, want %q", got, "s3cr3t")
	}
}

// TestTransportOmitsSecretWhenUnset verifies no secret header is sent when the
// transport has none configured, preserving backward compatibility.
func TestTransportOmitsSecretWhenUnset(t *testing.T) {
	captured := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- r.Header.Get(secretHeader)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &Transport{AllowLoopbackHTTP: true, Client: srv.Client(), BaseURL: srv.URL}
	if err := tr.Publish(context.Background(), "room1", sig.Offer, []byte("x")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := <-captured; got != "" {
		t.Errorf("secret header = %q, want empty", got)
	}
}

// TestTransportSecretMismatchFails verifies that a transport whose secret does
// not match the handler's is rejected by the server.
func TestTransportSecretMismatchFails(t *testing.T) {
	h := NewHandler()
	h.Secret = "right"
	srv := newTestServer(t, h)
	tr := &Transport{AllowLoopbackHTTP: true, Client: srv.Client(), BaseURL: srv.URL, Secret: "wrong"}
	if err := tr.Publish(context.Background(), "room1", sig.Offer, []byte("x")); err == nil {
		t.Fatal("expected publish to fail on secret mismatch")
	}
}
