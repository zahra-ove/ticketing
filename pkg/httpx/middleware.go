package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// RequestIDHeader carries the request id between clients and services.
const RequestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestIDFrom returns the request id stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func isProbe(path string) bool { return path == "/livez" || path == "/readyz" }

// Middleware wraps h with the standard chain, outermost first:
// tracing and metrics, request id, access log, panic recovery.
func Middleware(h http.Handler, log *slog.Logger) http.Handler {
	h = recoverer(log)(h)
	h = accessLog(log)(h)
	h = requestID(h)
	return otelhttp.NewHandler(h, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool { return !isProbe(r.URL.Path) }),
	)
}

// Route registers fn for pattern and names the span and metrics after the
// route template ("GET /v1/events/{id}"), never the raw path, which would
// explode metric cardinality with one series per id.
func Route(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	_, path, _ := strings.Cut(pattern, " ")
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		span := trace.SpanFromContext(r.Context())
		span.SetName(pattern)
		span.SetAttributes(semconv.HTTPRoute(path))
		if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			l.Add(semconv.HTTPRoute(path))
		}
		fn(w, r)
	}))
}

// requestID reuses a well-formed incoming request id or creates a new one, and
// echoes it in the response so that clients can quote it in bug reports.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" || len(id) > 64 {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		// Lets you find a trace in Tempo from the request id a client reports.
		trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("http.request.id", id))
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// statusRecorder captures the status code and size written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// accessLog logs one line per request. Health probes are skipped to keep the logs readable.
func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			if rec.status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			log.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
				slog.String("request_id", RequestIDFrom(r.Context())),
			)
		})
	}
}

// recoverer turns a panic into a 500 response instead of a dropped connection.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { //nolint:contextcheck // false positive: the closure uses r.Context()
				rec := recover()
				if rec == nil {
					return
				}
				// http.ErrAbortHandler is the documented way to abort a response; let it through.
				if rec == http.ErrAbortHandler { //nolint:errorlint // panic values are compared directly
					panic(rec)
				}
				log.ErrorContext(r.Context(), "panic recovered",
					slog.Any("panic", rec),
					slog.String("stack", string(debug.Stack())),
					slog.String("request_id", RequestIDFrom(r.Context())),
				)
				WriteProblem(w, r, Problem{Status: http.StatusInternalServerError})
			}()
			next.ServeHTTP(w, r)
		})
	}
}
