package model

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fetch(t *testing.T, status int, body string, hdr map[string]string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return ReadError(resp)
}

func TestReadErrorNilOn2xx(t *testing.T) {
	if err := fetch(t, 200, `{}`, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadErrorExtractsMessage(t *testing.T) {
	err := fetch(t, 401, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`, nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 401 || he.Message != "invalid x-api-key" {
		t.Fatalf("got %#v", err)
	}
	err = fetch(t, 400, `{"error":"model not found"}`, nil)
	if !errors.As(err, &he) || he.Message != "model not found" {
		t.Fatalf("got %#v", err)
	}
	err = fetch(t, 500, `<html>oops</html>`, nil)
	if !errors.As(err, &he) || he.Message != "<html>oops</html>" {
		t.Fatalf("got %#v", err)
	}
}

func TestReadErrorRateLimited(t *testing.T) {
	err := fetch(t, 429, `{"error":{"message":"slow down"}}`, map[string]string{"Retry-After": "30"})
	var rl *RateLimited
	if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second || rl.Err.Message != "slow down" {
		t.Fatalf("got %#v", err)
	}
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatal("RateLimited must unwrap to HTTPError")
	}
}
