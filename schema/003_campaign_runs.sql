-- uptime-bench migration 003: campaign tracking
--
-- Adds the campaign_runs table to track long-running automated
-- campaigns and links scenario_runs back to their parent campaign via
-- a nullable campaign_id (NULL for direct single-scenario runs;
-- non-NULL for runs orchestrated by a campaign).
--
-- See ROADMAP.md "Automated randomized testing campaigns" for the
-- methodology. The audit-trail columns (config_toml, master_seed,
-- adapter_versions, target_fleet_version) capture everything a reader
-- of a published comparison post needs to regenerate the campaign:
-- check out the recorded SHAs, run the recorded config with the
-- recorded seed, get the same Plan and Schedule the original campaign
-- generated.

CREATE TABLE IF NOT EXISTS campaign_runs (
    id                   VARCHAR(36)   NOT NULL,
    campaign_id          VARCHAR(128)  NOT NULL,    -- from campaign TOML's `id`
    config_toml          TEXT          NOT NULL,    -- full campaign config, verbatim
    master_seed          BIGINT        NOT NULL,    -- seeds the deterministic Plan
    started_at           DATETIME(6)   NOT NULL,
    ended_at             DATETIME(6)   NULL,
    resolution_reason    VARCHAR(64)   NULL,        -- planned_completion / aborted / etc.
    adapter_versions     JSON          NULL,        -- map of service_id → commit SHA
    target_fleet_version VARCHAR(64)   NULL,        -- target/dns binary commit SHA
    PRIMARY KEY (id),
    INDEX idx_campaign_id (campaign_id),
    INDEX idx_started_at  (started_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Link scenario_runs back to their parent campaign. NULL means a
-- single-scenario run invoked directly (the existing `make
-- run-scenario` path); non-NULL means a campaign-orchestrated replay.
ALTER TABLE scenario_runs
    ADD COLUMN campaign_id VARCHAR(36) NULL AFTER target_id,
    ADD CONSTRAINT fk_sr_campaign FOREIGN KEY (campaign_id)
        REFERENCES campaign_runs (id) ON DELETE CASCADE,
    ADD INDEX idx_sr_campaign (campaign_id);
