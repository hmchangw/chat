.PHONY: lint fmt test test-integration generate build

# Makefile for the distributed multi-site chat system.

# Run golangci-lint (includes go vet, staticcheck, errcheck, goimports, etc.)
lint:
	golangci-lint run ./...

# Run goimports via golangci-lint to format all .go files
fmt:
	golangci-lint fmt ./...

# Run all unit tests with race detector (excludes integration tests)
test:
ifdef SERVICE
	go test -race ./$(SERVICE)/...
else
	go test -race ./...
endif

# Run integration tests (requires Docker)
test-integration:
ifdef SERVICE
	go test -race -tags integration ./$(SERVICE)/...
else
	go test -race -tags integration ./...
endif

# Regenerate all mocks via go generate
generate:
ifdef SERVICE
	go generate ./$(SERVICE)/...
else
	go generate ./...
endif

# Build a single service binary (requires SERVICE=<name>)
build:
ifndef SERVICE
	$(error SERVICE is required. Usage: make build SERVICE=<name>)
endif
ifeq ($(SERVICE),history-service)
	CGO_ENABLED=0 go build -o bin/$(SERVICE) ./$(SERVICE)/cmd/
else
	CGO_ENABLED=0 go build -o bin/$(SERVICE) ./$(SERVICE)/
endif
