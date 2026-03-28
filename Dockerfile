FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /evidence-store ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates
COPY --from=builder /evidence-store /evidence-store
COPY migrations /migrations

EXPOSE 8000
ENTRYPOINT ["/evidence-store"]
