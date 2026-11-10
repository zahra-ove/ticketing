package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func newIssuer(t *testing.T, seed byte) *Issuer {
	t.Helper()
	s := make([]byte, ed25519.SeedSize)
	s[0] = seed
	i, err := NewIssuer(Config{Seed: s, Issuer: "test-auth", Audience: "ticketing", TTL: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestIssueAndVerify(t *testing.T) {
	t.Parallel()
	i := newIssuer(t, 1)
	want := Identity{Subject: uuid.New(), Role: RoleCustomer}

	raw, exp, err := i.Issue(want)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(exp) <= 14*time.Minute {
		t.Errorf("expiry %s is too early", exp)
	}

	got, err := i.Verify(raw)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got != want {
		t.Errorf("Verify() = %+v, want %+v", got, want)
	}
}

func TestVerify_Rejects(t *testing.T) {
	t.Parallel()
	i := newIssuer(t, 1)
	valid, _, _ := i.Issue(Identity{Subject: uuid.New(), Role: RoleAdmin})

	expiredIssuer := newIssuer(t, 1)
	expiredIssuer.now = func() time.Time { return time.Now().Add(-time.Hour) }
	expired, _, _ := expiredIssuer.Issue(Identity{Subject: uuid.New(), Role: RoleAdmin})

	foreign, _, _ := newIssuer(t, 2).Issue(Identity{Subject: uuid.New(), Role: RoleAdmin})

	otherAudience := newIssuer(t, 1)
	otherAudience.audience = "someone-else"
	wrongAud, _, _ := otherAudience.Issue(Identity{Subject: uuid.New(), Role: RoleAdmin})

	// "alg: none" token claiming to be an admin: must never be accepted.
	unsigned, _ := jwt.NewWithClaims(jwt.SigningMethodNone, claims{
		Role: RoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "test-auth", Subject: uuid.NewString(), Audience: jwt.ClaimStrings{"ticketing"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)

	// Same claims signed with a different Ed25519 key.
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	forged, _ := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims{
		Role: RoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "test-auth", Subject: uuid.NewString(), Audience: jwt.ClaimStrings{"ticketing"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}).SignedString(otherKey)

	parts := strings.Split(valid, ".")
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]

	tests := map[string]string{
		"expired":        expired,
		"other key":      foreign,
		"wrong audience": wrongAud,
		"alg none":       unsigned,
		"forged":         forged,
		"tampered":       tampered,
		"garbage":        "not-a-token",
		"empty":          "",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := i.Verify(raw); !errors.Is(err, ErrInvalid) {
				t.Errorf("Verify() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestJWKS(t *testing.T) {
	t.Parallel()
	keys := newIssuer(t, 1).JWKS()["keys"]
	if len(keys) != 1 || keys[0].Kty != "OKP" || keys[0].Crv != "Ed25519" || keys[0].Kid == "" || keys[0].X == "" {
		t.Errorf("JWKS = %+v", keys)
	}
}

func TestNewIssuer_RejectsShortSeed(t *testing.T) {
	t.Parallel()
	if _, err := NewIssuer(Config{Seed: []byte("short")}); err == nil {
		t.Error("expected an error for a short seed")
	}
}
