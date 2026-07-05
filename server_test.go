package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

// The mutating endpoints parse the id before touching the store, so a
// non-numeric id distinguishes "past the auth gate" (400) from
// "rejected" (401) without needing a database.
func TestPublicModeGatesMutations(t *testing.T) {
	app := &App{cfg: Config{Public: true, WebPassword: "hemligt"}}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	res, err := http.Post(srv.URL+"/api/listings/abc/broken", "application/json", strings.NewReader(`{"broken":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated broken: got %d, want 401", res.StatusCode)
	}
	res, err = http.Post(srv.URL+"/api/listings/abc/exclude", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated exclude: got %d, want 401", res.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	res, err = client.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"password":"fel"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d, want 401", res.StatusCode)
	}

	res, err = client.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"password":"hemligt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login: got %d, want 200", res.StatusCode)
	}

	res, err = client.Post(srv.URL+"/api/listings/abc/broken", "application/json", strings.NewReader(`{"broken":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("authenticated broken: got %d, want 400 (past the auth gate)", res.StatusCode)
	}
}

func TestPrivateModeNeedsNoLogin(t *testing.T) {
	app := &App{cfg: Config{}}
	srv := httptest.NewServer(app.Router())
	defer srv.Close()

	res, err := http.Post(srv.URL+"/api/listings/abc/broken", "application/json", strings.NewReader(`{"broken":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("broken without auth: got %d, want 400 (no gate)", res.StatusCode)
	}

	res, err = http.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"password":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("login with no password configured: got %d, want 404", res.StatusCode)
	}
}
