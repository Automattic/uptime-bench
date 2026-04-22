// Package runner orchestrates scenario execution: provisioning monitors,
// driving failure injection on the target fleet, recording ground-truth
// events, waiting out the grace period, and collecting adapter results.
package runner

// TODO: implement Runner
//
// Responsibilities:
//   - Load a parsed scenario and resolve its target and monitors against the fleet config
//   - Call adapter.Provision for each in-scope monitor
//   - Send ActivateRequest to the target fleet member's control API for each failure block
//   - Record ground-truth failure_start events in the DB
//   - Wait for scenario duration to elapse
//   - Send DeactivateRequest for each failure block; record failure_end events
//   - Wait for grace period to elapse
//   - Call adapter.Retrieve for each monitor (respecting adapter call budgets)
//   - Write monitor_reports to the DB
//   - Call adapter.Deprovision for each monitor (even on abort)
//   - Write scenario_runs.resolution_reason on close
