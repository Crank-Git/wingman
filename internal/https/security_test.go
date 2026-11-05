package https

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Crank-Git/wingman/internal/sig"
)

func TestTransportRejectsUnsafeURLsBeforeRequest(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://user:password@example.com", "http://127.0.0.1", "https:///missing", "/relative", "ftp://example.com"} {
		t.Run(base, func(t *testing.T) {
			calls := 0
			tr := &Transport{BaseURL: base, Secret: "placeholder", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("request escaped")
			})}}
			if err := tr.Publish(context.Background(), "r", sig.Offer, []byte("private SDP")); err == nil {
				t.Fatal("Publish accepts an unsafe URL")
			}
			if _, err := tr.Poll(context.Background(), "r", sig.Offer); err == nil {
				t.Fatal("Poll accepts an unsafe URL")
			}
			if calls != 0 {
				t.Fatalf("requests = %d, want 0", calls)
			}
		})
	}
}

func TestTransportProtectsRedirects(t *testing.T) {
	for _, secret := range []string{"", "placeholder"} {
		t.Run("secret="+secret, func(t *testing.T) {
			calls := 0
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) }))
			defer target.Close()
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			tr := &Transport{BaseURL: source.URL, Secret: secret, Client: source.Client()}
			err := tr.Publish(context.Background(), "r", sig.Offer, []byte("sdp"))
			if secret != "" && (err == nil || calls != 0) {
				t.Fatalf("cross-origin redirect: err=%v calls=%d", err, calls)
			}
			if secret == "" && (err != nil || calls != 1) {
				t.Fatalf("public redirect: err=%v calls=%d", err, calls)
			}
			if tr.Client.CheckRedirect != nil {
				t.Fatal("caller client changed")
			}
		})
	}
}

func TestTransportPreservesRedirectPolicy(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	client := srv.Client()
	want := errors.New("caller policy")
	calls := 0
	client.CheckRedirect = func(*http.Request, []*http.Request) error { calls++; return want }
	tr := &Transport{BaseURL: srv.URL, Client: client, Secret: "placeholder"}
	if err := tr.Publish(context.Background(), "r", sig.Offer, nil); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("policy calls = %d", calls)
	}
	client.CheckRedirect = nil
	if err := tr.Publish(context.Background(), "r", sig.Offer, nil); err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("default policy: %v", err)
	}
}

func TestTransportAllowsOnlyExplicitLoopbackHTTP(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "[::1]", "localhost", "example.com", "127.0.0.1.example.com"} {
		u, err := http.NewRequest(http.MethodGet, "http://"+host, nil)
		if err != nil {
			t.Fatal(err)
		}
		tr := &Transport{AllowLoopbackHTTP: true}
		err = tr.validateURL(u.URL)
		wantAllowed := host == "127.0.0.1" || host == "[::1]" || host == "localhost"
		if (err == nil) != wantAllowed {
			t.Errorf("host %s: %v", host, err)
		}
	}
}

func TestTransportRejectsUnsafeRedirectURLs(t *testing.T) {
	for _, location := range []string{"http://example.com", "https://user:password@example.com"} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, location, http.StatusTemporaryRedirect)
		}))
		tr := &Transport{BaseURL: srv.URL, Client: srv.Client()}
		if err := tr.Publish(context.Background(), "r", sig.Offer, nil); err == nil {
			t.Error("unsafe redirect succeeds")
		}
		srv.Close()
	}
}

func TestSameOriginUsesEffectivePorts(t *testing.T) {
	for _, test := range []struct {
		a, b string
		same bool
	}{
		{"https://example.com", "https://EXAMPLE.com:443", true},
		{"http://localhost", "http://localhost:80", true},
		{"https://example.com:444", "https://example.com", false},
		{"https://example.com", "http://example.com", false},
	} {
		a, err := url.Parse(test.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := url.Parse(test.b)
		if err != nil {
			t.Fatal(err)
		}
		if sameOrigin(a, b) != test.same {
			t.Errorf("origins %s and %s", test.a, test.b)
		}
	}
}
