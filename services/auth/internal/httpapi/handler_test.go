package httpapi_test

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/services/auth/internal/httpapi"
	"github.com/zahra-ove/ticketing/services/auth/internal/token"
	"github.com/zahra-ove/ticketing/services/auth/internal/users"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	issuer, err := token.NewIssuer(token.Config{Seed: make([]byte, ed25519.SeedSize), Issuer: "auth", Audience: "ticketing", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	dir := users.NewDirectory(users.Config{AdminPassword: "admin-pw", DemoPassword: "demo-pw"})
	return httpapi.NewHandler(issuer, dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func login(t *testing.T, h http.Handler, user, pass string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/token",
		strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body.AccessToken
}

func verify(t *testing.T, h http.Handler, authz string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/verify", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginAndVerify(t *testing.T) {
	t.Parallel()
	h := newHandler(t)

	rec, tok := login(t, h, "admin", "admin-pw")
	if rec.Code != http.StatusOK || tok == "" {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("token responses must not be cached")
	}

	v := verify(t, h, "Bearer "+tok)
	if v.Code != http.StatusOK || v.Header().Get(httpx.HeaderUserRole) != httpx.RoleAdmin || v.Header().Get(httpx.HeaderCustomerID) == "" {
		t.Errorf("verify = %d, headers %v", v.Code, v.Header())
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	t.Parallel()
	if rec, _ := login(t, newHandler(t), "alice", "nope"); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	h := newHandler(t)

	anon := verify(t, h, "")
	if anon.Code != http.StatusOK || anon.Header().Get(httpx.HeaderUserRole) != httpx.RoleAnonymous || anon.Header().Get(httpx.HeaderCustomerID) != "" {
		t.Errorf("anonymous verify = %d, headers %v", anon.Code, anon.Header())
	}
	for _, authz := range []string{"Bearer garbage", "Basic YWxpY2U6eA==", "Bearer "} {
		if rec := verify(t, h, authz); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%q: status = %d, want 401 with WWW-Authenticate", authz, rec.Code)
		}
	}
}
