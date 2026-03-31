FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY history-service-demo/ history-service-demo/
RUN CGO_ENABLED=0 go build -o /demo ./history-service-demo/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /demo /demo
ENTRYPOINT ["/demo"]
