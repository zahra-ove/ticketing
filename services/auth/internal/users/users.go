// Package users is a tiny, configuration-backed user directory. It stands in
// for a real identity provider (Keycloak, Auth0, ...) so the project runs
// without external accounts; the rest of the system only sees signed tokens.
package users

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/auth/internal/token"
)

// ErrInvalidCredentials is returned for any failed login, without saying
// whether the user exists.
var ErrInvalidCredentials = errors.New("invalid credentials")

// namespace makes user ids stable: the same username always has the same id.
var namespace = uuid.MustParse("6f2b1c1e-6a7a-4f4e-9a59-3f0a4c1b7d10")

// User is an account that can log in.
type User struct {
	ID       uuid.UUID
	Username string
	Role     string
	password [32]byte // sha256 of the password, compared in constant time
}

// Config lists the accounts.
type Config struct {
	AdminPassword string
	// DemoPassword is the password of the demo customers (alice, bob).
	DemoPassword string
	// DemoSignup lets any username in the form "customer-..." log in as a
	// customer with DemoPassword. It exists for load tests and must stay off
	// outside local development.
	DemoSignup bool
}

// Directory authenticates users.
type Directory struct {
	users        map[string]User
	demoSignup   bool
	demoPassword [32]byte
}

// NewDirectory returns a Directory with an admin and two demo customers.
func NewDirectory(cfg Config) *Directory {
	d := &Directory{users: map[string]User{}, demoSignup: cfg.DemoSignup, demoPassword: sha256.Sum256([]byte(cfg.DemoPassword))}
	d.add("admin", cfg.AdminPassword, token.RoleAdmin)
	d.add("alice", cfg.DemoPassword, token.RoleCustomer)
	d.add("bob", cfg.DemoPassword, token.RoleCustomer)
	return d
}

func (d *Directory) add(username, password, role string) {
	d.users[username] = User{
		ID:       uuid.NewSHA1(namespace, []byte(username)),
		Username: username,
		Role:     role,
		password: sha256.Sum256([]byte(password)),
	}
}

// Authenticate returns the user if the password matches.
//
// Passwords are compared as SHA-256 digests with subtle.ConstantTimeCompare,
// so timing does not reveal how much of a password was right. A real user
// store would keep salted hashes (bcrypt or argon2id) instead of plain config.
func (d *Directory) Authenticate(username, password string) (User, error) {
	given := sha256.Sum256([]byte(password))

	u, ok := d.users[username]
	if !ok && d.demoSignup && strings.HasPrefix(username, "customer-") && len(username) <= 64 {
		u = User{ID: uuid.NewSHA1(namespace, []byte(username)), Username: username, Role: token.RoleCustomer, password: d.demoPassword}
		ok = true
	}
	if !ok {
		// Compare anyway, so an unknown user takes as long as a wrong password.
		subtle.ConstantTimeCompare(given[:], d.demoPassword[:])
		return User{}, ErrInvalidCredentials
	}
	if subtle.ConstantTimeCompare(given[:], u.password[:]) != 1 {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}
