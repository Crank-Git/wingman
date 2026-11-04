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
	"net"
	"net/http"
	"net/url"
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

// defaultHandlerTTL expires unserved messages on the next authorized access.
const defaultHandlerTTL = 30 * time.Second

// route is the single signaling endpoint both peers share.
const route = "/signaling"

const maxMessageBytes = 16 * 1024
const maxHandlerEntries = 1024
const maxHandlerPayloadBytes = 16 * 1024 * 1024

// Transport is a sig.Transport that publishes and polls SDP over HTTPS. It is
// safe for concurrent use.
type Transport struct {
	// Client performs the HTTP requests. Defaults to http.DefaultClient.
	Client *http.Client
	// BaseURL is the signaling endpoint, e.g. "https://signaling.example.com".
	BaseURL string
	// AllowLoopbackHTTP permits plaintext HTTP for local development and tests.
	AllowLoopbackHTTP bool
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
	copy := *t
	t = &copy
	if t.Client == nil {
		t.Client = http.DefaultClient
	}
	if t.PollInterval == 0 {
		t.PollInterval = defaultPollInterval
	}
	return t
}

// validateURL prevents credentials and SDP from leaving through an unsafe URL.
func (t *Transport) validateURL(u *url.URL) error {
	if !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return fmt.Errorf("https: service URL requires an absolute host without userinfo")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && t.AllowLoopbackHTTP && (strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("https: service URL requires HTTPS; HTTP requires the loopback development option")
}

// secureClient preserves the caller policy without changing the caller client.
func (t *Transport) secureClient() *http.Client {
	client := *t.Client
	policy := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := t.validateURL(req.URL); err != nil {
			return err
		}
		if t.Secret != "" && !sameOrigin(req.URL, via[0].URL) {
			return fmt.Errorf("https: cross-origin redirect with credentials")
		}
		if policy != nil {
			return policy(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &client
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
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
	if err := t.validateURL(req.URL); err != nil {
		return err
	}
	if t.Secret != "" {
		req.Header.Set(secretHeader, t.Secret)
	}
	resp, err := t.secureClient().Do(req)
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
		if err := t.validateURL(req.URL); err != nil {
			return nil, err
		}
		if t.Secret != "" {
			req.Header.Set(secretHeader, t.Secret)
		}
		resp, err := t.secureClient().Do(req)
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
// POST accepts at most 16 KiB per message. Larger bodies return 413 without storage.
// Capacity is 1024 entries and 16 MiB of payloads. Excess entries or bytes return 503.
// GET consumes a stored message. Authorized access sweeps messages older than TTL.
// Rooms are case-sensitive single labels of 1 to 63 bytes without dots,
// backslash escapes, or surrounding whitespace. Underscores and edge hyphens are valid.
type Handler struct {
	// mu guards msgs and payloadBytes so admission and replacement share one limit.
	mu           sync.Mutex
	msgs         map[string]entry
	payloadBytes int
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
			h.removeLocked(k)
		}
	}
	h.mu.Unlock()
}

func (h *Handler) store(key string, body []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	old, exists := h.msgs[key]
	bytes := h.payloadBytes - len(old.data) + len(body)
	if !exists && len(h.msgs) >= maxHandlerEntries || bytes > maxHandlerPayloadBytes {
		return false
	}
	h.msgs[key] = entry{data: body, at: time.Now()}
	h.payloadBytes = bytes
	return true
}

// removeLocked requires mu so removal releases the same quota that admission uses.
func (h *Handler) removeLocked(key string) {
	h.payloadBytes -= len(h.msgs[key].data)
	delete(h.msgs, key)
}

// ServeHTTP handles POST (store) and GET (retrieve) on the /signaling route.
// A GET for an empty room returns 404 so the client retries.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != route {
		http.NotFound(w, r)
		return
	}
	// Reject unauthorized requests before they read bodies or change retained entries.
	if h.Secret != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get(secretHeader)), []byte(h.Secret)) != 1 {
		http.Error(w, "https: unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	room := q.Get("room")
	kind := q.Get("kind")
	if room == "" || kind == "" {
		http.Error(w, "https: room and kind query params are required", http.StatusBadRequest)
		return
	}
	if len(room) > 63 || strings.ContainsAny(room, ".\\") || strings.TrimSpace(room) != room {
		http.Error(w, "https: invalid room", http.StatusBadRequest)
		return
	}
	if _, err := sig.ParseKind(kind); err != nil {
		http.Error(w, "https: invalid kind", http.StatusBadRequest)
		return
	}
	h.sweep(time.Now())

	key := room + "/" + kind

	switch r.Method {
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxMessageBytes+1))
		if len(body) > maxMessageBytes {
			http.Error(w, "https: message too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, "https: read body", http.StatusBadRequest)
			return
		}
		if !h.store(key, body) {
			http.Error(w, "https: capacity exceeded", http.StatusServiceUnavailable)
			return
		}
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
		h.removeLocked(key)
		h.mu.Unlock()

	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "https: method not allowed", http.StatusMethodNotAllowed)
	}
}
