package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// NewServer returns an http.Server with timeouts that protect against slow
// clients (Slowloris) and stuck connections.
func NewServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
}

// Run serves srv until ctx is cancelled, then drains in-flight requests for at
// most shutdownTimeout. It returns nil after a clean shutdown.
func Run(ctx context.Context, srv *http.Server, shutdownTimeout time.Duration, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err // failed to start, e.g. port in use
	case <-ctx.Done():
	}

	log.InfoContext(ctx, "shutting down http server")
	// ctx is already cancelled; derive a fresh deadline that keeps its values.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}
	log.InfoContext(shutdownCtx, "http server stopped")
	return <-errCh
}

// Healthcheck probes /livez of a server listening on addr. It lets a
// distroless container (no curl, no shell) check itself.
func Healthcheck(ctx context.Context, addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse address %q: %w", addr, err)
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/livez", http.NoBody)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe /livez: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // nothing useful to do with a close error on a probe

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe /livez: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Pinger reports whether a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// PingerFunc adapts a function to Pinger.
type PingerFunc func(ctx context.Context) error

// Ping implements Pinger.
func (f PingerFunc) Ping(ctx context.Context) error { return f(ctx) }

// Livez reports that the process is up. It never checks dependencies, so a
// database outage does not make the orchestrator restart healthy containers.
func Livez(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readyz returns a handler that reports whether every dependency answers.
func Readyz(log *slog.Logger, deps map[string]Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status, body := http.StatusOK, map[string]string{"status": "ok"}
		for name, dep := range deps {
			if err := dep.Ping(ctx); err != nil {
				log.WarnContext(ctx, "readiness check failed", slog.String("dependency", name), slog.Any("error", err))
				status, body["status"], body[name] = http.StatusServiceUnavailable, "unavailable", "down"
				continue
			}
			body[name] = "up"
		}
		WriteJSON(w, status, body)
	}
}
