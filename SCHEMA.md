# uptime-bench Scenario Schema

Scenarios are defined as TOML files. Each file defines one scenario — a controlled failure (or set of failures) injected against a target endpoint, with the parameters needed to run it and record results.

## Duration strings

All duration fields use Go's `time.ParseDuration` format: a number followed by a unit suffix. Valid units: `ms` (milliseconds), `s` (seconds), `m` (minutes), `h` (hours). Units may be combined. Examples: `"90s"`, `"1m30s"`, `"500ms"`, `"2h"`. Fractional values are supported: `"1.5s"`.

---

## Top-level fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `id` | string | yes | — | Stable kebab-case identifier for the scenario. Does not include version. |
| `version` | string | yes | — | Version of this scenario definition. Increment when fields change in a way that affects results. |
| `description` | string | no | — | Human-readable summary of what the scenario tests. |
| `target` | string | yes | — | ID of the target endpoint to inject failures against. |
| `monitors` | array of strings | yes | — | Service instance IDs to evaluate, matching the `id` fields in `services.toml` (e.g. `["jetmon-v1", "pingdom"]`). Only enabled services are used; disabled services with a matching ID are skipped. |
| `check_frequency` | duration string | yes | — | Check interval configured for all monitors during this run. |
| `grace_period` | duration string | yes | — | Time allowed after failure injection ends for monitors to resolve the incident. |
| `duration` | duration string | yes | — | How long failure injection is active. All `[[failures]]` blocks run for this duration. |
| `seed` | integer | no | random | Random seed for reproducible injection. If omitted, the runner generates a seed and records it in the run output. Always specify for formal comparison runs. |
| `keyword` | string | conditional | canary string | Scenario-level keyword used by both target and monitor for `http_body` content scenarios. Required when any failure is `content = "keyword_injected"` (no default — it is the bad string being injected). Defaults to `"uptime-bench-canary"` for other content variants. |
| `keyword_check` | string | no | inferred | One of `"present"` or `"absent"`. `"present"` means the monitor alerts when `keyword` is missing from the body (the canary case). `"absent"` means the monitor alerts when `keyword` is found (the injected-bad-keyword case). Defaults to `"absent"` if any failure is `keyword_injected`, otherwise `"present"`. |

### Optional `[maintenance]` block

Declares a vendor-side alert-suppression window the harness asks the monitor to honour during this run. Tests whether the monitor correctly silences alerts during the declared window. Adapters that lack this capability are skipped at provision time with `reason_code = "capability_mismatch"`. See [`docs/inter-run-state-design.md`](docs/inter-run-state-design.md) for the full design.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `start_offset` | duration string | no | `"0s"` | How far after scenario start the window opens. Must be non-negative. |
| `duration` | duration string | yes | — | How long the window stays open. Must be positive. |

```toml
[maintenance]
start_offset = "0s"
duration     = "300s"
```

The window is converted to absolute timestamps at provision time using the run's recorded start time as the reference; a few seconds of drift between scenario activation and window start is expected because vendor maintenance APIs are minute-grained.

---

## Failure blocks

Each `[[failures]]` block defines one failure mode to inject. A scenario must contain at least one. Multiple blocks run simultaneously for the full `duration`.

### Common fields

These fields apply to every failure type.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `type` | string | yes | — | Failure type discriminator. See types below. |
| `rate` | float | no | `1.0` | Fraction of requests affected, in the range (0.0, 1.0]. `1.0` affects every request. `0.4` affects roughly 40%. Zero is not permitted — a zero-rate failure injects nothing. |
| `regions` | list of strings | no | *(all)* | Restrict this failure to probes from specific geographic regions. Each name must match a key in `probe_ranges` in `services.toml`. When set, the failure is applied only to connections from source IPs in those regions; probes from other regions see a healthy response. Omit to affect all traffic regardless of origin. |
| `offset` | duration string | no | `"0s"` | Time after scenario start before this failure activates. The failure runs for `duration` starting from `offset`, so a 300s scenario with one failure at `offset = "60s"` is active from t=60s to t=360s. Use this to stagger failures within a single scenario (e.g. DNS issue starts at t=0, HTTP error joins at t=30s). |

---

## HTTP failure types

### `http_status`

