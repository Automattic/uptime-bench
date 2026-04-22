-- uptime-bench initial schema
-- Migration: 001_initial
--
-- Applied automatically on first `docker compose up` via the
-- /docker-entrypoint-initdb.d mount. For production, apply with:
--   mysql -h HOST -u uptime_bench -p uptime_bench < schema/001_initial.sql
--
-- Schema rules:
--   - Migrations are append-only. Never edit a prior migration file.
--   - Derived metric rows are never written in the same transaction as
--     raw event rows. The derived_metrics table is populated by a
--     separate recomputation step.

CREATE TABLE IF NOT EXISTS scenario_runs (
    id                VARCHAR(36)   NOT NULL,
    scenario_id       VARCHAR(128)  NOT NULL,
    scenario_version  VARCHAR(32)   NOT NULL,
    seed              BIGINT        NOT NULL,
    target_id         VARCHAR(128)  NOT NULL,
    parameters        JSON          NOT NULL,   -- full scenario TOML fields, serialised
    started_at        DATETIME(6)   NOT NULL,
    ended_at          DATETIME(6)   NULL,
    resolution_reason VARCHAR(64)   NULL,       -- required on close; NULL while in-progress
    PRIMARY KEY (id),
    INDEX idx_scenario_id  (scenario_id),
    INDEX idx_started_at   (started_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Ground-truth events: the canonical record of what each scenario did.
-- event_type values: failure_start, failure_end, run_start, run_end
CREATE TABLE IF NOT EXISTS ground_truth_events (
    id            BIGINT        NOT NULL AUTO_INCREMENT,
    run_id        VARCHAR(36)   NOT NULL,
    event_type    VARCHAR(64)   NOT NULL,
    target_id     VARCHAR(128)  NOT NULL,
    failure_type  VARCHAR(64)   NULL,           -- NULL for run_start / run_end
    occurred_at   DATETIME(6)   NOT NULL,
    details       JSON          NULL,           -- failure params, rate, etc.
    PRIMARY KEY (id),
    INDEX idx_run_id     (run_id),
    INDEX idx_occurred_at (occurred_at),
    CONSTRAINT fk_gte_run FOREIGN KEY (run_id)
        REFERENCES scenario_runs (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Monitor reports: what each adapter retrieved from each service.
-- event_type values: alert_fired, alert_resolved, status_change
-- retrieve_status values: known, unknown
CREATE TABLE IF NOT EXISTS monitor_reports (
    id                        BIGINT        NOT NULL AUTO_INCREMENT,
    run_id                    VARCHAR(36)   NOT NULL,
    service_id                VARCHAR(64)   NOT NULL,
    retrieve_status           VARCHAR(16)   NOT NULL,
    retrieve_unknown_reason   VARCHAR(512)  NULL,    -- populated when retrieve_status = unknown
    event_type                VARCHAR(64)   NULL,    -- NULL when retrieve_status = unknown
    raw_classification        VARCHAR(256)  NULL,
    normalized_classification VARCHAR(64)   NULL,
    reported_at               DATETIME(6)   NULL,    -- service clock; NULL when unknown
    retrieved_at              DATETIME(6)   NOT NULL,
    metadata                  JSON          NULL,
    PRIMARY KEY (id),
    INDEX idx_run_service (run_id, service_id),
    CONSTRAINT fk_mr_run FOREIGN KEY (run_id)
        REFERENCES scenario_runs (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Derived metrics: computed from ground_truth_events + monitor_reports.
-- Never written in the same transaction as raw event rows.
-- Recomputable from the raw tables at any time.
--
-- metric_name examples: detection_latency_s, true_positive, false_negative,
--   unknown, capability_skip, classification_score
CREATE TABLE IF NOT EXISTS derived_metrics (
    id            BIGINT        NOT NULL AUTO_INCREMENT,
    run_id        VARCHAR(36)   NOT NULL,
    service_id    VARCHAR(64)   NOT NULL,
    metric_name   VARCHAR(64)   NOT NULL,
    metric_value  DOUBLE        NULL,           -- numeric metrics
    metric_text   VARCHAR(256)  NULL,           -- categorical metrics
    computed_at   DATETIME(6)   NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_run_service_metric (run_id, service_id, metric_name),
    INDEX idx_run_id (run_id),
    CONSTRAINT fk_dm_run FOREIGN KEY (run_id)
        REFERENCES scenario_runs (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
