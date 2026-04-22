// Package datadog implements the uptime-bench adapter for Datadog Synthetics.
// The service ID is "datadog-synthetics" to distinguish it from other Datadog
// products that may be added later.
package datadog

// ServiceID is the stable identifier for this service.
// It must match the ID used in scenario TOML files.
const ServiceID = "datadog-synthetics"

// TODO: implement adapter.Adapter
// API docs: https://docs.datadoghq.com/api/latest/synthetics/
// Auth: DD-API-KEY + DD-APPLICATION-KEY headers
// Rate limit: 300 req/min (varies by endpoint)
