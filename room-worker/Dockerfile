FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY room-worker/ room-worker/
RUN CGO_ENABLED=0 go build -o /room-worker ./room-worker/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /room-worker /room-worker
ENTRYPOINT ["/room-worker"]
