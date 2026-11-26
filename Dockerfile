FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS builder

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

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

RUN apk add --no-cache ca-certificates
COPY --from=builder /evidence-store /evidence-store
COPY migrations /migrations

EXPOSE 8000
ENTRYPOINT ["/evidence-store"]
