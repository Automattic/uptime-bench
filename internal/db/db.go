// Package db manages the MySQL connection and event log writes.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// DB wraps a *sql.DB with uptime-bench-specific query methods.
type DB struct {
	db *sql.DB
}

// nullStr returns nil for empty strings, otherwise a pointer to s. Used to
// pass NULL into MySQL columns where the empty string would be a distinct
// (and incorrect) value.
func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Open connects to MySQL using the given DSN.
// DSN format: user:password@tcp(host:3306)/uptime_bench?parseTime=true
func Open(dsn string) (*DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	return &DB{db: db}, nil
}

// Close releases the database connection pool.
func (d *DB) Close() error {
	return d.db.Close()
}

// RunRecord is a scenario run row.
type RunRecord struct {
	ID               string
	ScenarioID       string
	ScenarioVersion  string
	Seed             int64
	TargetID         string
	CampaignID       string // empty for direct (non-campaign) runs
	Parameters       any    // will be JSON-encoded
	StartedAt        time.Time
	EndedAt          *time.Time
	ResolutionReason *string
}

// InsertRun writes a new scenario_runs row.
func (d *DB) InsertRun(ctx context.Context, r RunRecord) error {
	params, err := json.Marshal(r.Parameters)
	if err != nil {
		return fmt.Errorf("db: InsertRun: marshal params: %w", err)
	}
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO scenario_runs
		 (id, scenario_id, scenario_version, seed, target_id, campaign_id, parameters, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ScenarioID, r.ScenarioVersion, r.Seed, r.TargetID,
		nullStr(r.CampaignID), params, r.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("db: InsertRun: %w", err)
	}
	return nil
}

// CampaignRunRecord is a campaign_runs row. The audit-trail fields
// (ConfigTOML, MasterSeed, AdapterVersions, TargetFleetVersion) let a
// reader of a published comparison post regenerate the campaign
// deterministically. See ROADMAP.md "Automated randomized testing
// campaigns" → "Methodology audit trail".
type CampaignRunRecord struct {
	ID                 string
	CampaignID         string // from campaign TOML's `id` field
	ConfigTOML         string // verbatim campaign config
	MasterSeed         int64
	StartedAt          time.Time
	AdapterVersions    any    // map[string]string of service_id → SHA, JSON-encoded
	TargetFleetVersion string // commit SHA of target/dns binaries
}

// InsertCampaignRun writes a new campaign_runs row at campaign start.
func (d *DB) InsertCampaignRun(ctx context.Context, r CampaignRunRecord) error {
	var versions []byte
	if r.AdapterVersions != nil {
		var err error
		versions, err = json.Marshal(r.AdapterVersions)
		if err != nil {
			return fmt.Errorf("db: InsertCampaignRun: marshal adapter_versions: %w", err)
		}
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO campaign_runs
		 (id, campaign_id, config_toml, master_seed, started_at, adapter_versions, target_fleet_version)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.CampaignID, r.ConfigTOML, r.MasterSeed, r.StartedAt,
		versions, nullStr(r.TargetFleetVersion),
	)
	if err != nil {
		return fmt.Errorf("db: InsertCampaignRun: %w", err)
	}
	return nil
}

