# syntax=docker/dockerfile:1

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /nats-debug ./nats-debug/

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /nats-debug /usr/local/bin/nats-debug

EXPOSE 8090

ENTRYPOINT ["nats-debug"]
