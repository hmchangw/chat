# Stage 1: Build
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

# Copy the parent module (for replace directive).
COPY go.mod go.sum ./
COPY pkg/ pkg/

# Copy the auth-service module.
COPY auth-service/go.mod auth-service/go.sum ./auth-service/
WORKDIR /src/auth-service
RUN go mod download

COPY auth-service/*.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /auth-service .

# Stage 2: Runtime
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /auth-service /usr/local/bin/auth-service

ENTRYPOINT ["auth-service"]
