FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY room-gatekeeper/ room-gatekeeper/
RUN CGO_ENABLED=0 go build -o /room-gatekeeper ./room-gatekeeper/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /room-gatekeeper /room-gatekeeper
ENTRYPOINT ["/room-gatekeeper"]
