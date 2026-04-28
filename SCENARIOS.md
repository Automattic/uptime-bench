# uptime-bench — Scenario Library

This document catalogs the failure modes that uptime-bench models as benchmark scenarios, organized by the layer at which the failure is observed by the monitor under test.

The taxonomy follows the same five-layer structure developed for the Jetmon project. See [`jetmon/TAXONOMY.md`](../jetmon/TAXONOMY.md) for the full Jetmon-specific design context, including the data model, state vocabulary, and signal-processing decisions.

## Complexity tags

Each item carries a tag inherited from the Jetmon taxonomy. In the uptime-bench context these indicate **scenario complexity and infrastructure requirements**, not an implementation roadmap:

- **[v1]** — Low complexity. Every credible monitor claims to detect these. Baseline scenarios: they separate "working" from "broken."
- **[v2]** — Moderate complexity. Some scenarios require multi-region infrastructure to induce reliably.
- **[v3]** — High complexity. Requires headless browser fleet, baseline learning, or other significant infrastructure.
- **[v4]** — Deferred. Hard to induce safely, or gated on integrations or partnerships.
- **[future]** — Genuinely hard; known but not yet scheduled.

Benchmark prioritization is separate from complexity: [v1] scenarios are the most revealing for baseline comparisons; [v2] scenarios covering intermittent failures, HEAD/GET mismatches, and WSOD-with-200 are where monitors most commonly diverge.

## A note on layer boundaries

Tag scenarios by **where the failure is observed by the monitor**, not where the root cause lives. Root-cause attribution is a separate concern; many observable failures surface at one layer while originating at another.

---

## Layer 1: Reachability

Can the monitor reach the site at all? These failures happen before any connection is established.

