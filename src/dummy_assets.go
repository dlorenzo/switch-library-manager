//go:build dev

package main

// Dummy definitions for the astilectron-bundler generated symbols (Asset,
// AssetDir, RestoreAssets) so plain `go build`, `go vet`, and `govulncheck`
// can run without first running the bundler.
//
// IMPORTANT: This file is gated behind the "dev" build tag so it is EXCLUDED
// from the bundler build in CI. The bundler generates bind.go / bind_<os>_<arch>.go
// files that define these names as functions; without the tag, the two
// declarations collide and the build fails with "Asset redeclared in this block".
//
// Local development:
//   go build -tags dev ./...
//   go vet   -tags dev ./...
var (
	Asset         func(name string) ([]byte, error)
	AssetDir      func(name string) ([]string, error)
	RestoreAssets func(dir, name string) error
)
