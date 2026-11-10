// Package httpapi exposes login, the public keys, and the ForwardAuth endpoint
// the gateway calls for every request.
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zahra-ove/ticketing/pkg/httpx"
	"github.com/zahra-ove/ticketing/services/auth/internal/token"
	"github.com/zahra-ove/ticketing/services/auth/internal/users"
)

type handler struct {
	issuer *token.Issuer
	users  *users.Directory
	log    *slog.Logger
}

// NewHandler returns the HTTP handler of the auth service.
func NewHandler(issuer *token.Issuer, dir *users.Directory, log *slog.Logger) http.Handler {
	h := &handler{issuer: issuer, users: dir, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", httpx.Livez)
	mux.HandleFunc("GET /readyz", httpx.Livez) // no dependencies
	httpx.Route(mux, "POST /v1/token", h.token)
	httpx.Route(mux, "GET /.well-known/jwks.json", h.jwks)
	httpx.Route(mux, "GET /v1/verify", h.verify)
	return httpx.Middleware(mux, log)
}

type tokenRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type tokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`
	Role        string    `json:"role"`
	UserID      string    `json:"user_id"`
}

// token logs a user in with username and password (a simplified OAuth2
// "password" grant; a real deployment would use an identity provider).
func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		var berr *httpx.BadRequestError
		errors.As(err, &berr)
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Detail: berr.Msg})
		return
	}

	u, err := h.users.Authenticate(req.Username, req.Password)
	if err != nil {
		h.log.InfoContext(r.Context(), "login failed", slog.String("username", req.Username))
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Detail: "invalid username or password"})
		return
	}

	raw, exp, err := h.issuer.Issue(token.Identity{Subject: u.ID, Role: u.Role})
	if err != nil {
		h.log.ErrorContext(r.Context(), "issue token failed", slog.Any("error", err))
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, tokenResponse{
		AccessToken: raw,
		TokenType:   "Bearer",
		ExpiresIn:   int(time.Until(exp).Seconds()),
		ExpiresAt:   exp.UTC(),
		Role:        u.Role,
		UserID:      u.ID.String(),
	})
}

func (h *handler) jwks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpx.WriteJSON(w, http.StatusOK, h.issuer.JWKS())
}

// verify is called by the gateway (Traefik ForwardAuth) before each request.
//
//   - no Authorization header: 200 as anonymous, so public routes work;
//     services reject anonymous callers on protected routes.
//   - valid bearer token: 200 with the identity headers, which the gateway
//     copies onto the request it forwards.
//   - invalid or expired token: 401, and the gateway answers the client
//     directly. A bad token is never silently downgraded to anonymous.
func (h *handler) verify(w http.ResponseWriter, r *http.Request) {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		w.Header().Set(httpx.HeaderUserRole, httpx.RoleAnonymous)
		w.WriteHeader(http.StatusOK)
		return
	}

	raw, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok {
		h.unauthorized(w, r, "Authorization header must use the Bearer scheme")
		return
	}
	id, err := h.issuer.Verify(strings.TrimSpace(raw))
	if err != nil {
		h.log.InfoContext(r.Context(), "token rejected", slog.Any("error", err))
		h.unauthorized(w, r, "the bearer token is invalid or expired")
		return
	}

	w.Header().Set(httpx.HeaderCustomerID, id.Subject.String())
	w.Header().Set(httpx.HeaderUserRole, id.Role)
	w.WriteHeader(http.StatusOK)
}

func (h *handler) unauthorized(w http.ResponseWriter, r *http.Request, detail string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="ticketing", error="invalid_token"`)
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Detail: detail})
}
