# uptime-bench — Project Guide for Claude

## What uptime-bench is

uptime-bench is a benchmark suite for evaluating uptime monitoring services. It runs controlled failure scenarios against target endpoints, measures how each service under test detects and reports each failure, and produces structured comparison data.

It is **not** a monitoring service. It does not monitor real sites or run probes in production. It exists to answer: "How well does service X actually detect failure type Y?"

Key documents:
- [`docs/architecture.md`](docs/architecture.md) — system components and design principles
- [`docs/scenarios.md`](docs/scenarios.md) — the library of failure modes the benchmark covers
- [`docs/scenario-format.md`](docs/scenario-format.md) — scenario file format and field reference (TOML)
- [`docs/adapters.md`](docs/adapters.md) — monitor adapter interface, types, and harness responsibilities
- [`docs/events.md`](docs/events.md) — ground-truth event log and output schema
- [`docs/roadmap.md`](docs/roadmap.md) — deferred features and future work

## Coding conventions

### General

- Match existing style in the file you're editing. No drive-by refactors.
- Comments explain *why*, not *what*. The code shows what.
- No features beyond what the current task requires.

### Go (primary language)

- Standard Go idioms: `gofmt`, short variable names in small scopes, errors as values, no panics in library code.
- Error wrapping: `fmt.Errorf("context: %w", err)` — preserve the chain.
- Context is the first parameter on any function that does I/O or might be cancelled.
- Prefer small interfaces defined at the consumer, not the producer.
- Table-driven tests where it fits.

### SQL / database

- Schema changes are migrations, never edits to prior migrations.
- Metric rows are derived — never write them in the same path as raw event writes. Keep derivation separate and rerunnable.

## Architectural principles

### Event-sourced ground truth

The benchmark's record of what each scenario did is an append-only event log. Derived metrics (detection latency, accuracy, classification fidelity) are computed from the log. If a metric calculation needs to change or is found to be wrong, recompute from the log — never patch stored metric values.

### Adapters absorb service-specific complexity

Monitor adapters are the only place service-specific behavior lives: rate limits, API quirks, polling patterns, proprietary terminology. The core harness never branches on which service is under test. Adding a new service means writing a new adapter, not touching the harness.

### Unknown is not a detection failure

If an adapter cannot reach a monitoring service's API, the result is Unknown — not a false negative. Never count Unknown as a missed detection. Record why the adapter failed and propagate Unknown to derived metrics correctly.

The same rule applies to capability mismatches: when a scenario needs a feature the adapter doesn't support, the harness skips Provision and writes a row tagged `reason_code = "capability_mismatch"`. Reporting and accuracy calculations must filter these out before deriving false-negative rates. Capability-mismatch rows are *data*, not noise — they are the support matrix for the benchmark.

### Reproducibility is non-negotiable

Every scenario run must be deterministic given the same inputs. Randomized injection must be seeded and the seed recorded. Scenario definitions, target implementations, and adapter versions must all be pinned in the run record.

### Raw classification separate from normalized scores

Preserve each service's raw incident classification alongside any normalized score uptime-bench applies. The raw output is the audit record; the normalized score is for comparison. Never overwrite raw with normalized.

## What to check before shipping

- Every scenario run records a `resolution_reason` on close — no run ends without one.
- Adapters produce Unknown (not false negative) when they cannot reach a service.
- The runner produces capability_mismatch (not false negative) when a scenario needs a feature the adapter doesn't support. Reporting filters this out of accuracy metrics and queries it separately for the support matrix.
- Adapter deprovision runs even when a scenario aborts midway — no state leaks between runs.
- The seed is recorded in the run record for every run.
- No service-specific logic in the core harness — adapter only.
- Derived metric rows are never written in the same transaction as raw event rows.

## Decided

- **Language:** Go.
- **Scenario format:** TOML. Failure params are flattened into each `[[failures]]` block; the `type` field is the discriminator. No nested `params` sub-objects.
- **Initial services:** Jetmon, UptimeRobot, Pingdom, Datadog Synthetics, Better Uptime.

## Things to ask about, not assume

- Whether a new scenario requires changes to the target fleet or only to the scenario runner.
- Whether a new adapter should participate in all scenario types or only a subset.
- Rate limit and cost budget for adapter calls in the scenario under development.
