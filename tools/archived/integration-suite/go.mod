// Separate module so the archived single-site suite leaves the root
// module's ./... package set. It is a FROZEN feature reference (see
// STALE.md), not maintained, and imports live github.com/hmchangw/chat
// packages that drift as main evolves — keeping it in the root module
// made every such drift (e.g. the room-key Valkey->Mongo move, #285)
// break repo-wide `make lint` / `make test` / `go build ./...`. This
// boundary stops that: root tooling no longer descends here.
//
// Intentionally minimal — no require graph / go.sum. The archived tool
// is not built standalone; this file exists only to create the module
// boundary. If you ever need to build it for reference, add a
// `replace github.com/hmchangw/chat => ../../..` plus `go mod tidy`,
// but expect API drift against current main.
module github.com/hmchangw/chat/tools/archived/integration-suite

go 1.25.11
