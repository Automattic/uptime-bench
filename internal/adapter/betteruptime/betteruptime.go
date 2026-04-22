// Package betteruptime implements the uptime-bench adapter for Better Uptime.
//
// To implement:
//  1. Implement adapter.Adapter (Provision, Retrieve, Deprovision, ServiceID, Capabilities).
//  2. New(id, apiURL, token string) — id is the configured instance ID from services.toml;
//     apiURL defaults to "https://betteruptime.com/api/v2" when empty.
//  3. Provision must store Fields["service_type"] = "better-uptime" for normalization.
//  4. Register in cmd/harness/main.go registry under "better-uptime".
//  5. Add auth keys to services.example.toml (currently: token).
//
// API docs: https://betteruptime.com/api/
// Auth: Bearer token
// Rate limit: 60 req/min
package betteruptime

// adapterType is the key in adapter.NormalizedClassification and the registry.
const adapterType = "better-uptime"
