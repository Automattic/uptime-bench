// Package pingdom implements the uptime-bench adapter for Pingdom.
//
// To implement:
//  1. Implement adapter.Adapter (Provision, Retrieve, Deprovision, ServiceID, Capabilities).
//  2. New(id, apiURL, token string) — id is the configured instance ID from services.toml;
//     apiURL defaults to "https://api.pingdom.com/api/3.1" when empty.
//  3. Provision must store Fields["service_type"] = "pingdom" for normalization.
//  4. Register in cmd/harness/main.go registry under "pingdom".
//  5. Add auth keys to services.example.toml (currently: token).
//
// API docs: https://docs.pingdom.com/api/
// Auth: Bearer token (API key)
// Rate limit: 15 req/min on most plans
package pingdom

// adapterType is the key in adapter.NormalizedClassification and the registry.
const adapterType = "pingdom"
