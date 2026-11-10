package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/pkg/httpx"
)

func TestRequireRole(t *testing.T) {
	t.Parallel()
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	h := httpx.RequireRole(ok, httpx.RoleAdmin)

	tests := []struct {
		name       string
		id, role   string
		wantStatus int
	}{
		{name: "admin", id: uuid.NewString(), role: httpx.RoleAdmin, wantStatus: http.StatusNoContent},
		{name: "customer", id: uuid.NewString(), role: httpx.RoleCustomer, wantStatus: http.StatusForbidden},
		{name: "anonymous", wantStatus: http.StatusUnauthorized},
		{name: "unknown role", id: uuid.NewString(), role: "superuser", wantStatus: http.StatusUnauthorized},
		{name: "bad id", id: "nope", role: httpx.RoleAdmin, wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
			if tt.id != "" {
				req.Header.Set(httpx.HeaderCustomerID, tt.id)
				req.Header.Set(httpx.HeaderUserRole, tt.role)
			}
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
