# One Dockerfile builds every service: docker build --build-arg SERVICE=event .
# The final image contains a single static binary on distroless, running as non-root.

ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src

# Download modules in their own layer so code changes do not invalidate it.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG SERVICE
ARG VERSION=dev
RUN test -n "${SERVICE}" || (echo "SERVICE build arg is required" && false)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app ./services/${SERVICE}/cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
CMD ["serve"]
