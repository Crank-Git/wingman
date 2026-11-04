// Package https provides a sig.Transport that exchanges SDP over HTTPS and the
// matching http.Handler that stores the messages. It exists so peers can fall
// back to HTTPS when DNS write-over-query is blocked (for example by a firewall
// or an ISP that drops unsolicited TXT queries): the same room/kind addressing
// applies, but the transport is an ordinary HTTPS request/response pair.
//
// Unlike the DNS transport, no message chunking is required here because HTTP
// carries the whole SDP in a single request body.
package https

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Crank-Git/wingman/internal/sig"
)

// defaultPollInterval is the delay between poll attempts when the room is not
// yet ready.
const defaultPollInterval = 250 * time.Millisecond

// secretHeader is the HTTP header carrying the shared signaling secret. It is
// dedicated to this channel rather than reusing Authorization so that the
// secret is clearly scoped to wingman's signaling endpoint.
const secretHeader = "X-Wingman-Secret"

// defaultHandlerTTL bounds how long an unserved SDP message is retained before
// it is swept. It bounds memory for rooms that never complete a handshake.
const defaultHandlerTTL = 30 * time.Second

// route is the single signaling endpoint both peers share.
const route = "/signaling"

// Transport is a sig.Transport that publishes and polls SDP over HTTPS. It is
// safe for concurrent use.
type Transport struct {
	// Client performs the HTTP requests. Defaults to http.DefaultClient.
	Client *http.Client
	// BaseURL is the signaling endpoint, e.g. "https://signaling.example.com".
	BaseURL string
	// PollInterval is the delay between poll attempts. Defaults to 250ms.
	PollInterval time.Duration
	// PollRetries bounds consecutive misses before giving up. 0 means unlimited.
	PollRetries int
	// Secret, when non-empty, is sent as the secretHeader on every request so a
	// handler configured with the same secret can authenticate the channel.
	Secret string
}

// withDefaults fills in the zero-valued fields.
func (t *Transport) withDefaults() *Transport {
	if t.Client == nil {
		t.Client = http.DefaultClient
	}
	if t.PollInterval == 0 {
		t.PollInterval = defaultPollInterval
	}
	return t
}

// endpoint builds the signaling URL for a room/kind pair.
func (t *Transport) endpoint(room string, k sig.Kind) string {
	return t.BaseURL + route + "?room=" + urlEncode(room) + "&kind=" + urlEncode(k.String())
}

// Publish POSTs the full SDP message to the signaling endpoint.
func (t *Transport) Publish(ctx context.Context, room string, k sig.Kind, msg []byte) error {
	t = t.withDefaults()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint(room, k), strings.NewReader(string(msg)))
	if err != nil {
		return fmt.Errorf("https: new request: %w", err)
	}
	if t.Secret != "" {
		req.Header.Set(secretHeader, t.Secret)
	}
	resp, err := t.Client.Do(req)
	if err != nil {
		return fmt.Errorf("https: publish: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("https: publish: server returned %s", resp.Status)
	}
	return nil
}

// Poll GETs the SDP message, retrying while the room reports it is not ready
// (HTTP 404) until PollRetries is exhausted or ctx is cancelled.
func (t *Transport) Poll(ctx context.Context, room string, k sig.Kind) ([]byte, error) {
	t = t.withDefaults()
	retries := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint(room, k), nil)
		if err != nil {
			return nil, fmt.Errorf("https: new request: %w", err)
		}
		if t.Secret != "" {
			req.Header.Set(secretHeader, t.Secret)
		}
		resp, err := t.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("https: poll: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		code := resp.StatusCode

		switch {
		case code == http.StatusOK:
			if readErr != nil {
				return nil, fmt.Errorf("https: poll: read body: %w", readErr)
			}
			return body, nil
		case code == http.StatusNotFound:
			// Room not ready yet: retry with backoff.
			retries++
			if t.PollRetries > 0 && retries >= t.PollRetries {
				return nil, &PollError{Room: room, Kind: k.String(), Retries: retries}
			}
			wait(ctx, t.PollInterval)
			continue
		default:
			return nil, fmt.Errorf("https: poll: server returned %s", resp.Status)
		}
	}
}

