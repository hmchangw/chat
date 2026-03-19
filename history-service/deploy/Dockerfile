FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY history-service/ history-service/
RUN CGO_ENABLED=0 go build -o /history-service ./history-service/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /history-service /history-service
ENTRYPOINT ["/history-service"]
