// Package pingdom implements the uptime-bench adapter for Pingdom.
package pingdom

// ServiceID is the stable identifier for this service.
// It must match the ID used in scenario TOML files.
const ServiceID = "pingdom"

// TODO: implement adapter.Adapter
// API docs: https://docs.pingdom.com/api/
// Auth: Bearer token (API key)
// Rate limit: 15 req/min on most plans
