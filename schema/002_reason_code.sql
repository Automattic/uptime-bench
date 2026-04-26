-- uptime-bench migration 002: structured reason_code on monitor_reports
--
-- Adds a structured reason_code column to monitor_reports so reporting
-- can separate the three outcomes that all currently land in
-- retrieve_status = "unknown":
--
--   - reason_code IS NULL
--       Either a successful Known result, or an Unknown without a
--       structured code attached (legacy rows).
--
--   - reason_code = "capability_mismatch"
--       The harness skipped Provision because the scenario required a
--       capability the adapter does not support (e.g., keyword body
--       inspection on a status-only check). NOT a missed detection;
--       this is the support matrix.
--
--   - reason_code = future codes (api_unreachable, rate_limited, ...)
--       Reserved for adapter-side failures. Not populated yet.
--
-- See EVENTS.md "Missed detection vs. Unknown vs. capability mismatch"
-- for the reporting rules. retrieve_unknown_reason still carries the
-- free-form human-readable detail; reason_code is the queryable
-- categorisation.

ALTER TABLE monitor_reports
    ADD COLUMN reason_code VARCHAR(64) NULL AFTER retrieve_unknown_reason,
    ADD INDEX idx_reason_code (reason_code);
