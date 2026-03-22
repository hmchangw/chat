FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ pkg/
COPY inbox-worker/ inbox-worker/

RUN CGO_ENABLED=0 go build -o /inbox-worker ./inbox-worker/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /inbox-worker /inbox-worker

ENTRYPOINT ["/inbox-worker"]