Returns a specific HTTP status code instead of a normal response.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `status_code` | integer | yes | — | HTTP status code to return. Typically a 4xx or 5xx value. Must be a valid three-digit HTTP status code. |

```toml
[[failures]]
type        = "http_status"
status_code = 503
rate        = 0.40
```

---

### `http_timeout`

Delays or withholds the response at a specific phase of the HTTP exchange.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `phase` | string | yes | — | Which phase to delay: `"ttfb"` (time to first byte — headers are withheld), `"body"` (headers sent promptly, body transfer stalls), `"total"` (entire response withheld). |
| `delay` | duration string | yes | — | How long to stall the affected phase before responding or closing the connection. |

```toml
[[failures]]
type  = "http_timeout"
phase = "ttfb"
delay = "10s"
```

---

### `http_partial`

Accepts the connection and begins responding, then closes it mid-response (truncated body, 200 OK).

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `truncate_after_bytes` | integer | no | random | Close the connection after this many bytes of the response body. If omitted, the truncation point is randomised per request within the run seed. |

```toml
[[failures]]
type                  = "http_partial"
truncate_after_bytes  = 512
```

---

### `http_redirect`

Injects a broken redirect that monitors following redirects will fail to resolve.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `variant` | string | yes | — | `"loop"` (A → B → A redirect cycle) or `"chain"` (redirect chain exceeding the monitor's follow limit). |
| `chain_length` | integer | no | `15` | For `variant = "chain"`: number of hops in the redirect chain. Should exceed the monitor's max-redirect limit (typically > 10). Ignored when `variant = "loop"`. |

```toml
[[failures]]
type    = "http_redirect"
variant = "loop"
```

---

### `http_body`

Returns a modified response body with a 200 OK status, simulating silent application failures, content integrity violations, and security compromises. All variants preserve the HTTP status code so that status-only monitors cannot detect them — only monitors that inspect response bodies will fire.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `content` | string | yes | — | Content variant. See variant table below. |

`http_body` failures coordinate with the scenario-level `keyword` and `keyword_check` fields (see the top-level Scenario section). The target uses `keyword` to know what string to remove (`keyword_missing`) or inject (`keyword_injected`); the monitor uses `keyword` + `keyword_check` to know what body content to alert on. They are not separate per-failure fields.

#### Content variants

| `content` value | Description | Detectable by |
|-----------------|-------------|---------------|
| `"empty"` | Near-empty body (`<html></html>`), 200 OK. Simulates white-screen-of-death. | Keyword check, body-size threshold |
| `"error_page"` | CMS database error page ("Error establishing a database connection"), 200 OK. | Keyword check, error-page pattern |
| `"keyword_missing"` | Otherwise-normal page body with the expected keyword absent. The scenario-level `keyword` names which string is removed; defaults to the canary string. | Keyword check |
| `"keyword_injected"` | Otherwise-normal page body with an unexpected keyword injected (e.g., `"HACKED"`, `"BTC"`, `"ENCRYPTED"`). The scenario-level `keyword` names what is injected; required (no default). | Keyword check for unexpected term |
| `"ransomware"` | Complete page replacement with a ransomware/extortion notice. Simulates a full site takeover by malware. | Keyword check, content diff, page-text analysis |
| `"defacement"` | Complete page replacement with hacktivist defacement content. Simulates a compromised web server. | Keyword check, content diff |
| `"malicious_script"` | Otherwise-normal page with an injected `<script>` tag pointing to an external malicious-looking domain. Simulates an XSS/supply-chain compromise. | Script-injection check, external resource check |
| `"spam_links"` | Otherwise-normal page with hidden SEO spam links injected (gambling, pharmacy, etc.). Simulates a blackhat SEO compromise. | Keyword check, link-injection check |

```toml
keyword       = "Welcome"
keyword_check = "present"

[[failures]]
type    = "http_body"
content = "keyword_missing"
```

```toml
[[failures]]
type    = "http_body"
content = "ransomware"
# keyword and keyword_check default to "uptime-bench-canary" / "present"
```

```toml
[[failures]]
type    = "http_body"
content = "malicious_script"
```

```toml
keyword       = "HACKED"
keyword_check = "absent"

[[failures]]
type    = "http_body"
content = "keyword_injected"
```

---

## TCP failure types

### `tcp_refused`

Closes incoming connections immediately, simulating a closed port.

No type-specific fields.

```toml
[[failures]]
type = "tcp_refused"
```

---

### `tcp_timeout`

Accepts the TCP SYN but sends no SYN-ACK, causing the monitor's connection attempt to time out.

No type-specific fields.

```toml
[[failures]]
type = "tcp_timeout"
rate = 0.50
```

---

## DNS failure types

### `dns_nxdomain`

Returns NXDOMAIN for the target domain — the domain does not exist.

No type-specific fields.

```toml
[[failures]]
type = "dns_nxdomain"
```

---

### `dns_servfail`

Returns SERVFAIL from the authoritative nameserver.

No type-specific fields.

```toml
[[failures]]
type = "dns_servfail"
```

---

### `dns_timeout`

Returns no response from the nameserver, causing the resolver to time out.

No type-specific fields.

```toml
[[failures]]
type = "dns_timeout"
```

---

### `dns_cname_nxdomain`

The CNAME chain for the target domain resolves successfully, but the final target of the chain is NXDOMAIN.

No type-specific fields.

```toml
[[failures]]
type = "dns_cname_nxdomain"
```

---

### `dns_latency`

DNS resolution succeeds but with added artificial delay. Tests whether monitors separately measure DNS lookup time and whether they alert on latency alone.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `added_latency` | duration string | yes | — | Latency added to each DNS resolution. The resolution still succeeds. |

```toml
[[failures]]
type          = "dns_latency"
added_latency = "2000ms"
```

---

### `dns_ns_unavailable`

Takes one or more of the fleet's authoritative nameservers offline. Unlike other DNS failure types — which control what the nameserver *responds with* — this type controls whether the nameserver responds at all. Resolvers attempting to reach an affected nameserver receive no reply and must time out before trying the next NS record.

This failure type requires the fleet to run at least two authoritative nameservers. It is controlled via the DNS control plane, not the target server control plane.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `mode` | string | no | `"silent"` | How the affected nameserver(s) fail. `"silent"` — stop responding to DNS queries (resolvers time out). `"servfail"` — continue responding but return SERVFAIL for the affected domain. |

**`rate` interpretation for this type:** fraction of the fleet's authoritative nameservers to take offline. `1.0` takes all NS servers down (domain becomes unresolvable). `0.5` with two NS servers takes one down (tests resolver fallback). The harness selects which servers to affect; the selection is stable within a run.

```toml
[[failures]]
type = "dns_ns_unavailable"
```

```toml
[[failures]]
type = "dns_ns_unavailable"
mode = "servfail"
rate = 0.5
```

---

## TLS failure types

### `tls_expired`

Serves a certificate that has passed its NotAfter date.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `days_expired` | integer | no | `1` | How many days past its NotAfter date the certificate is. |

```toml
[[failures]]
type        = "tls_expired"
days_expired = 7
```

---

### `tls_expiring`

Serves a certificate that is valid today but approaching expiry. Tests whether monitors fire warning alerts before a cert actually breaks.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `days_remaining` | integer | yes | — | Days until the certificate expires. Use values near common monitor alert thresholds to test boundary behaviour: `29`, `13`, `6`. |

```toml
[[failures]]
type           = "tls_expiring"
days_remaining = 6
```

---

### `tls_invalid`

Serves a certificate that fails client validation.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `variant` | string | no | `"self_signed"` | `"self_signed"` (certificate is self-signed) or `"hostname_mismatch"` (certificate CN/SAN does not cover the requested hostname). |

```toml
[[failures]]
type    = "tls_invalid"
variant = "hostname_mismatch"
```

---

### `tls_handshake`

Fails the TLS handshake before certificate inspection, simulating a protocol incompatibility.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `reason` | string | no | `"version_mismatch"` | `"version_mismatch"` (client and server share no common TLS version) or `"no_common_cipher"` (no shared cipher suite). |

```toml
[[failures]]
type   = "tls_handshake"
reason = "no_common_cipher"
```

**Note:** The target HTTPS listener implements this by aborting per-handshake configuration selection before a certificate is chosen. Clients generally see only a TLS handshake failure; the configured `reason` is preserved for scenario intent and target-side diagnostics.

---

### `tls_deprecated`

Serves the HTTPS connection using only a deprecated TLS protocol version (TLS 1.0 or TLS 1.1). The connection succeeds — the monitor is not blocked — but uses a deprecated cipher. Tests whether monitors detect and report advisory-level TLS version warnings separately from hard failures.

This is distinct from `tls_handshake`: the handshake completes and the monitor receives an HTTP response, but via a protocol version that browsers and security scanners flag. Some monitors classify this as a warning rather than a downtime event.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `variant` | string | no | `"TLS11"` | Maximum TLS version the server offers. `"TLS10"` (TLS 1.0 only) or `"TLS11"` (TLS 1.0 and 1.1, but not 1.2 or 1.3). |

```toml
[[failures]]
type    = "tls_deprecated"
variant = "TLS11"
```

**Note:** The target HTTPS listener implements this by selecting a per-handshake TLS configuration. End-to-end monitor acceptance coverage is tracked in [ROADMAP.md](ROADMAP.md).

---

## Validation rules

- `rate` must be in the range (0.0, 1.0]. A value of exactly 0.0 is rejected — use the absence of a failure block instead.
- `offset` must be non-negative.
- `duration`, `check_frequency`, and `grace_period` must be positive.
- `check_frequency` should be less than `duration`. If not, no monitor checks occur during the failure window and the run produces no useful data.
- `status_code` for `http_status` must be a valid three-digit HTTP status code.
- `days_remaining` for `tls_expiring` must be a positive integer.
- `chain_length` for `http_redirect` with `variant = "chain"` must exceed the monitor's max-redirect follow limit (typically > 10) to actually trigger the failure.
- Scenario-level `keyword` is required when any failure has `http_body.content = "keyword_injected"` (it is the string being injected). For other content variants, it defaults to the canary string.
- `keyword_check` is one of `"present"` or `"absent"`; defaults to `"absent"` when any failure is `keyword_injected`, otherwise `"present"`.
- `[maintenance]` block: `start_offset` must be non-negative; `duration` must be positive.
- `mode` for `dns_ns_unavailable` must be `"silent"` or `"servfail"`.
- `variant` for `tls_deprecated` must be `"TLS10"` or `"TLS11"`. Defaults to `"TLS11"`.
- `dns_ns_unavailable` requires the fleet to have at least two authoritative nameservers for the target domain. The runner rejects this failure type if the fleet registry does not satisfy this requirement.
- At least one `[[failures]]` block is required per scenario.

---

## Complete examples

### Hard DNS failure

```toml
id          = "dns-nxdomain"
version     = "1"
description = "Target domain returns NXDOMAIN for the full run duration"
target      = "standard-http-site"
monitors    = ["all"]
check_frequency = "60s"
grace_period    = "180s"
duration        = "300s"
seed            = 1234

[[failures]]
type = "dns_nxdomain"
```

---

### Intermittent 503 with slow DNS

```toml
id          = "http-5xx-with-slow-dns"
version     = "1"
description = "503 on 40% of requests combined with 2s DNS latency"
target      = "standard-http-site"
monitors    = ["jetmon-v1", "pingdom", "datadog-synthetics", "better-uptime", "uptimerobot"]
check_frequency = "60s"
grace_period    = "180s"
duration        = "300s"
seed            = 7331

[[failures]]
type        = "http_status"
status_code = 503
rate        = 0.40

[[failures]]
type          = "dns_latency"
added_latency = "2000ms"
```

---

### Certificate expiry warning threshold

```toml
id          = "tls-expiring-under-7-days"
version     = "1"
description = "Certificate expiring in 6 days — tests monitor warning alert threshold"
target      = "standard-https-site"
monitors    = ["all"]
check_frequency = "60s"
grace_period    = "300s"
duration        = "600s"
seed            = 5678

[[failures]]
type           = "tls_expiring"
days_remaining = 6
```

---

### Silent application failure (WSOD)

```toml
id          = "http-body-error-page"
version     = "1"
description = "Origin returns CMS error page with 200 OK — tests content inspection"
target      = "standard-http-site"
monitors    = ["all"]
check_frequency = "60s"
grace_period    = "180s"
duration        = "300s"
seed            = 9012

[[failures]]
type    = "http_body"
content = "error_page"
```
