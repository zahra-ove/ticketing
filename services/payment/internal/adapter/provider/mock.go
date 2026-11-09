// Package provider contains a mock payment provider. It behaves like a real
// card processor in the ways that matter for the saga: it has latency, it
// sometimes fails transiently, it declines some cards, and it honours
// idempotency keys so a retried charge never charges twice.
package provider

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zahra-ove/ticketing/services/payment/internal/app"
)

// DeclinedAmountSuffix is the cent value that triggers a decline: amounts
// such as 50.13 fail, which makes the failure path easy to try by hand.
const DeclinedAmountSuffix = 13

// ErrUnavailable is the transient error the mock returns now and then.
var ErrUnavailable = errors.New("payment provider temporarily unavailable")

// Config tunes the mock.
type Config struct {
	// Latency is the simulated processing time of each call.
	Latency time.Duration
	// FailureRate is the probability (0..1) of a transient failure per call.
	FailureRate float64
}

// Mock implements app.Provider in memory.
type Mock struct {
	cfg Config

	mu      sync.Mutex
	charges map[string]string // idempotency key -> reference
	refunds map[string]bool
}

var _ app.Provider = (*Mock)(nil)

// NewMock returns a Mock.
func NewMock(cfg Config) *Mock {
	return &Mock{cfg: cfg, charges: map[string]string{}, refunds: map[string]bool{}}
}

// Charge implements app.Provider.
func (m *Mock) Charge(ctx context.Context, req app.ChargeRequest) (string, error) {
	if err := m.simulate(ctx); err != nil {
		return "", err
	}
	if req.AmountCents%100 == DeclinedAmountSuffix {
		return "", &app.DeclinedError{Reason: "card_declined"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if ref, ok := m.charges[req.IdempotencyKey]; ok {
		return ref, nil
	}
	ref := "ch_" + uuid.NewString()
	m.charges[req.IdempotencyKey] = ref
	return ref, nil
}

// Refund implements app.Provider.
func (m *Mock) Refund(ctx context.Context, _, idempotencyKey string) error {
	if err := m.simulate(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refunds[idempotencyKey] = true
	return nil
}

func (m *Mock) simulate(ctx context.Context) error {
	if m.cfg.Latency > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.cfg.Latency):
		}
	}
	if rand.Float64() < m.cfg.FailureRate { //nolint:gosec // simulation, not security-sensitive
		return ErrUnavailable
	}
	return nil
}
