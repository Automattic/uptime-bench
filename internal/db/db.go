// Package db manages the MySQL connection and event log writes.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// DB wraps a *sql.DB with uptime-bench-specific query methods.
type DB struct {
	db *sql.DB
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
	Parameters       any // will be JSON-encoded
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
		 (id, scenario_id, scenario_version, seed, target_id, parameters, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ScenarioID, r.ScenarioVersion, r.Seed, r.TargetID, params, r.StartedAt,
	)
	if err != nil {
		return fmt.Errorf("db: InsertRun: %w", err)
	}
	return nil
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
	RetrieveUnknownReason    string // populated when unknown
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
	nullStr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO monitor_reports
		 (run_id, service_id, retrieve_status, retrieve_unknown_reason,
		  event_type, raw_classification, normalized_classification,
		  reported_at, retrieved_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.ServiceID, r.RetrieveStatus,
		nullStr(r.RetrieveUnknownReason),
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

// UpsertDerivedMetric inserts or replaces one derived_metrics row.
// The UNIQUE KEY on (run_id, service_id, metric_name) makes this idempotent.
func (d *DB) UpsertDerivedMetric(ctx context.Context, r DerivedMetricRow) error {
	nullStr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
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
			&r.RetrieveUnknownReason, &r.EventType,
			&r.RawClassification, &r.NormalizedClassification,
			&r.ReportedAt, &r.RetrievedAt,
		); err != nil {
			return nil, fmt.Errorf("db: MonitorReportsForRun: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
