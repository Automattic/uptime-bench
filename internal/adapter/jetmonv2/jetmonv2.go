// Package jetmonv2 is a placeholder for the Jetmon 2 adapter.
//
// Jetmon 2 exposes no public HTTP API yet (see ../../jetmon/ROADMAP.md,
// "Public REST API — Not started"). Until that lands, the `jetmon-v2`
// service type is registered only so it can appear in services.toml;
// building it returns ErrNotImplemented.
//
// When the Jetmon 2 API is available, replace this stub with a real
// adapter and update the registry entry in cmd/harness/main.go.
package jetmonv2

import "errors"

// ErrNotImplemented is returned by New until Jetmon 2 ships a public API.
var ErrNotImplemented = errors.New("jetmon-v2: adapter not implemented — blocked on Jetmon 2 public API")
