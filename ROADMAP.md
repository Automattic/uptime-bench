# uptime-bench Roadmap

Deferred features that are intentionally not yet implemented. Items here have been accounted for in the schema and data model — they can be added without breaking changes — but the implementation work has been deferred.

---

## Staggered failure start times

**Status:** Schema-ready, not implemented.

The `offset` field is defined on every `[[failures]]` block and validated by the schema parser, but the runner ignores it. All failures currently start simultaneously at scenario start regardless of what `offset` is set to.

**What it enables:**

- Models realistic cascading failures where one layer degrades before another (e.g., DNS latency appears 30 seconds before TCP connections start failing).
- Tests detection sensitivity: does a monitor fire on the first failing layer, or only after multiple layers compound?
- Enables recovery-and-re-failure within a single run without requiring two separate scenarios.

**What needs to be built:**

- *Runner:* schedule each failure block's injection start at `scenario_start + offset` rather than injecting all failures at once.
- *Ground-truth log:* already correct — each failure block emits its own `failure_start` and `failure_end` events with the actual timestamps.
- *Measurement engine:* detection latency must be calculated against the right `failure_start` event. When failures are staggered, "which failure did the monitor respond to?" becomes the hard question. The metric calculation needs a matching rule — either the earliest active failure or the failure whose classification best matches the monitor's reported classification.

**Complexity note:** the measurement engine change is the substantive work, not the runner scheduling. Design the matching rule before implementing.
