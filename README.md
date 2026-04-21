# Uptime Bench

A benchmark suite for evaluating uptime monitoring services.

`uptime-bench` runs controlled failure scenarios against monitoring providers and measures how quickly and accurately each one detects, classifies, and reports incidents. It's designed to produce apples-to-apples comparisons across services that otherwise expose very different dashboards, alerting semantics, and terminology.

## Why

Uptime monitoring services are hard to compare. Vendors publish their own uptime numbers, their detection intervals vary, their definitions of "down" differ, and their alerting behavior under flapping, partial outages, or DNS weirdness is rarely documented. `uptime-bench` provides a reproducible way to put them side by side.

## What it does

- Spins up controllable target endpoints that can simulate specific failure modes (hard down, slow response, intermittent failure, TLS errors, DNS failures, partial regional outages, etc.)
- Points one or more monitoring services at those targets
- Records what each service detects, when it detects it, and how it reports the incident
- Produces a structured comparison across services and scenarios

## Status

Early development. Scope, scenario taxonomy, and output schema are still being nailed down. Not yet usable as a drop-in tool.

## Scope

In scope:

- Detection latency (time from failure to alert)
- Detection accuracy (false positives, missed incidents, flapping behavior)
- Incident classification and reporting fidelity
- Behavior under ambiguous conditions (slow responses, intermittent failures, DNS issues)

Out of scope (for now):

- Dashboard or UI evaluation
- Pricing or plan comparisons
- Load testing the monitoring services themselves

## Services under evaluation

To be determined. The suite is designed to be service-agnostic — adding a new provider means implementing an adapter that knows how to configure monitors and pull incident data from that service's API.

## Contributing

Not yet accepting contributions while the core design stabilizes. Issues and discussion are welcome.

## License

GPL v2.0. See [LICENSE](LICENSE) for details.
