package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// tokenCookie is the cookie that carries the access token once a browser has
// presented it, so later requests (assets, API calls, the WebSocket handshake,
// which cannot set a header of its own) are recognised without it in the URL.
const tokenCookie = "review_token"

// NewToken returns a random access token of n bytes, hex encoded.
func NewToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// TokenAuth wraps a handler so that every request must carry the access token:
// as a "token" query parameter, as a bearer Authorization header, or as the
// cookie this sets on first use. The token guards a review served beyond the
// loopback interface, where the whole source tree would otherwise be open to
// anyone who can reach the port.
//
// A GET that presents a valid token in the query is answered with a redirect to
// the same URL without it, so the token does not linger in the address bar or
// browser history.
func TokenAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A token in the query authorises and is kept in a cookie, then
			// dropped from the URL so it does not linger in the address bar or
			// the browser history.
			if qt := r.URL.Query().Get("token"); qt != "" && tokenEqual(qt, token) {
				http.SetCookie(w, &http.Cookie{
					Name:     tokenCookie,
					Value:    token,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
				})
				if r.Method == http.MethodGet {
					http.Redirect(w, r, withoutToken(r), http.StatusFound)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if tokenEqual(bearerToken(r), token) || matchesCookie(r, token) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

// bearerToken returns the token from a bearer Authorization header, or an empty
// string.
func bearerToken(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return t
	}
	return ""
}

// matchesCookie reports whether the request carries a cookie holding the token.
func matchesCookie(r *http.Request, token string) bool {
	c, err := r.Cookie(tokenCookie)
	if err != nil {
		return false
	}
	return tokenEqual(c.Value, token)
}

// tokenEqual compares two tokens in constant time, so a wrong one cannot be
// narrowed down by timing.
func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// withoutToken returns the request's URL with the token query parameter
// removed, keeping any others.
func withoutToken(r *http.Request) string {
	q := r.URL.Query()
	q.Del("token")
	target := r.URL.Path
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	return target
}
