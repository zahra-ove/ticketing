// Package grpcx builds gRPC servers and clients with the cross-cutting
// concerns every service needs: tracing and metrics, panic recovery,
// request logging, health checks and graceful shutdown.
package grpcx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// NewServer returns a gRPC server with OpenTelemetry instrumentation, panic
// recovery, request logging, the standard health service and reflection
// (so tools like grpcurl can discover the API).
func NewServer(log *slog.Logger) (*grpc.Server, *health.Server) {
	recoveryOpt := recovery.WithRecoveryHandlerContext(func(ctx context.Context, p any) error {
		log.ErrorContext(ctx, "panic recovered in grpc handler", slog.Any("panic", p))
		return status.Error(codes.Internal, "internal error")
	})
	logOpts := []logging.Option{
		logging.WithLogOnEvents(logging.FinishCall),
		// Client errors are normal; log them below the level of real failures.
		logging.WithLevels(func(code codes.Code) logging.Level {
			switch code {
			case codes.OK, codes.NotFound, codes.InvalidArgument, codes.AlreadyExists, codes.FailedPrecondition, codes.Canceled:
				return logging.LevelInfo
			default:
				return logging.LevelError
			}
		}),
	}

	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			logging.UnaryServerInterceptor(slogAdapter(log), logOpts...),
			recovery.UnaryServerInterceptor(recoveryOpt),
		),
	)

	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	reflection.Register(srv)
	return srv, hs
}

// Run serves srv on addr until ctx is cancelled, then stops gracefully,
// waiting at most shutdownTimeout for in-flight calls.
func Run(ctx context.Context, srv *grpc.Server, hs *health.Server, addr string, shutdownTimeout time.Duration, log *slog.Logger) error {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("grpc listen on %s: %w", addr, err)
	}

	errCh := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "grpc server listening", slog.String("addr", addr))
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("grpc server: %w", err)
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.InfoContext(ctx, "shutting down grpc server")
	hs.Shutdown() // report NOT_SERVING so clients stop sending new calls

	stopped := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(shutdownTimeout):
		log.WarnContext(ctx, "grpc graceful stop timed out; forcing stop")
		srv.Stop()
	}
	log.InfoContext(ctx, "grpc server stopped")
	return <-errCh
}

func slogAdapter(l *slog.Logger) logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		l.Log(ctx, slog.Level(lvl), msg, fields...)
	})
}
