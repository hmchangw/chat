FROM golang:1.25.10-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ pkg/
COPY tools/federation-init/ tools/federation-init/

RUN CGO_ENABLED=0 go build -o /federation-init ./tools/federation-init/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=builder /federation-init /federation-init
USER app
ENTRYPOINT ["/federation-init"]
