// Package token issues and verifies JSON Web Tokens signed with Ed25519.
//
// EdDSA keeps tokens small and signing fast, and only the public key is
// needed to verify, which the service publishes as a JWKS document.
package token

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Roles a token can carry.
const (
	RoleCustomer = "customer"
	RoleAdmin    = "admin"
)

// ErrInvalid means the token is malformed, expired, or not signed by us.
var ErrInvalid = errors.New("invalid token")

// Identity is who a token belongs to.
type Identity struct {
	Subject uuid.UUID
	Role    string
}

type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// Issuer signs and verifies tokens.
type Issuer struct {
	key      ed25519.PrivateKey
	kid      string
	issuer   string
	audience string
	ttl      time.Duration
	now      func() time.Time
}

// Config configures an Issuer.
type Config struct {
	// Seed is the 32-byte Ed25519 private key seed.
	Seed     []byte
	Issuer   string
	Audience string
	TTL      time.Duration
}

// NewIssuer returns an Issuer for the key derived from cfg.Seed.
func NewIssuer(cfg Config) (*Issuer, error) {
	if len(cfg.Seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing key seed must be %d bytes, got %d", ed25519.SeedSize, len(cfg.Seed))
	}
	key := ed25519.NewKeyFromSeed(cfg.Seed)
	sum := sha256.Sum256(key.Public().(ed25519.PublicKey))
	return &Issuer{
		key:      key,
		kid:      base64.RawURLEncoding.EncodeToString(sum[:8]),
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		ttl:      cfg.TTL,
		now:      time.Now,
	}, nil
}

// Issue returns a signed access token for id and its expiry time.
func (i *Issuer) Issue(id Identity) (string, time.Time, error) {
	now := i.now()
	exp := now.Add(i.ttl)
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims{
		Role: id.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    i.issuer,
			Subject:   id.Subject.String(),
			Audience:  jwt.ClaimStrings{i.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        uuid.NewString(),
		},
	})
	t.Header["kid"] = i.kid
	signed, err := t.SignedString(i.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, exp, nil
}

// Verify checks signature, algorithm, issuer, audience and expiry, and
// returns the identity in the token. Every failure is reported as ErrInvalid.
func (i *Issuer) Verify(raw string) (Identity, error) {
	var c claims
	_, err := jwt.ParseWithClaims(raw, &c,
		func(*jwt.Token) (any, error) { return i.key.Public(), nil },
		// Pinning the algorithm blocks "alg: none" and key-confusion attacks.
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(i.issuer),
		jwt.WithAudience(i.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
		jwt.WithTimeFunc(i.now),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	sub, err := uuid.Parse(c.Subject)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: subject is not a UUID", ErrInvalid)
	}
	if c.Role != RoleCustomer && c.Role != RoleAdmin {
		return Identity{}, fmt.Errorf("%w: unknown role %q", ErrInvalid, c.Role)
	}
	return Identity{Subject: sub, Role: c.Role}, nil
}

// JWK is a JSON Web Key for an Ed25519 public key (RFC 8037).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// JWKS returns the public key set, so other parties can verify tokens offline.
func (i *Issuer) JWKS() map[string][]JWK {
	return map[string][]JWK{"keys": {{
		Kty: "OKP",
		Crv: "Ed25519",
		X:   base64.RawURLEncoding.EncodeToString(i.key.Public().(ed25519.PublicKey)),
		Kid: i.kid,
		Alg: jwt.SigningMethodEdDSA.Alg(),
		Use: "sig",
	}}}
}