// wait sleeps for d or returns early on ctx cancellation.
func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// PollError indicates polling exceeded the retry budget for a room that never
// received the requested message.
type PollError struct {
	Room    string
	Kind    string
	Retries int
}

func (e *PollError) Error() string {
	return "wingman: poll timed out for room " + e.Room + " kind " + e.Kind + " after " + strconv.Itoa(e.Retries) + " retries"
}

// urlEncode percent-encodes a query parameter value.
func urlEncode(s string) string {
	// Byte-wise percent-encoding. Ranging over bytes rather than runes avoids a
	// broken utf8.AppendRune in some toolchains and is equivalent to net/url's
	// own encoding: each UTF-8 byte outside the unreserved set is encoded, so a
	// multi-byte rune becomes the same %XX sequence as QueryEscape would emit.
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c == '-' || c == '.' || c == '~':
			b.WriteByte(c)
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// entry is one stored SDP message together with the time it was written, used
// to bound retention via TTL sweep.
type entry struct {
	data []byte
	at   time.Time
}

// Handler is an http.Handler that stores one SDP message per (room, kind) and
// serves it over HTTPS. It is safe for concurrent use.
//
// Two mechanisms bound memory: a served message is deleted on read (SDP is
// one-shot), and unserved messages older than TTL are swept on access so rooms
// that never complete a handshake cannot grow the map without bound.
type Handler struct {
	mu   sync.Mutex
	msgs map[string]entry
	// TTL bounds how long an unserved message is retained. Zero means the
	// default; entries older than TTL are swept on access.
	TTL time.Duration
	// Secret, when non-empty, requires requests to present it via the
	// secretHeader before any message is read or written. Empty means the
	// channel is open to anyone who knows the room/kind.
	Secret string
}

// NewHandler returns an empty Handler with the default TTL.
func NewHandler() *Handler {
	return &Handler{
		msgs: make(map[string]entry),
		TTL:  defaultHandlerTTL,
	}
}

// sweep drops entries older than the handler's TTL. It is called on access so
// no background goroutine is required.
func (h *Handler) sweep(now time.Time) {
	if h.TTL <= 0 {
		return
	}
	h.mu.Lock()
	for k, e := range h.msgs {
		if now.Sub(e.at) > h.TTL {
			delete(h.msgs, k)
		}
	}
	h.mu.Unlock()
}

// ServeHTTP handles POST (store) and GET (retrieve) on the /signaling route.
// A GET for an empty room returns 404 so the client retries.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != route {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	room := q.Get("room")
	kind := q.Get("kind")
	if room == "" || kind == "" {
		http.Error(w, "https: room and kind query params are required", http.StatusBadRequest)
		return
	}
	if _, err := sig.ParseKind(kind); err != nil {
		http.Error(w, "https: invalid kind", http.StatusBadRequest)
		return
	}
	// Bound memory: drop any entries that have outlived the TTL.
	h.sweep(time.Now())

	// Authenticate the channel when a secret is configured. The constant-time
	// comparison avoids leaking the secret via timing.
	if h.Secret != "" {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(secretHeader)), []byte(h.Secret)) != 1 {
			http.Error(w, "https: unauthorized", http.StatusUnauthorized)
			return
		}
	}
	key := room + "/" + kind

	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "https: read body", http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		h.msgs[key] = entry{data: body, at: time.Now()}
		h.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		h.mu.Lock()
		e, ok := h.msgs[key]
		h.mu.Unlock()
		if !ok {
			http.Error(w, "https: not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/sdp")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(e.data)
		// SDP is one-shot: remove it once served so a retried GET does not
		// return a stale message.
		h.mu.Lock()
		delete(h.msgs, key)
		h.mu.Unlock()

	default:
		http.Error(w, "https: method not allowed", http.StatusMethodNotAllowed)
	}
}
