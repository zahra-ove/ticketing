package httpx

import (
	"net/http"

	"github.com/google/uuid"
)

// Identity headers. The gateway removes them from every incoming request and
// sets them only from a verified token (ForwardAuth), so services behind it
// can trust them. Services are not reachable from outside except through the
// gateway; see docs/adr/0007-gateway-and-authentication.md.
const (
	HeaderCustomerID = "X-Customer-ID"
	HeaderUserRole   = "X-User-Role"
)

// Roles carried in HeaderUserRole.
const (
	RoleAnonymous = "anonymous"
	RoleCustomer  = "customer"
	RoleAdmin     = "admin"
)

// Caller is the authenticated user of a request.
type Caller struct {
	ID   uuid.UUID
	Role string
}

// IsAdmin reports whether the caller has the admin role.
func (c Caller) IsAdmin() bool { return c.Role == RoleAdmin }

// CallerFrom reads the caller from the identity headers. ok is false for
// anonymous requests.
func CallerFrom(r *http.Request) (c Caller, ok bool) {
	id, err := uuid.Parse(r.Header.Get(HeaderCustomerID))
	if err != nil {
		return Caller{Role: RoleAnonymous}, false
	}
	role := r.Header.Get(HeaderUserRole)
	if role != RoleCustomer && role != RoleAdmin {
		return Caller{Role: RoleAnonymous}, false
	}
	return Caller{ID: id, Role: role}, true
}

// RequireRole lets the request through only for an authenticated caller with
// one of roles: 401 without a token, 403 with the wrong role.
func RequireRole(next http.HandlerFunc, roles ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := CallerFrom(r)
		if !ok {
			WriteProblem(w, r, Problem{Status: http.StatusUnauthorized, Detail: "a valid bearer token is required"})
			return
		}
		for _, role := range roles {
			if c.Role == role {
				next(w, r)
				return
			}
		}
		WriteProblem(w, r, Problem{Status: http.StatusForbidden, Detail: "your role is not allowed to do this"})
	}
}
