package notify

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildRejectsMissingConfig(t *testing.T) {
	for _, kind := range []string{"discord", "ntfy", "webhook"} {
		if _, err := Build(kind, "", ""); err == nil {
			t.Errorf("%s: want error on empty URL", kind)
		}
	}
	if _, err := Build("gotify", "https://g", ""); err == nil {
		t.Error("gotify: want error on empty token")
	}
	if _, err := Build("nope", "x", ""); err == nil {
		t.Error("unknown kind should error")
	}
}

func TestChannelsPost(t *testing.T) {
	var req *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		req = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := Hit{Model: "iPhone 16 Pro", StorageGB: 256, Price: 6999, RefPrice: 8500, PctBelow: 17.7, Samples: 42, URL: "https://x/1"}

	t.Run("discord", func(t *testing.T) {
		n, _ := Build("discord", srv.URL, "")
		if err := n.Notify(context.Background(), h); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body, "iPhone 16 Pro") {
			t.Errorf("discord body missing model: %s", body)
		}
	})

	t.Run("ntfy sets headers", func(t *testing.T) {
		n, _ := Build("ntfy", srv.URL, "tok")
		if err := n.Notify(context.Background(), h); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Title") == "" {
			t.Error("ntfy: no Title header")
		}
		if strings.ContainsRune(req.Header.Get("Title"), 'ö') {
			t.Errorf("ntfy Title should be ASCII, got %q", req.Header.Get("Title"))
		}
		if req.Header.Get("Click") != h.URL {
			t.Errorf("ntfy Click = %q, want %q", req.Header.Get("Click"), h.URL)
		}
		if req.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("ntfy auth = %q", req.Header.Get("Authorization"))
		}
	})

	t.Run("gotify token in query", func(t *testing.T) {
		n, _ := Build("gotify", srv.URL, "apptoken")
		if err := n.Notify(context.Background(), h); err != nil {
			t.Fatal(err)
		}
		if req.URL.Query().Get("token") != "apptoken" {
			t.Errorf("gotify token = %q", req.URL.Query().Get("token"))
		}
	})
}

func TestNtfyAuthHeaderAndQueryParam(t *testing.T) {
	var auth, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		query = r.URL.Query().Get("auth")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A pasted "Bearer "-prefixed, space-padded token must still normalize.
	n, err := Build("ntfy", srv.URL+"/topic", "  Bearer tk_secret ")
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(context.Background(), Hit{Model: "iPhone"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tk_secret" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer tk_secret")
	}
	// ntfy decodes ?auth with raw-url base64; it must round-trip to the header.
	dec, err := base64.RawURLEncoding.DecodeString(query)
	if err != nil {
		t.Fatalf("auth param not raw-url base64: %v", err)
	}
	if string(dec) != "Bearer tk_secret" {
		t.Errorf("?auth decodes to %q, want %q", dec, "Bearer tk_secret")
	}
}

func TestChannelPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad webhook", http.StatusNotFound)
	}))
	defer srv.Close()
	n, _ := Build("discord", srv.URL, "")
	if err := n.Notify(context.Background(), Hit{}); err == nil {
		t.Fatal("want error on 404 response")
	}
}

func TestMultiJoinsErrors(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()

	good, _ := Build("webhook", ok.URL, "")
	fail, _ := Build("webhook", bad.URL, "")
	m := Multi{LogNotifier{}, good, fail}
	if err := m.Notify(context.Background(), Hit{}); err == nil {
		t.Fatal("Multi should surface the failing channel's error")
	}
}
