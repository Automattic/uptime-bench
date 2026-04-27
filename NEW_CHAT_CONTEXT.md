# New Chat Context: uptime-bench-certmint

This file is a seed for a new Codex chat started from this directory.

## Current State

- Project path: `/home/gaarai/code/uptime-bench-certmint`
- Relationship: companion producer service for `/home/gaarai/code/uptime-bench`
- Git: initial scaffold is tracked on `trunk`; check `git status` for local changes
- Language: Go 1.26, standard library only
- Verified commands:
  - `go test ./...`
  - `go vet ./...`
  - `go build -o /tmp/uptime-bench-certmint-check ./cmd/certmint`
  - `/tmp/uptime-bench-certmint-check plan -config configs/example.json`

Known environment issue: this machine's Go toolchain is installed through Snap. Some sandboxed `go`/`gofmt` invocations can fail with Snap confinement errors; rerun with approval/escalation when that happens.

## Purpose

`uptime-bench-certmint` builds a TLS certificate library for `uptime-bench` TLS expiration tests. It shells out to certbot, mints real publicly trusted Let's Encrypt certificates, snapshots certbot lineages into immutable directories, and writes a JSON manifest for `uptime-bench` to consume.

The project should remain separate from `uptime-bench` because it owns operational concerns:

- DNS-01 challenge credentials
- certbot account and lineage state
- private-key storage
- issuance cadence
- Let's Encrypt rate-limit policy
- systemd scheduling

`uptime-bench` should consume the output read-only.

## Design Intent

The ideal long-term cert library is mostly built from longer-lived standard Let's Encrypt certificates as they age naturally. The `shortlived` profile is used to build near-expiry and newly-expired coverage quickly without waiting months.

Supported issuance profiles in the scaffold:

- `classic`: no preferred profile flag; certbot uses the CA default, currently normal Let's Encrypt server certificates
- `shortlived`: passes `--preferred-profile shortlived`, producing 160-hour certificates

Wildcard certs require DNS-01 validation. The example config uses `certbot-dns-rfc2136` arguments as a placeholder.

## Rate-Limit Strategy

The config supports `unique_san_template`. Each issuance keeps the useful identifiers, such as:

- `bench.example.com`
- `*.bench.example.com`

and adds a unique SAN, such as:

- `cert-20260427-01-shortlived.bench.example.com`

This changes the exact set of identifiers for each order while preserving wildcard coverage for test hosts. It does not bypass registered-domain issuance limits; the daemon should still keep total issuance per registered domain conservative.

## Important Files

- `README.md`: operator-facing overview and commands
- `PROJECT.md`: boundary/contract with `uptime-bench`
- `configs/example.json`: example daemon config
- `deploy/systemd/uptime-bench-certmint.service`: starting systemd unit
- `cmd/certmint/main.go`: CLI entry point; commands are `plan`, `once`, `daemon`, `inspect`
- `internal/config`: JSON config loader/validator
- `internal/planner`: due-slot planner and unique SAN/cert-name generation
- `internal/certbot`: certbot argv construction and execution
- `internal/library`: archives certbot live PEM files into immutable library snapshots
- `internal/manifest`: manifest load/save and slot tracking
- `internal/certutil`: PEM certificate metadata parsing

## Current Behavior

`plan -config configs/example.json` produces due orders for the current UTC day. On 2026-04-27 it produced:

- one `classic` order for slot `00`
- two `shortlived` orders for slots `00` and `01`

The generated shortlived certbot command includes:

```text
--preferred-profile shortlived
```

`once -dry-run` prints due certbot commands without issuing certs or writing snapshots.

`once` without `-dry-run`:

1. calculates due slots
2. runs certbot if the expected certbot lineage does not exist
3. copies `cert.pem`, `chain.pem`, `fullchain.pem`, and `privkey.pem` from certbot live storage
4. parses the leaf cert's NotBefore/NotAfter/fingerprint
5. appends an entry to `<library_dir>/manifest.json`

## Manifest Contract

The manifest lives at:

```text
<library_dir>/manifest.json
```

Each entry includes:

- ID
- domain
- profile and preferred profile
- slot date and slot index
- certbot cert name
- SAN identifiers
- issued time
- NotBefore / NotAfter
- SHA-256 fingerprint
- paths to cert, chain, fullchain, and privkey PEM files

The target side in `uptime-bench` should use the manifest, not parse directory names.

## Suggested Next Tasks

1. Add tests for `internal/certbot.Args`, especially `--preferred-profile shortlived`, `--staging`, custom `--server`, and identifier ordering.
2. Add config loader/validation tests.
3. Add an archive test that generates a local test certificate fixture and verifies manifest metadata, copied files, permissions, and ID shape.
4. Decide whether to commit this scaffold locally before deeper work.
5. Add operator install docs for certbot + DNS plugin setup.
6. Add a sample RFC2136 credentials template with clear permission guidance, but do not include real secrets.
7. Decide whether to add a lock file around `once`/`daemon` so overlapping systemd/manual invocations cannot race.
8. Later, implement the `uptime-bench` consumer: TLS listener, SNI cert loading, manifest selection by `days_remaining`/`days_expired`, and selected cert metadata in ground truth.

## Useful Constraints

- Do not run production certbot issuance from tests.
- Prefer staging until DNS-01 automation is verified.
- Keep the project dependency-light unless a specific need justifies a library.
- Private keys and DNS credentials should be handled with `0600`-style permissions; the systemd unit currently sets `UMask=0077`.
- Do not modify `/home/gaarai/code/uptime-bench` unless explicitly asked in that chat.
