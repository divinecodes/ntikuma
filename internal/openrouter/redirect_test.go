package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRedirectsToAnotherOriginAreRefused(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		body, _ := io.ReadAll(r.Body)
		t.Errorf("redirect followed: x-api-key=%q authorization=%q body=%q", r.Header.Get("x-api-key"), r.Header.Get("Authorization"), body)
	}))
	defer other.Close()
	// localhost and 127.0.0.1 are different origins to the client.
	target := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusFound} {
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target+r.URL.Path, status)
		}))
		c := New("fake-test-key")
		c.BaseURL = origin.URL
		_, err := c.Stream(context.Background(), Request{Models: []string{"m"}, Messages: []Message{{Role: "user", Content: "private question"}}}, nil)
		if err == nil || !strings.Contains(err.Error(), "refused a redirect") {
			t.Fatalf("%d: %v", status, err)
		}
		if _, err := c.Models(context.Background()); err == nil {
			t.Fatalf("%d: model list followed a cross-origin redirect", status)
		}
		origin.Close()
	}
	if reached.Load() != 0 {
		t.Fatalf("other origin reached %d times", reached.Load())
	}
}

func TestSameOriginRedirectsAreFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old/chat/completions" {
			http.Redirect(w, r, "/v1/chat/completions", http.StatusTemporaryRedirect)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fake-test-key" {
			t.Errorf("key dropped on a same-origin redirect")
		}
		sse(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "data: [DONE]")
	}))
	defer srv.Close()
	c := New("fake-test-key")
	c.BaseURL = srv.URL + "/old"
	res, err := c.Stream(context.Background(), Request{Models: []string{"m"}, Messages: []Message{{Role: "user", Content: "q"}}}, nil)
	if err != nil || res.Text != "ok" {
		t.Fatalf("%q %v", res.Text, err)
	}
}

func TestRedirectFromHTTPSToHTTPIsRefused(t *testing.T) {
	var reached atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer secure.Close()
	c := New("fake-test-key")
	c.BaseURL = secure.URL
	c.HTTP.Transport = secure.Client().Transport
	_, err := c.Stream(context.Background(), Request{Models: []string{"m"}, Messages: []Message{{Role: "user", Content: "q"}}}, nil)
	if err == nil || reached.Load() != 0 {
		t.Fatalf("downgrade followed: %v, reached %d", err, reached.Load())
	}
	// Same host and port, other scheme: still a different origin.
	sameHost := strings.Replace(secure.URL, "https://", "http://", 1)
	req, _ := http.NewRequest(http.MethodPost, sameHost+"/x", nil)
	via, _ := http.NewRequest(http.MethodPost, secure.URL+"/x", nil)
	if SameOriginRedirects(req, []*http.Request{via}) == nil {
		t.Fatal("https to http on the same host allowed")
	}
}