// CloseCampaignRun sets ended_at and resolution_reason on an existing
// campaign row, mirroring CloseRun's shape for individual runs.
func (d *DB) CloseCampaignRun(ctx context.Context, campaignRunID string, endedAt time.Time, reason string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE campaign_runs SET ended_at = ?, resolution_reason = ? WHERE id = ?`,
		endedAt, reason, campaignRunID,
	)
	if err != nil {
		return fmt.Errorf("db: CloseCampaignRun: %w", err)
	}
	return nil
}

// RunIDsForCampaign returns scenario_runs IDs belonging to a campaign
// run, ordered by start time for deterministic batch metric derivation.
func (d *DB) RunIDsForCampaign(ctx context.Context, campaignRunID string) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id FROM scenario_runs WHERE campaign_id = ? ORDER BY started_at, id`,
		campaignRunID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: RunIDsForCampaign: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("db: RunIDsForCampaign: scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CloseRun sets ended_at and resolution_reason on an existing run.
func (d *DB) CloseRun(ctx context.Context, runID string, endedAt time.Time, reason string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE scenario_runs SET ended_at = ?, resolution_reason = ? WHERE id = ?`,
		endedAt, reason, runID,
	)
	if err != nil {
		return fmt.Errorf("db: CloseRun: %w", err)
	}
	return nil
}

// GroundTruthEvent is a ground_truth_events row.
type GroundTruthEvent struct {
	RunID       string
	EventType   string // run_start, run_end, failure_start, failure_end
	TargetID    string
	FailureType string // empty for run_start / run_end
	OccurredAt  time.Time
	Details     any // will be JSON-encoded; may be nil
}

// InsertGroundTruthEvent writes one ground_truth_events row.
func (d *DB) InsertGroundTruthEvent(ctx context.Context, e GroundTruthEvent) error {
	var details []byte
	if e.Details != nil {
		var err error
		details, err = json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("db: InsertGroundTruthEvent: marshal details: %w", err)
		}
	}
	var failureType *string
	if e.FailureType != "" {
		failureType = &e.FailureType
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO ground_truth_events
		 (run_id, event_type, target_id, failure_type, occurred_at, details)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.RunID, e.EventType, e.TargetID, failureType, e.OccurredAt, details,
	)
	if err != nil {
		return fmt.Errorf("db: InsertGroundTruthEvent: %w", err)
	}
	return nil
}

// MonitorReportRow is a monitor_reports row.
type MonitorReportRow struct {
	RunID                    string
	ServiceID                string
	RetrieveStatus           string // "known" or "unknown"
	RetrieveUnknownReason    string // free-form detail, populated when unknown
	ReasonCode               string // structured code, e.g. "capability_mismatch"; see EVENTS.md
	EventType                string // may be empty when unknown
	RawClassification        string
	NormalizedClassification string
	ReportedAt               *time.Time
	RetrievedAt              time.Time
	Metadata                 any
}

// InsertMonitorReport writes one monitor_reports row.
func (d *DB) InsertMonitorReport(ctx context.Context, r MonitorReportRow) error {
	var meta []byte
	if r.Metadata != nil {
		var err error
		meta, err = json.Marshal(r.Metadata)
		if err != nil {
			return fmt.Errorf("db: InsertMonitorReport: marshal metadata: %w", err)
		}
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO monitor_reports
		 (run_id, service_id, retrieve_status, retrieve_unknown_reason, reason_code,
		  event_type, raw_classification, normalized_classification,
		  reported_at, retrieved_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.ServiceID, r.RetrieveStatus,
		nullStr(r.RetrieveUnknownReason),
		nullStr(r.ReasonCode),
		nullStr(r.EventType),
		nullStr(r.RawClassification),
		nullStr(r.NormalizedClassification),
		r.ReportedAt, r.RetrievedAt, meta,
	)
	if err != nil {
		return fmt.Errorf("db: InsertMonitorReport: %w", err)
	}
	return nil
}

// DerivedMetricRow is a derived_metrics row.
type DerivedMetricRow struct {
	RunID       string
	ServiceID   string
	MetricName  string
	MetricValue *float64
	MetricText  string
	ComputedAt  time.Time
}

// CampaignMetricRow is one derived_metrics row joined to its campaign
// replay context for reporting.
type CampaignMetricRow struct {
	RunID       string
	FailureType string
	ServiceID   string
	MetricName  string
	MetricValue *float64
	MetricText  string
}

// CampaignRunSummary describes one campaign_runs row resolved by
// ResolveCampaign — the audit-trail metadata the report tool needs to
// disclose how an aggregated report was scoped.
type CampaignRunSummary struct {
	ID         string
	CampaignID string
	StartedAt  time.Time
	EndedAt    *time.Time // nil for an in-progress campaign
}

// CampaignLookup is the result of resolving a user-supplied campaign
// identifier to one or more campaign_runs rows. Either a concrete
// campaign_runs.id or the stable campaign_id from the campaign TOML
// is accepted; the report tool uses this to log which interpretation
// hit and to surface the aggregation depth in the output.
type CampaignLookup struct {
	Input             string
	Runs              []CampaignRunSummary
	MatchedAsRunID    bool // input matched a campaign_runs.id
	MatchedAsConfigID bool // input matched at least one campaign_runs.campaign_id
}

// ResolveCampaign finds every campaign_runs row matching the input as
// either a concrete id or a stable campaign_id. Returns a lookup with
// MatchedAsRunID / MatchedAsConfigID flags so the caller can tell the
// user which interpretation hit (and warn when the answer is "neither
// — your report will be empty"). Rows are ordered by started_at, id
// for deterministic downstream consumption.
func (d *DB) ResolveCampaign(ctx context.Context, input string) (*CampaignLookup, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, campaign_id, started_at, ended_at
		   FROM campaign_runs
		  WHERE id = ? OR campaign_id = ?
		  ORDER BY started_at, id`,
		input, input,
	)
	if err != nil {
		return nil, fmt.Errorf("db: ResolveCampaign: %w", err)
	}
	defer rows.Close()

	out := &CampaignLookup{Input: input}
	for rows.Next() {
		var r CampaignRunSummary
		var endedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.CampaignID, &r.StartedAt, &endedAt); err != nil {
			return nil, fmt.Errorf("db: ResolveCampaign: scan: %w", err)
		}
		if endedAt.Valid {
			t := endedAt.Time
			r.EndedAt = &t
		}
		if r.ID == input {
			out.MatchedAsRunID = true
		}
		if r.CampaignID == input {
			out.MatchedAsConfigID = true
		}
		out.Runs = append(out.Runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: ResolveCampaign: %w", err)
	}
	return out, nil
}

// UpsertDerivedMetric inserts or replaces one derived_metrics row.
// The UNIQUE KEY on (run_id, service_id, metric_name) makes this idempotent.
func (d *DB) UpsertDerivedMetric(ctx context.Context, r DerivedMetricRow) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO derived_metrics
		 (run_id, service_id, metric_name, metric_value, metric_text, computed_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   metric_value = VALUES(metric_value),
		   metric_text  = VALUES(metric_text),
		   computed_at  = VALUES(computed_at)`,
		r.RunID, r.ServiceID, r.MetricName, r.MetricValue, nullStr(r.MetricText), r.ComputedAt,
	)
	if err != nil {
		return fmt.Errorf("db: UpsertDerivedMetric: %w", err)
	}
	return nil
}

// CampaignMetricRows returns derived metrics for every scenario run
// belonging to the given campaign_runs.id values. Callers first resolve
// a user-supplied identifier via ResolveCampaign so the report path can
// log which interpretation matched and how many runs were aggregated;
// passing the run-id list explicitly here keeps that signal out of band
// from the data fetch.
func (d *DB) CampaignMetricRows(ctx context.Context, campaignRunIDs []string) ([]CampaignMetricRow, error) {
	if len(campaignRunIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(campaignRunIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(campaignRunIDs))
	for i, id := range campaignRunIDs {
		args[i] = id
	}
	query := `SELECT sr.id,
	        COALESCE(ft.failure_types, ''),
	        dm.service_id,
	        dm.metric_name,
	        dm.metric_value,
	        COALESCE(dm.metric_text, '')
	   FROM scenario_runs sr
	   JOIN derived_metrics dm ON dm.run_id = sr.id
	   LEFT JOIN (
	     SELECT run_id,
	            GROUP_CONCAT(DISTINCT failure_type ORDER BY failure_type SEPARATOR '+') AS failure_types
	       FROM ground_truth_events
	      WHERE event_type = 'failure_start'
	        AND failure_type IS NOT NULL
	      GROUP BY run_id
	   ) ft ON ft.run_id = sr.id
	  WHERE sr.campaign_id IN (` + placeholders + `)
	  ORDER BY COALESCE(ft.failure_types, ''), dm.service_id, sr.started_at, sr.id, dm.metric_name`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: CampaignMetricRows: %w", err)
	}
	defer rows.Close()

	var out []CampaignMetricRow
	for rows.Next() {
		var r CampaignMetricRow
		var metricValue sql.NullFloat64
		if err := rows.Scan(
			&r.RunID,
			&r.FailureType,
			&r.ServiceID,
			&r.MetricName,
			&metricValue,
			&r.MetricText,
		); err != nil {
			return nil, fmt.Errorf("db: CampaignMetricRows: scan: %w", err)
		}
		if metricValue.Valid {
			value := metricValue.Float64
			r.MetricValue = &value
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GroundTruthEventsForRun returns all ground_truth_events for a run, ordered by occurred_at.
func (d *DB) GroundTruthEventsForRun(ctx context.Context, runID string) ([]GroundTruthEvent, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT run_id, event_type, target_id, COALESCE(failure_type,''), occurred_at
		 FROM ground_truth_events WHERE run_id = ? ORDER BY occurred_at`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: GroundTruthEventsForRun: %w", err)
	}
	defer rows.Close()

	var out []GroundTruthEvent
	for rows.Next() {
		var e GroundTruthEvent
		if err := rows.Scan(&e.RunID, &e.EventType, &e.TargetID, &e.FailureType, &e.OccurredAt); err != nil {
			return nil, fmt.Errorf("db: GroundTruthEventsForRun: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MonitorReportsForRun returns all monitor_reports for a run.
func (d *DB) MonitorReportsForRun(ctx context.Context, runID string) ([]MonitorReportRow, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT run_id, service_id, retrieve_status,
		        COALESCE(retrieve_unknown_reason,''),
		        COALESCE(reason_code,''),
		        COALESCE(event_type,''), COALESCE(raw_classification,''),
		        COALESCE(normalized_classification,''),
		        reported_at, retrieved_at
		 FROM monitor_reports WHERE run_id = ? ORDER BY retrieved_at`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("db: MonitorReportsForRun: %w", err)
	}
	defer rows.Close()

	var out []MonitorReportRow
	for rows.Next() {
		var r MonitorReportRow
		if err := rows.Scan(
			&r.RunID, &r.ServiceID, &r.RetrieveStatus,
			&r.RetrieveUnknownReason, &r.ReasonCode, &r.EventType,
			&r.RawClassification, &r.NormalizedClassification,
			&r.ReportedAt, &r.RetrievedAt,
		); err != nil {
			return nil, fmt.Errorf("db: MonitorReportsForRun: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
