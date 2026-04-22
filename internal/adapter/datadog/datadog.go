// Package datadog implements the uptime-bench adapter for Datadog Synthetics.
//
// To implement:
//  1. Implement adapter.Adapter (Provision, Retrieve, Deprovision, ServiceID, Capabilities).
//  2. New(id, apiURL, apiKey, appKey string) — id is the configured instance ID from
//     services.toml; apiURL defaults to "https://api.datadoghq.com" when empty.
//  3. Provision must store Fields["service_type"] = "datadog-synthetics" for normalization.
//  4. Register in cmd/harness/main.go registry under "datadog-synthetics".
//  5. Add auth keys to services.example.toml (currently: api_key, app_key).
//
// API docs: https://docs.datadoghq.com/api/latest/synthetics/
// Auth: DD-API-KEY + DD-APPLICATION-KEY headers
// Rate limit: 300 req/min (varies by endpoint)
package datadog

// adapterType is the key in adapter.NormalizedClassification and the registry.
const adapterType = "datadog-synthetics"
