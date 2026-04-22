// Package uptimerobot implements the uptime-bench adapter for UptimeRobot.
//
// To implement:
//  1. Implement adapter.Adapter (Provision, Retrieve, Deprovision, ServiceID, Capabilities).
//  2. New(id, apiURL, apiKey string) — id is the configured instance ID from services.toml;
//     apiURL defaults to "https://api.uptimerobot.com/v2" when empty.
//  3. Provision must store Fields["service_type"] = "uptimerobot" for normalization.
//  4. Register in cmd/harness/main.go registry under "uptimerobot".
//  5. Add auth keys to services.example.toml (currently: api_key).
//
// API docs: https://uptimerobot.com/api/
// Auth: API key per account (v2 API)
// Rate limit: 10 req/min on free, higher on paid plans
package uptimerobot

// adapterType is the key in adapter.NormalizedClassification and the registry.
const adapterType = "uptimerobot"
