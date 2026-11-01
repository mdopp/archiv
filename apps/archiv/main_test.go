package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, path string, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestHealth(t *testing.T) {
	if code, body := get(t, handler("s3cret"), "/healthz", nil); code != 200 || strings.TrimSpace(body) != "ok" {
		t.Fatalf("healthz: %d %q", code, body)
	}
}

func TestGreetsTheForwardedUser(t *testing.T) {
	_, body := get(t, handler("s3cret"), "/", map[string]string{proxyHeader: "s3cret", "Remote-User": "anna"})
	if strings.TrimSpace(body) != "Hallo, anna!" {
		t.Fatalf("got %q", body)
	}
}

func TestRemoteUserWithoutTheSecretIsIgnored(t *testing.T) {
	for _, h := range []map[string]string{
		{"Remote-User": "mallory"},
		{proxyHeader: "wrong", "Remote-User": "mallory"},
	} {
		if _, body := get(t, handler("s3cret"), "/", h); strings.TrimSpace(body) != "Hallo!" {
			t.Fatalf("%v: got %q", h, body)
		}
	}
	// no secret configured: nobody is trusted
	if _, body := get(t, handler(""), "/", map[string]string{proxyHeader: "", "Remote-User": "mallory"}); strings.TrimSpace(body) != "Hallo!" {
		t.Fatalf("empty secret: got %q", body)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	if code, _ := get(t, handler("s3cret"), "/nope", nil); code != 404 {
		t.Fatalf("got %d", code)
	}
}
