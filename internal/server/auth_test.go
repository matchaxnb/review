package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

func TestTokenAuthRejectsWithoutToken(t *testing.T) {
	h := TokenAuth("secret")(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTokenAuthRejectsWrongToken(t *testing.T) {
	h := TokenAuth("secret")(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?token=nope", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTokenAuthQuerySetsCookieAndRedirects(t *testing.T) {
	h := TokenAuth("secret")(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?token=secret&path=a.go", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?path=a.go" {
		t.Fatalf("Location = %q, want %q", loc, "/?path=a.go")
	}
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == tokenCookie && c.Value == "secret" {
			found = true
		}
	}
	if !found {
		t.Fatal("token cookie not set")
	}
}

func TestTokenAuthCookieIsAccepted(t *testing.T) {
	h := TokenAuth("secret")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: tokenCookie, Value: "secret"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestTokenAuthBearerIsAccepted(t *testing.T) {
	h := TokenAuth("secret")(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/file", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestTokenAuthAllowsNonGetWithoutRedirect(t *testing.T) {
	h := TokenAuth("secret")(okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/?token=secret", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("unexpected redirect to %q", loc)
	}
}

func TestNewTokenIsRandomAndHex(t *testing.T) {
	a, err := NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two tokens are equal")
	}
	if len(a) != 64 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("token %q is not 64 hex chars", a)
	}
}
