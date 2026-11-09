package grpcx

import (
	"fmt"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// retryPolicy retries calls that failed because the server was briefly
// unreachable (restart, deploy). Only UNAVAILABLE is retried: it is safe for
// any call because the request never reached the handler.
const retryPolicy = `{
  "methodConfig": [{
    "name": [{}],
    "timeout": "3s",
    "retryPolicy": {
      "maxAttempts": 3,
      "initialBackoff": "0.1s",
      "maxBackoff": "1s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

// NewClient returns a connection to target with tracing, a default timeout
// and retries on UNAVAILABLE. Traffic is plaintext: services talk on a
// private network; mTLS would be added by a service mesh in production.
func NewClient(target string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithDefaultServiceConfig(retryPolicy),
	)
	if err != nil {
		return nil, fmt.Errorf("create grpc client for %s: %w", target, err)
	}
	return conn, nil
}
