FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY tools/mchat/ tools/mchat/
RUN CGO_ENABLED=0 go build -o /mchat ./tools/mchat/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /mchat /mchat
ENTRYPOINT ["/mchat"]
