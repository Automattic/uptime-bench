// Package uptimerobot implements the uptime-bench adapter for UptimeRobot.
package uptimerobot

// ServiceID is the stable identifier for this service.
// It must match the ID used in scenario TOML files.
const ServiceID = "uptimerobot"

// TODO: implement adapter.Adapter
// API docs: https://uptimerobot.com/api/
// Auth: API key per account (v2 API)
// Rate limit: 10 req/min on free, higher on paid plans
