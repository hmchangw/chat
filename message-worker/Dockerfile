FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY message-worker/ message-worker/
RUN CGO_ENABLED=0 go build -o /message-worker ./message-worker/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /message-worker /message-worker
ENTRYPOINT ["/message-worker"]
