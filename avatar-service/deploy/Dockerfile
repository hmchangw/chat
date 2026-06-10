FROM golang:1.25.11-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY avatar-service/ avatar-service/
RUN CGO_ENABLED=0 go build -o /avatar-service ./avatar-service/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=builder /avatar-service /avatar-service
USER app
ENTRYPOINT ["/avatar-service"]
