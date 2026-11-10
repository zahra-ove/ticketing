package users_test

import (
	"errors"
	"testing"

	"github.com/zahra-ove/ticketing/services/auth/internal/token"
	"github.com/zahra-ove/ticketing/services/auth/internal/users"
)

func TestAuthenticate(t *testing.T) {
	t.Parallel()
	d := users.NewDirectory(users.Config{AdminPassword: "admin-secret", DemoPassword: "demo-secret"})

	admin, err := d.Authenticate("admin", "admin-secret")
	if err != nil || admin.Role != token.RoleAdmin {
		t.Fatalf("admin login = %+v, %v", admin, err)
	}
	alice, err := d.Authenticate("alice", "demo-secret")
	if err != nil || alice.Role != token.RoleCustomer {
		t.Fatalf("alice login = %+v, %v", alice, err)
	}
	again, _ := d.Authenticate("alice", "demo-secret")
	if again.ID != alice.ID {
		t.Error("user ids must be stable across logins")
	}

	for _, tc := range []struct{ user, pass string }{
		{"admin", "demo-secret"},
		{"alice", "wrong"},
		{"mallory", "demo-secret"},
		{"customer-1", "demo-secret"}, // demo signup is off
	} {
		if _, err := d.Authenticate(tc.user, tc.pass); !errors.Is(err, users.ErrInvalidCredentials) {
			t.Errorf("%s/%s: error = %v, want ErrInvalidCredentials", tc.user, tc.pass, err)
		}
	}
}

func TestAuthenticate_DemoSignup(t *testing.T) {
	t.Parallel()
	d := users.NewDirectory(users.Config{AdminPassword: "a", DemoPassword: "demo", DemoSignup: true})

	u, err := d.Authenticate("customer-42", "demo")
	if err != nil || u.Role != token.RoleCustomer {
		t.Fatalf("demo customer login = %+v, %v", u, err)
	}
	if _, err := d.Authenticate("admin2", "demo"); err == nil {
		t.Error("demo signup must only create customer-* users")
	}
	if _, err := d.Authenticate("customer-42", "wrong"); err == nil {
		t.Error("demo customers still need the demo password")
	}
}
