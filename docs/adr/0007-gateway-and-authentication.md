# 7. API gateway with ForwardAuth and Ed25519 JWTs

Date: 2026-11-10

## Status

Accepted

## Context

Until now each service exposed its own port and trusted an `X-Customer-ID`
header sent by the client, so anyone could act as any customer and create
events. We need one entry point, authentication, roles (customer, admin), and
protection of the hot endpoints, without each service re-implementing token
handling.

## Decision

- **Traefik is the only public entry point** (`:8000`). Services have no host
  ports in Docker Compose; they are reachable only on the internal network.
  Routes, middlewares and rate limits live in a reviewed file
  (`deploy/compose/gateway/dynamic.yml`), not in container labels.
- **An auth service issues JWTs** signed with **Ed25519** (`EdDSA`): small
  tokens, fast signatures, and only the public key is needed to verify, which
  is published at `/.well-known/jwks.json`. Tokens carry `sub` (user id),
  `role`, `iss`, `aud`, `exp` and `jti`, and live 15 minutes. Verification
  pins the algorithm, so `alg: none` and key-confusion tokens are rejected.
  The user directory is a configuration-backed stand-in for a real identity
  provider (Keycloak, Auth0); nothing else in the system depends on it.
- **ForwardAuth.** For every request the gateway first **removes**
  `X-Customer-ID` and `X-User-Role` from the incoming request, then calls the
  auth service's `/v1/verify`:
  - no token → 200, anonymous (public routes such as the catalog work);
  - valid token → 200 with the identity headers, which Traefik copies onto
    the forwarded request;
  - invalid or expired token → 401 to the client; a bad token is never
    downgraded to anonymous.
- **Authorization stays in the services**, next to the data it protects:
  writing the catalog requires `admin` (`httpx.RequireRole`); bookings and
  payments are visible only to their owner (others get 404, so ids cannot be
  probed), and admins can read any payment.
- **Rate limits** at the edge: seat holds per token (10/s, burst 20), login per
  client IP (5/s, burst 10).
- The gateway exports traces and metrics over OTLP, so every trace starts at
  the edge and includes the token check.

## Consequences

- Services stay simple: they read two headers and never parse tokens.
- Trusting headers is only safe because services are not reachable except
  through the gateway. In Kubernetes the same guarantee would come from
  network policies, and a service mesh with mTLS would add service identity.
  Services could also verify the JWT themselves with the JWKS (defence in
  depth) at the cost of duplicating verification.
- ForwardAuth adds one internal call per request (sub-millisecond on the local
  network). Caching verification results, or verifying the JWT in the gateway
  with a plugin, would remove it.
- Tokens cannot be revoked before they expire; the short lifetime limits the
  damage. A refresh-token flow is left out of scope.
