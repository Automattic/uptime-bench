# uptime-bench docs

The root [README](../README.md) explains what uptime-bench is and why it exists. This directory holds the detailed operator, developer, and design references.

## Start Here

- [Architecture](architecture.md) - system components, data flow, and design principles.
- [Fleet overview](fleet-overview.md) - what each deployed server does and how fleet traffic moves.
- [Testing guide](testing.md) - local end-to-end setup with Docker Compose.
- [Operations guide](operations.md) - provisioning, deploys, credentials, smoke tests, and live fleet operation.

## Benchmark Model

- [Scenarios](scenarios.md) - the shipped failure library and what each scenario tests.
- [Scenario format](scenario-format.md) - TOML schema, field reference, and failure parameters.
- [Events and metrics](events.md) - ground truth, monitor reports, derived metrics, and scoring rules.
- [Reporting standard](reporting.md) - required post-run report bundle, raw artifacts, capacity artifacts, and report contents.
- [Jetmon capacity benchmark](capacity-benchmark.md) - Prometheus collection and capacity-test design for Jetmon v1/v2.
- [Roadmap](roadmap.md) - completed milestones, active priorities, and deferred ideas.

## Extension Points

- [Adapters](adapters.md) - interface contract for adding or maintaining monitoring-service adapters.
- [Inter-run state design](inter-run-state-design.md) - maintenance windows, cooldown reset, and suppression semantics.
- [Certmint operator guide](certmint-operator.md) - running the certificate-library producer.

## Related References

- [ACME hook scripts](../deploy/acme-hooks/README.md) - certbot manual DNS hooks used by certmint.
- [Example campaign config](../configs/campaign/example.toml) - a runner-safe starter campaign.
- [Example services config](../services.example.toml) - service adapter configuration shape.
- [Example fleet config](../fleet.example.toml) - target, DNS, domain, and certmint configuration shape.
