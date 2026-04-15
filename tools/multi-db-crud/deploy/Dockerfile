FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/multi-db-crud ./tools/multi-db-crud

FROM alpine:3.21
RUN apk --no-cache add ca-certificates
COPY --from=builder /bin/multi-db-crud /bin/multi-db-crud
EXPOSE 8091
ENTRYPOINT ["/bin/multi-db-crud"]
