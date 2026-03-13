# syntax=docker/dockerfile:1
# Multi-platform build: linux/amd64 and linux/arm64 (Raspberry Pi 4+).
# Build with:
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     --build-arg VERSION=$(git describe --tags --always --dirty) \
#     -t ghcr.io/gerrowadat/nomad-botherer:VERSION .

FROM --platform=${BUILDPLATFORM} golang:1.24-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILDDATE=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILDDATE} -s -w" \
    -o /out/nomad-botherer \
    ./cmd/nomad-botherer

# ── runtime image ──────────────────────────────────────────────────────────────
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S nomad-botherer && \
    adduser -S -G nomad-botherer nomad-botherer

COPY --from=builder /out/nomad-botherer /usr/local/bin/nomad-botherer

USER nomad-botherer

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/nomad-botherer"]
