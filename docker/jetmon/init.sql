-- Jetmon database initialization for local POC testing.
-- This script creates the Jetmon 1 base schema, applies Jetmon 2 migrations,
-- and pre-seeds the bench.local and probe.local monitors.
--
-- Mounted as /docker-entrypoint-initdb.d/init.sql in the jetmon-mysql container.

-- Jetmon 1 base table (original schema, required before Jetmon 2 migrations).
CREATE TABLE IF NOT EXISTS jetpack_monitor_sites (
    jetpack_monitor_site_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id                 BIGINT UNSIGNED NOT NULL,
    bucket_no               SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    monitor_url             VARCHAR(300) NOT NULL,
    monitor_active          TINYINT(1) NOT NULL DEFAULT 1,
    site_status             TINYINT(1) NOT NULL DEFAULT 1,
    last_status_change      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    check_interval          TINYINT(1) NOT NULL DEFAULT 1,
    INDEX idx_blog_id_monitor_url (blog_id, monitor_url),
    INDEX idx_bucket_active_interval (bucket_no, monitor_active, check_interval)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon 2 migration 1: schema tracking table.
CREATE TABLE IF NOT EXISTS jetmon_schema_migrations (
    id         INT UNSIGNED NOT NULL PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon 2 migration 2: new columns on jetpack_monitor_sites.
ALTER TABLE jetpack_monitor_sites
    ADD COLUMN IF NOT EXISTS ssl_expiry_date        DATE NULL,
    ADD COLUMN IF NOT EXISTS check_keyword          VARCHAR(500) NULL,
    ADD COLUMN IF NOT EXISTS maintenance_start      DATETIME NULL,
    ADD COLUMN IF NOT EXISTS maintenance_end        DATETIME NULL,
    ADD COLUMN IF NOT EXISTS custom_headers         JSON NULL,
    ADD COLUMN IF NOT EXISTS timeout_seconds        TINYINT UNSIGNED NULL,
    ADD COLUMN IF NOT EXISTS redirect_policy        ENUM('follow','alert','fail') NULL DEFAULT 'follow',
    ADD COLUMN IF NOT EXISTS alert_cooldown_minutes SMALLINT UNSIGNED NULL;

-- Jetmon 2 migration 3: bucket ownership table.
CREATE TABLE IF NOT EXISTS jetmon_hosts (
    host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
    bucket_min     SMALLINT UNSIGNED NOT NULL,
    bucket_max     SMALLINT UNSIGNED NOT NULL,
    last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status         ENUM('active','draining') NOT NULL DEFAULT 'active'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon 2 migration 4: full audit log.
CREATE TABLE IF NOT EXISTS jetmon_audit_log (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id    BIGINT UNSIGNED NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    source     VARCHAR(255) NOT NULL DEFAULT 'local',
    http_code  SMALLINT NULL,
    error_code TINYINT NULL,
    rtt_ms     INT NULL,
    old_status TINYINT NULL,
    new_status TINYINT NULL,
    detail     TEXT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id_created (blog_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon 2 migration 5: check history for timing samples.
CREATE TABLE IF NOT EXISTS jetmon_check_history (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id    BIGINT UNSIGNED NOT NULL,
    http_code  SMALLINT NULL,
    error_code TINYINT NULL,
    rtt_ms     INT NULL,
    dns_ms     INT NULL,
    tcp_ms     INT NULL,
    tls_ms     INT NULL,
    ttfb_ms    INT NULL,
    checked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id_checked (blog_id, checked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon 2 migration 6: false positive tracking.
CREATE TABLE IF NOT EXISTS jetmon_false_positives (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id    BIGINT UNSIGNED NOT NULL,
    http_code  SMALLINT NULL,
    error_code TINYINT NULL,
    rtt_ms     INT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id (blog_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon 2 migration 7: last_checked_at and last_alert_sent_at columns.
ALTER TABLE jetpack_monitor_sites
    ADD COLUMN IF NOT EXISTS last_checked_at   DATETIME NULL,
    ADD COLUMN IF NOT EXISTS last_alert_sent_at DATETIME NULL,
    ADD INDEX  IF NOT EXISTS idx_bucket_monitor_last_checked
        (bucket_no, monitor_active, last_checked_at);

-- Mark all migrations as applied so jetmon2 migrate does not re-run them.
INSERT IGNORE INTO jetmon_schema_migrations (id) VALUES (1),(2),(3),(4),(5),(6),(7);

-- Pre-seed monitors for the bench fleet.
-- blog_id 1001 → bench.local (primary test site)
-- blog_id 1002 → probe.local (health check probe site)
-- check_interval = 1 minute for fast POC detection.
INSERT INTO jetpack_monitor_sites
    (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval)
VALUES
    (1001, 0, 'http://bench.local/', 1, 1, 1),
    (1002, 0, 'http://probe.local/health', 1, 1, 1);
