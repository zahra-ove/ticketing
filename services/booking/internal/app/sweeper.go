package app

import (
	"context"
	"log/slog"
	"time"
)

// Expirer cancels bookings whose payment never arrived.
type Expirer interface {
	ExpireUnpaidBookings(ctx context.Context, timeout time.Duration, limit int) (int, error)
}

// RunPaymentTimeoutSweeper periodically cancels bookings that have waited
// longer than timeout for their payment. It is the saga's safety net for a
// payment service that is down or a message that was lost.
func RunPaymentTimeoutSweeper(ctx context.Context, e Expirer, interval, timeout time.Duration, log *slog.Logger) error {
	const batch = 100
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.InfoContext(ctx, "payment timeout sweeper started",
		slog.Duration("interval", interval), slog.Duration("timeout", timeout))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		n, err := e.ExpireUnpaidBookings(ctx, timeout, batch)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "expiring unpaid bookings failed", slog.Any("error", err))
			continue
		}
		if n > 0 {
			log.InfoContext(ctx, "expired unpaid bookings", slog.Int("count", n))
		}
	}
}
