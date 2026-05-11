//go:build e2e

package harness

import (
	// Imported eagerly so `go mod tidy` keeps the compose module in go.mod
	// before chapter 7 fills in StartStack. Chapter 7 swaps the blank
	// import for real symbol references.
	_ "github.com/testcontainers/testcontainers-go/modules/compose"
)
