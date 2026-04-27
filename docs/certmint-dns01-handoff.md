# Certmint ACME DNS-01 Integration Handoff

## Goal

`uptime-bench-certmint` needs to mint publicly trusted Let's Encrypt
certificates that are later consumed by `uptime-bench` TLS scenarios. The
certificates should cover the same hostnames that public monitoring services
resolve through the `uptime-bench` authoritative DNS fleet.

The current proof-of-concept used Cloudflare DNS successfully, but that only
works while Cloudflare is authoritative for the relevant `_acme-challenge`
names. For production benchmark domains, the runtime hostnames are intended to
resolve through `uptime-bench-dns`, so Cloudflare cannot be the long-term
answer for those exact names unless challenge records remain delegated there.

## Recommended Direction

Add ACME DNS-01 TXT support to `uptime-bench-dns`, then let certmint drive it
through Certbot manual DNS hooks.

High-level flow:

```text
uptime-bench-certmint
  -> certbot certonly --manual --preferred-challenges dns
  -> manual auth hook receives CERTBOT_IDENTIFIER and CERTBOT_VALIDATION
  -> hook writes TXT record to every uptime-bench authoritative DNS member
  -> Let's Encrypt queries _acme-challenge.<identifier>
  -> certbot obtains the cert
  -> manual cleanup hook removes the TXT value
  -> certmint archives certbot's live lineage into the cert library
```

This preserves wildcard issuance. HTTP-01 and TLS-ALPN-01 are not good fits
because they cannot issue wildcard certificates, and certmint's useful coverage
depends on wildcard identifiers plus unique SANs.

## Relevant Existing Files

- `cmd/dns/main.go`: authoritative DNS binary entry point.
- `internal/dnsserver/dnsserver.go`: DNS response generation. Currently builds
  and serves A records from fleet config.
- `internal/dnsserver/dnsserver_test.go`: DNS unit tests.
- `internal/control/server.go`: HTTP control-plane patterns.
- `internal/control/client.go`: control client patterns.
- `fleet.example.toml`: nameserver/domain/target model.
- `cmd/target/main.go`: target loads a certmint manifest via
  `-cert-library-manifest`.
- `internal/certlibrary/certlibrary.go`: target-side certmint manifest loader
  and cert selection logic.

## ACME Constraints To Preserve

- DNS-01 validates by querying TXT records at `_acme-challenge.<identifier>`.
- Wildcard `*.example.com` validates at `_acme-challenge.example.com`.
- Non-wildcard `example.com` also validates at `_acme-challenge.example.com`.
- Multiple TXT values at the same challenge name must be supported because apex
  and wildcard authorizations can be active in the same order.
- Certbot manual hooks provide:
  - `CERTBOT_IDENTIFIER`
  - `CERTBOT_VALIDATION`
  - `CERTBOT_TOKEN`
  - `CERTBOT_REMAINING_CHALLENGES`
  - `CERTBOT_ALL_IDENTIFIERS`
- For wildcard identifiers, strip the leading `*.` before building the
  challenge name.

Challenge name derivation:

```text
identifier:  *.bench.example.com
challenge:   _acme-challenge.bench.example.com

identifier:  bench.example.com
challenge:   _acme-challenge.bench.example.com
```

## DNS Server Work

Add a dynamic TXT record store to the DNS server alongside the existing A
record zone map.

Suggested data shape:

```go
type TXTStore struct {
    mu      sync.RWMutex
    records map[string][]string // normalized fqdn -> TXT values
}
```

The store should:

- normalize names lowercase without trailing dot
- preserve multiple values for one TXT name
- allow deleting one specific value without removing unrelated values
- be process-local; persistent storage is not required for ACME hooks

Update DNS response logic to answer TXT queries. Keep existing A behavior
unchanged.

Important behavior:

- If a TXT challenge record exists, answer it even if the name is not present
  in the static A zone map.
- Prefer ACME challenge TXT records to normal DNS failure injection, or require
  certmint to run only outside active benchmark runs. Prefer bypassing failure
  injection for `_acme-challenge.` because certificate issuance is operational
  plumbing, not a benchmark scenario.
- Continue returning A records for benchmark hostnames as today.

## Control API Work

Add control-plane endpoints on every DNS member. Exact shape can follow local
style, but keep it simple.

Suggested endpoints:

```http
PUT /acme/txt
Content-Type: application/json

{
  "name": "_acme-challenge.bench.example.com",
  "value": "validation-token",
  "ttl": 30
}
```

```http
DELETE /acme/txt
Content-Type: application/json

{
  "name": "_acme-challenge.bench.example.com",
  "value": "validation-token"
}
```

Return success only after updating the local in-memory TXT store.

