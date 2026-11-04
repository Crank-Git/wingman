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

	tr := &Transport{
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

	tr := &Transport{
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

	tr := &Transport{
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

	tr := &Transport{Client: srv.Client(), BaseURL: srv.URL}
	if err := tr.Publish(context.Background(), "room1", sig.Offer, []byte("x")); err == nil {
		t.Fatal("expected error when server returns non-200")
	}
}

func TestPublishRequiresClient(t *testing.T) {
	// A nil client must surface an error rather than panic.
	h := NewHandler()
	srv := newTestServer(t, h)
	tr := &Transport{Client: srv.Client(), BaseURL: srv.URL}
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

	tr := &Transport{Client: http.DefaultClient, BaseURL: "http://" + addr}
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

	tr := &Transport{Client: srv.Client(), BaseURL: srv.URL, PollRetries: 1}
	if _, err := tr.Poll(context.Background(), "r", sig.Offer); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
}

// TestPublishDefaultsNilClient verifies that a nil Client falls back to
// http.DefaultClient.
func TestPublishDefaultsNilClient(t *testing.T) {
	h := NewHandler()
	srv := newTestServer(t, h)

	tr := &Transport{BaseURL: srv.URL} // Client left nil
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
	tr := &Transport{Client: newRoundTripClient(rt), BaseURL: "http://x"}
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
