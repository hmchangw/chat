FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ pkg/
COPY broadcast-worker/ broadcast-worker/

RUN CGO_ENABLED=0 go build -o /broadcast-worker ./broadcast-worker/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /broadcast-worker /broadcast-worker

ENTRYPOINT ["/broadcast-worker"]
