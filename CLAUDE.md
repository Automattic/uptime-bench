# Jetmon — Project Guide for Claude

This file gives Claude Code the architectural context and conventions for the Jetmon project. Read this before making changes.

## What Jetmon is

Jetmon monitors sites and detects outages. It runs probes against sites, records results, and surfaces state transitions (up → seems down → down → resolved) with appropriate severity.

## Core architectural decisions

### Events are the source of truth

Site status is **event-sourced**. An event has:

- `start_timestamp` — when the condition began
- `end_timestamp` — when it resolved (nullable while active)
- `severity` — numeric, allows ordering and threshold logic
- `state` — human-readable label derived from the event lifecycle

Do **not** treat `state` as a standalone column that gets mutated in place on the site row as the primary record. The event log is canonical.

### Site row holds a denormalized projection

The site row stores the current derived state (for fast reads, dashboards, queries). This denormalized field is updated **transactionally** alongside the event write — they must not drift. If you're updating one, you're updating the other in the same transaction.

### Severity and state are separate concerns

- **Severity** is numeric — use it for ordering, thresholds, escalation rules.
- **State** is a human-readable label — use it for display and lifecycle transitions.

Don't collapse them into one field. A single event can have its severity updated in place (e.g., a degradation worsens) without changing its state.

### "Seems Down" is the key transient state

Between the first probe failure and verifier confirmation, a site is in **Seems Down**. This is a real, named state — not an implementation detail. Treat it as a first-class lifecycle stage:

```
Up → Seems Down → Down → Resolved
         ↓
         Up (false alarm, verifier disagrees)
```

### Events update in place; identity is idempotent

When severity changes mid-event, update the existing event row rather than closing and opening a new one. Event identity must be idempotent so that retries and duplicate probe results don't create duplicate events.

### Record resolution reason

When an event ends, record **why** it ended (verifier cleared, manual override, auto-timeout, etc.). Don't just null out `end_timestamp`'s counterpart — capture the cause.

### Causal links are separate from rollup

If event B was caused by event A (e.g., a DNS failure cascading into HTTP failures), store that causal link in a dedicated structure. Do **not** conflate causal links with rollup/aggregation logic — those are different concerns with different query patterns.

### Deduplication lives in the shared probe runner

All probe types share a single runner that handles deduplication. Don't reimplement dedup per probe type — if you're adding a new probe, plug it into the runner.

## Coding conventions

### General

- Match existing style in the file you're editing. Don't introduce a new pattern just because you prefer it.
- No drive-by refactors. If you spot something worth fixing outside the current task, flag it separately.
- Comments explain *why*, not *what*. The code shows what.

### Go (primary backend language)

- Follow standard Go idioms: `gofmt`, short variable names in small scopes, errors as values, no panics in library code.
- Error wrapping with `fmt.Errorf("context: %w", err)` — preserve the chain.
- Context is the first parameter on any function that does I/O or might be cancelled.
- Prefer small interfaces defined at the consumer, not the producer.
- Table-driven tests where it fits.

### C++ (legacy components)

- Match the existing style of the file — indentation, brace placement, naming.
- Prefer RAII; avoid raw `new`/`delete` in new code.

### SQL / MySQL

- Schema changes are migrations, never edits to prior migrations.
- Every event-writing code path must update the site row projection in the same transaction.
- Index for the read patterns the dashboard actually uses, not hypothetical ones.

## What to check before shipping

- Event writes and site-row updates are in one transaction.
- New probe types register with the shared runner (and its dedup).
- Severity changes update events in place — no spurious close/open.
- Resolution reason is recorded on every event close.
- State transitions through "Seems Down" correctly, including the false-alarm path back to Up.

## Things to ask about, not assume

- Retention policy for closed events.
- Who consumes the causal link graph and what shape they need it in.
- Whether a new probe type should contribute to rollup severity or stand alone.

## Testing

- Unit tests for the state machine transitions — especially the Seems Down → Up false-alarm path.
- Integration tests that exercise the transactional write (event + site-row projection) and verify they stay in sync under concurrent probe results.
- Idempotency tests: replay the same probe result twice, assert no duplicate event.