Security:

- Use the existing control-plane token/auth model if one exists for DNS
  failure control.
- Do not expose unauthenticated mutation endpoints.
- These endpoints only need to be reachable by the certmint host or harness
  network, not by the public Internet.

## Certmint Hook Work

Certmint does not need to become an ACME client. It can keep shelling out to
Certbot.

Add example Certbot manual DNS hook scripts in certmint or uptime-bench docs.
The hook must call every authoritative DNS member for the zone before returning.

Auth hook pseudocode:

```sh
identifier="${CERTBOT_IDENTIFIER#*.}"
name="_acme-challenge.${identifier}"
value="${CERTBOT_VALIDATION}"

for dns_control_url in $UPTIME_BENCH_DNS_CONTROL_URLS; do
  curl -fsS -X PUT "${dns_control_url}/acme/txt" \
    -H "Authorization: Bearer ${UPTIME_BENCH_CONTROL_TOKEN}" \
    -H "Content-Type: application/json" \
    --data "{\"name\":\"${name}\",\"value\":\"${value}\",\"ttl\":30}"
done
```

Cleanup hook pseudocode:

```sh
identifier="${CERTBOT_IDENTIFIER#*.}"
name="_acme-challenge.${identifier}"
value="${CERTBOT_VALIDATION}"

for dns_control_url in $UPTIME_BENCH_DNS_CONTROL_URLS; do
  curl -fsS -X DELETE "${dns_control_url}/acme/txt" \
    -H "Authorization: Bearer ${UPTIME_BENCH_CONTROL_TOKEN}" \
    -H "Content-Type: application/json" \
    --data "{\"name\":\"${name}\",\"value\":\"${value}\"}"
done
```

Certmint config would use:

```json
"authenticator_args": [
  "--manual",
  "--preferred-challenges",
  "dns",
  "--manual-auth-hook",
  "/usr/local/bin/uptime-bench-acme-auth",
  "--manual-cleanup-hook",
  "/usr/local/bin/uptime-bench-acme-cleanup"
]
```

Note: certmint currently always adds `--preferred-challenges dns`, so do not
duplicate it unless certmint is changed to make that optional.

## Unique SAN Note

The staging test found that Let's Encrypt rejects a direct child SAN when the
same order includes a wildcard that already covers it.

Bad in the same order:

```text
*.truetempo.party
cert-20260427-00-shortlived.truetempo.party
```

Good:

```text
*.truetempo.party
cert-20260427-00-shortlived.unique.truetempo.party
```

Certmint has been updated to default to:

```text
cert-{date}-{slot}-{profile}.unique.{domain}
```

## Test Plan

Unit tests:

- DNS TXT query returns all configured ACME TXT values.
- Multiple TXT values at the same name are preserved.
- Deleting one TXT value keeps sibling values.
- A-record behavior remains unchanged.
- `_acme-challenge.` TXT responses bypass or are unaffected by active DNS
  failure scenarios, if that behavior is implemented.
- Control API rejects unauthenticated TXT mutations.

Integration test:

1. Start two `uptime-bench-dns` instances with control APIs.
2. Add a TXT value to both with the new control endpoint.
3. Query each DNS instance directly for `_acme-challenge.<domain>` TXT.
4. Delete the value from both.
5. Confirm the TXT no longer resolves.

Manual staging test:

1. Delegate a test domain to `uptime-bench-dns`.
2. Configure certmint with manual DNS hooks.
3. Run:

   ```sh
   uptime-bench-certmint once -config /path/to/config.json -dry-run
   uptime-bench-certmint once -config /path/to/config.json
   ```

4. Inspect the resulting certmint manifest.
5. Confirm `uptime-bench` target can load the manifest and select a cert for a
   benchmark hostname covered by the wildcard.

## Open Design Questions

- Should ACME TXT records be process-local only, or persisted across DNS server
  restarts while a Certbot order is in progress?
  - Process-local is simpler and probably enough if certmint runs outside
    deployment windows.
- Should certmint talk directly to every DNS node, or should the harness own a
  fanout endpoint?
  - Direct fanout is simpler for first implementation.
- Should DNS failure injection ignore only `_acme-challenge.` names or all TXT
  records?
  - Prefer ignoring only `_acme-challenge.`.
- Should challenge endpoints live in `internal/control` or a DNS-specific
  control package?
  - Follow the existing control server style unless it forces unrelated coupling.

## References

- Let’s Encrypt challenge docs:
  https://letsencrypt.org/docs/challenge-types/
- Certbot manual hook docs:
  https://eff-certbot.readthedocs.io/en/latest/man/certbot.html
- Certmint repository:
  `../uptime-bench-certmint`