### Domain and registry
- **[v2]** Domain expired at registrar
- **[v2]** Domain approaching expiration (warning threshold, e.g., <30 days)
- **[v3]** Registrar lock status changed unexpectedly
- **[v3]** WHOIS/RDAP query failures
- **[v3]** Nameserver delegation mismatch (parent zone NS records don't match child zone)
- **[v2]** Domain suspended or in client/server hold status

### DNS resolution
- **[v1]** NXDOMAIN for apex and `www` subdomain
- **[v1]** SERVFAIL from authoritative nameservers
- **[v1]** Timeout contacting authoritative nameservers
- **[v1]** Resolver returns REFUSED
- **[v2]** DNSSEC validation failure (bogus signatures, expired signatures, broken chain of trust)
- **[v2]** CNAME chain exceeds resolver depth limit
- **[v1]** CNAME pointing to NXDOMAIN target

### Nameserver availability
- **[v2]** All authoritative nameservers for a domain go silent — domain becomes unresolvable without NXDOMAIN or SERVFAIL *(requires controlling the fleet's authoritative nameservers)*
- **[v2]** Partial nameserver failure — one NS is unreachable while others remain up; tests resolver retry and fallback behavior *(requires multi-NS fleet)*
- **[v3]** Nameserver responds but with significantly increased latency — DNS resolution succeeds but slowly *(see also `dns_latency` type)*
- **[v2]** Registrar-level NS delegation failure: all nameservers listed in the parent zone are simultaneously unreachable *(tests the hardest class of DNS outage — no fallback is possible)*

### DNS configuration
- **[v1]** Missing A record
- **[v1]** Missing AAAA record when IPv6 is expected
- **[v2]** A/AAAA records pointing to unreachable or parked IPs
- **[v2]** Round-robin DNS with one or more dead backends
- **[v3]** Geo-DNS returning wrong region's endpoint
- **[v3]** TTL set pathologically low (thrash) or high (stale after cutover)
- **[v4]** Missing or misconfigured MX/TXT records affecting site-adjacent services (SPF, DMARC, domain verification)
- **[future]** Split-horizon DNS mismatch (internal vs. external resolution differs)

### Network-layer connectivity
- **[v1]** IPv4 unreachable from monitor vantage point
- **[v1]** IPv6 unreachable when AAAA is published (common silent failure)
- **[v2]** Asymmetric IPv4/IPv6 behavior (one works, one doesn't)
- **[v2]** ICMP unreachable from upstream router
- **[future]** BGP route withdrawal affecting destination prefix
- **[v3]** MTU/PMTUD blackhole (small packets succeed, large fail)

### Geographic and network-path reachability
- **[v2]** Reachable from one region but not another *(requires multi-region probe fleet)*
- **[v3]** ASN-level block (monitor's ASN blackholed at destination)
- **[v2]** Country-level block or government-level filtering
- **[v3]** Upstream transit provider outage affecting subset of vantage points
- **[v4]** Origin IP listed on major blocklists (Spamhaus, SORBS, etc.)
- **[future]** CDN/origin IP nullrouted by major ISP

---

## Layer 2: Transport & Security

The connection itself — TCP, TLS, and the cryptographic handshake.

### TCP
- **[v1]** Connection refused (port closed)
- **[v1]** Connection reset mid-handshake
- **[v1]** Connection timeout (SYN with no SYN-ACK)
- **[v2]** Half-open connections (handshake completes but no data flows)
- **[v1]** Slow handshake exceeding threshold

### TLS handshake
- **[v1]** TLS handshake failure (generic)
- **[v2]** Unsupported protocol version mismatch
- **[v2]** No common cipher suite
- **[v2]** SNI mismatch (wrong vhost served)
- **[v2]** TLS alert parsing: `handshake_failure`, `protocol_version`, `unrecognized_name`

### Certificate validity
- **[v1]** Expired certificate
- **[v1]** Not-yet-valid certificate (clock skew or premature deployment)
- **[v1]** Certificate hostname mismatch (CN/SAN doesn't cover requested host)
- **[v1]** Self-signed certificate in production
- **[v1]** Certificate signed by untrusted CA
- **[v1]** Missing intermediate certificate(s) — chain incomplete
- **[v2]** Revoked certificate (CRL or OCSP says revoked)
- **[v2]** Weak signature algorithm (SHA-1, MD5)
- **[v2]** Key too short (RSA < 2048)

### Certificate operational issues
- **[v2]** OCSP stapling broken or returning `unknown`/`revoked`
- **[v3]** Certificate Transparency: cert not logged
- **[v1]** Approaching expiration (warning threshold, e.g., <30 days)
- **[v2]** HSTS header missing when expected
- **[v3]** HSTS `max-age` too low or preload list drift

### HTTPS enforcement
- **[v1]** Port 80 not redirecting to 443
- **[v1]** HTTPS not supported at all
- **[v3]** Mixed-content: HTTPS page loads HTTP assets
- **[v2]** HTTP/2 or HTTP/3 negotiation failures when advertised

### Other transport protocols
- **[v3]** WebSocket upgrade failures
- **[future]** gRPC connection or deadline-exceeded failures
- **[v4]** SMTP/IMAP/POP port availability
- **[v4]** Other TCP services (SSH, FTP, database ports)

---

## Layer 3: Infrastructure & Edge

The systems between the internet and the origin server.

### CDN and edge provider
- **[v1]** CDN returning its own error page (Cloudflare 520–526)
- **[v2]** CDN origin-unreachable errors
- **[v4]** Cloudflare/Fastly/Akamai/CloudFront provider-level outage detection
- **[v3]** Cache serving stale error responses
- **[v3]** Cache poisoning (wrong content served from edge)

### Cloud provider
- **[v4]** AWS/GCP/Azure region outage detection
- **[v3]** Managed database failure surfacing as application error
- **[v2]** Object storage outage affecting media

### Load balancer
- **[v1]** Load balancer entirely unreachable
- **[v2]** One or more backends dead but still in rotation
- **[v2]** Stale backend serving old code/content
- **[v3]** Uneven distribution (one backend getting 90% of traffic)
- **[v3]** Session affinity broken
- **[v2]** SSL termination issues at LB (cert mismatch between LB and origin)
- **[future]** LB health checks misconfigured

### WAF, bot protection, and rate limiting
- **[v1]** WAF false-positive blocking monitor (403)
- **[v1]** Bot-protection challenge page served instead of content
- **[v1]** Rate limiting triggered on monitor (429)
- **[v2]** IP reputation block (monitor IP flagged)
- **[v2]** Geoblocking misconfigured

### DDoS and traffic management
- **[v2]** DDoS protection in "under attack" mode serving challenges
- **[v3]** Anycast misrouting (traffic landing in wrong PoP)

---

## Layer 4: Application Response

The server accepts the connection and speaks HTTP — but does it respond correctly and promptly?

### Connection-level HTTP failures
- **[v1]** TCP connection accepted, no HTTP response sent (hang)
- **[v1]** Response timeout (server slow to first byte beyond threshold)
- **[v1]** Connection closed mid-response (truncated body)
- **[v2]** Invalid HTTP framing (bad Content-Length, chunked encoding errors)

### Status code anomalies
- **[v1]** 5xx responses (500, 502, 503, 504)
- **[v2]** Intermittent 5xx at elevated rate (e.g., >1% of requests)
- **[v1]** 4xx on canonical URLs that should succeed (404 on homepage)
- **[v1]** 401/403 on public pages
- **[v1]** Method inconsistency: HEAD returns 200 but GET returns 4xx/5xx — `http_method_status method="GET" status_code=503` — catches false-up signals from HEAD-only checks
- **[v1]** Method inconsistency: GET succeeds but HEAD returns 405 — `http_method_status method="HEAD" status_code=405` — catches false-down signals from HEAD-only checks
- **[v2]** OPTIONS preflight failures affecting CORS-dependent pages

### Network timing breakdown
- **[v1]** Total response time exceeds threshold
- **[v1]** Time to First Byte (TTFB) exceeds threshold
- **[v2]** DNS lookup time exceeds threshold
- **[v2]** TCP connect time exceeds threshold
- **[v2]** TLS handshake time exceeds threshold
- **[v2]** Content download time exceeds threshold
- **[v2]** Response size anomalies (much smaller or larger than baseline)
- **[v3]** Slow-loris-style responses (bytes trickle in over long duration)

### Redirect behavior
- **[v1]** Redirect loop (A → B → A)
- **[v1]** Redirect chain too long (>5 hops)
- **[v1]** Redirect to wrong host
- **[v1]** HTTPS → HTTP downgrade in redirect chain
- **[v2]** Redirect strips path or query string when it shouldn't
- **[v3]** 301 when 302 expected, or vice versa

### Header anomalies
- **[v1]** Missing `Content-Type`
- **[v2]** Wrong `Content-Type` (HTML served as `text/plain`)
- **[v2]** Missing security headers when expected
- **[v3]** Malformed `Cache-Control` causing CDN misbehavior
- **[v3]** Excessive cookie size breaking downstream proxies

---

## Layer 5: Content Integrity

The response is valid HTTP — but is the payload actually correct? Layer 5 splits into two classes:

- **Correctness failures** — the payload is wrong regardless of who requested it or when. Detected by inspecting a single response.
- **Consistency failures** — the payload looks fine in isolation but is wrong *for this request* (wrong user's view, wrong region's content, stale cache). Detected by comparing across requests or against expected invariants.

### Correctness: silent application failures
- **[v1]** CMS fatal error rendered with 200 OK (WSOD)
- **[v1]** "Error establishing a database connection" served as HTML with 200
- **[v1]** PHP fatal errors or stack traces in response body
- **[v1]** White-screen-of-death (empty or near-empty body, 200 OK)
- **[v2]** Python/Ruby/Node tracebacks leaked to response body

### Correctness: maintenance and transitional states
- **[v2]** Maintenance mode page served with 200 (should be 503 with Retry-After)
- **[v2]** "Coming soon" or placeholder content served unexpectedly
- **[v2]** Holding page from registrar/host
- **[v2]** Default server welcome page (nginx, Apache, IIS default)

### Correctness: security-relevant content

These scenarios inject security-compromise content into an otherwise healthy-looking 200 OK response. Status-only monitors cannot detect them; only monitors that inspect the response body will fire.

- **[v1]** Ransomware/extortion notice replacing site content — `http_body content="ransomware"` — simulates full site takeover by malware (DARKLOCK-style notice, BTC payment demand, 72-hour countdown)
- **[v1]** Hacktivist defacement replacing site content — `http_body content="defacement"` — simulates web server compromise (H4CK3D branding, ideological message)
- **[v1]** Malicious script injected into otherwise-normal page — `http_body content="malicious_script"` — simulates XSS or supply-chain compromise (external `<script>` tag pointing to attacker-controlled domain)
- **[v1]** SEO spam links injected into otherwise-normal page — `http_body content="spam_links"` — simulates blackhat SEO compromise (hidden links to pharmacy, gambling, crypto sites)
- **[v1]** Unexpected keyword injected (e.g., `"HACKED"`, `"BTC"`, `"ENCRYPTED"`) — `http_body content="keyword_injected"` — tests whether monitors can detect presence of unexpected terms
- **[v2]** Admin/debug pages exposed publicly (`/wp-admin` accessible without auth, `.env` served)

### Correctness: content completeness
- **[v1]** Expected string/marker present (canary text) — `http_body content="keyword_missing"`
- **[v1]** Near-empty body with 200 OK (white-screen-of-death) — `http_body content="empty"`
- **[v2]** Missing critical element (no `<title>`, empty `<body>`)
- **[v2]** Response body significantly smaller than baseline
- **[v3]** Broken HTML structure (unclosed tags affecting render)
- **[v3]** Missing or broken critical assets referenced by page (CSS/JS 404s)

### Correctness: structured data
- **[v2]** JSON API returning HTML error page
- **[v2]** XML/RSS feed malformed
- **[v2]** Sitemap returning 200 but empty or malformed
- **[v2]** `robots.txt` missing or returning HTML

### Consistency: cache and routing
- **[v2]** Wrong vhost served (different site's content on this domain)
- **[v3]** Cache poisoning (one user's content served to another)
- **[future]** A/B test or feature flag stuck in wrong state
- **[v3]** Localized content served to wrong region
- **[v3]** Logged-in view served to anonymous monitor (cache key bug)
- **[v2]** Stale content served long after origin update

### Client-side rendering (rendered-DOM checks)
*All items require headless browser infrastructure.*
- **[v3]** SPA fails to hydrate (initial HTML loads, JS fails)
- **[v3]** Client-side routing broken
- **[v3]** JavaScript errors in console exceeding threshold
- **[v3]** Core Web Vitals regression (LCP, CLS, FID)

### Third-party dependency failures
*All items require rendered-DOM inspection.*
- **[v3]** Critical external JS failing to load
- **[v3]** Payment processor SDK unavailable (Stripe, PayPal)
- **[v3]** Font provider outage affecting rendering
- **[v3]** CDN for assets failing (jsDelivr, unpkg)
- **[v3]** Embedded content broken (YouTube, Vimeo, social embeds)

---

## Reverse Checks: Agent-Reported Monitoring

Probe-based monitoring asks "is the site up from outside?" Reverse checks flip the direction: the monitored system reports *to us*, and silence means failure. This is a fundamentally different detection model.

**Applicability:** Reverse-check scenarios require the monitoring service under test to have an on-site agent. External-probe-only services (Pingdom, UptimeRobot, Datadog Synthetics in HTTP-probe mode, etc.) cannot be evaluated against these scenarios. WordPress-specific scenarios below apply only to monitoring services with a WordPress-aware agent — such as Jetmon via Jetpack.

### Heartbeat and dead-man's-switch
- **[v1]** Site fails to check in within expected interval
- **[v1]** Grace-period exhaustion (missed enough check-ins to declare down)
- **[v2]** Heartbeat interval drift (checking in late)
- **[v3]** Heartbeat from unexpected location or with unexpected payload

### Background jobs and queues (generic)
- **[v2]** Job queue depth exceeding threshold
- **[v2]** Failed jobs accumulating
- **[v3]** Job processing time regression
- **[v4]** Specific critical jobs not completing

### Application-internal health signals (generic)
- **[v2]** Error log rate exceeding threshold
- **[v3]** Database slow-query rate exceeding threshold
- **[v3]** Cache hit rate dropping below expected baseline
- **[v2]** Memory usage approaching configured limit
- **[v2]** Disk space approaching full
- **[v3]** Database connection pool exhaustion

### WordPress-specific: scheduled tasks
*Applicable to WordPress monitoring services with an on-site agent.*
- **[v1]** `wp-cron.php` not firing
- **[v2]** Scheduled events backlogging (queue depth growing)
- **[v2]** Individual recurring events failing repeatedly
- **[v3]** Plugin-registered cron jobs silently failing
- **[v2]** Post scheduled for publication but not published
- **[v2]** Action Scheduler queue depth exceeding threshold

### WordPress-specific: security and integrity signals
*Applicable to WordPress monitoring services with an on-site agent.*
- **[v4]** File integrity changes in core or plugin files
- **[v2]** Unexpected admin user creation
- **[v4]** Failed login rate spike
- **[v2]** Plugin/theme update failures
- **[v1]** WordPress core, plugin, or theme out of date beyond threshold

### WordPress-specific: configuration drift
*Applicable to WordPress monitoring services with an on-site agent.*
- **[v1]** PHP version approaching EOL
- **[v1]** WordPress version outdated
- **[v2]** Critical plugin disabled unexpectedly
- **[v2]** Site URL or home URL changed
- **[v1]** Debug mode enabled in production

---

## Out of scope for uptime-bench scenarios

Failure classes the benchmark does not cover:

- **Transaction / multistep monitoring** — scripted user flows (login, checkout). The failure mode is "step 3 of 5 broke," not a single request failing. A distinct problem.
- **Real User Monitoring (RUM)** — captures actual user sessions rather than synthetic probes.
- **Capacity and load testing** — "works at 10 rps, collapses at 100 rps" is not an uptime concern.
- **APM / in-process tracing** — code-level profiling, slow-query identification.
- **Alerting channel evaluation** — we measure alert creation in the monitor's data model, not whether PagerDuty or Slack received it.
