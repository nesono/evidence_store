FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The version the UI shows (issue #111): the minute this binary was linked, in
# UTC. Evaluated inside the build, so it moves only when this layer actually
# rebuilds — a cached layer keeps its stamp, which is right, because the binary
# it produced has not changed either.
RUN CGO_ENABLED=0 go build \
    -ldflags "-X github.com/nesono/evidence-store/internal/version.stamp=$(date -u +%Y.%m.%d.%H.%M)" \
    -o /evidence-store ./cmd/server

FROM alpine:3.23

RUN apk add --no-cache ca-certificates
COPY --from=builder /evidence-store /evidence-store
COPY migrations /migrations

EXPOSE 8000
ENTRYPOINT ["/evidence-store"]
