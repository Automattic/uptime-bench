package jetmoncapacity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var dbOperationalStatusVariables = []string{
	"Questions",
	"Queries",
	"Com_select",
	"Com_insert",
	"Com_update",
	"Com_delete",
	"Com_replace",
	"Innodb_rows_read",
	"Innodb_rows_inserted",
	"Innodb_rows_updated",
	"Innodb_rows_deleted",
	"Bytes_received",
	"Bytes_sent",
}

// DBOperationalRun captures MySQL status counters and table row counts at a
// capacity-window boundary, or the delta between two boundaries.
type DBOperationalRun struct {
	Phase           string                 `json:"phase"`
	Status          string                 `json:"status"`
	Error           string                 `json:"error,omitempty"`
	CapturedAt      time.Time              `json:"captured_at"`
	StartCapturedAt *time.Time             `json:"start_captured_at,omitempty"`
	EndCapturedAt   *time.Time             `json:"end_captured_at,omitempty"`
	ElapsedSeconds  float64                `json:"elapsed_seconds,omitempty"`
	Source          string                 `json:"source"`
	StatusVariables []string               `json:"status_variables,omitempty"`
	Tables          []string               `json:"tables,omitempty"`
	Services        []DBOperationalService `json:"services"`
}

// DBOperationalService is the service-level DB counter snapshot or delta.
type DBOperationalService struct {
	ID                  string            `json:"id"`
	Schema              string            `json:"schema"`
	Database            string            `json:"database,omitempty"`
	Status              string            `json:"status"`
	Error               string            `json:"error,omitempty"`
	GlobalStatus        map[string]uint64 `json:"global_status,omitempty"`
	GlobalStatusDelta   map[string]int64  `json:"global_status_delta,omitempty"`
	TableRows           map[string]uint64 `json:"table_rows,omitempty"`
	TableRowDelta       map[string]int64  `json:"table_row_delta,omitempty"`
	MissingTables       []string          `json:"missing_tables,omitempty"`
	ExistingTables      []string          `json:"existing_tables,omitempty"`
	DatabaseQueries     int64             `json:"database_queries,omitempty"`
	DatabaseWrites      int64             `json:"database_writes,omitempty"`
	RowsRead            int64             `json:"rows_read,omitempty"`
	RowsAdded           int64             `json:"rows_added,omitempty"`
	RowsUpdated         int64             `json:"rows_updated,omitempty"`
	RowsDeleted         int64             `json:"rows_deleted,omitempty"`
	QueryCounterSource  string            `json:"query_counter_source,omitempty"`
	WriteCounterSource  string            `json:"write_counter_source,omitempty"`
	RowCounterSource    string            `json:"row_counter_source,omitempty"`
	TableRowDeltaSource string            `json:"table_row_delta_source,omitempty"`
}

func defaultDBOperationalTables() []string {
	return []string{
		"jetpack_monitor_sites",
		"jetpack_monitor_check_history",
		"jetpack_monitor_events",
		"jetpack_monitor_event_transitions",
		"jetpack_monitor_audit_log",
		"jetpack_monitor_site_runtime",
		"jetpack_monitor_false_positives",
		"jetpack_monitor_process_health",
		"jetpack_monitor_hosts",
		"jetpack_monitor_site_check_config",
		"jetpack_monitor_site_safety_flags",
		"jetpack_monitor_webhooks",
		"jetpack_monitor_webhook_delivery_attempts",
		"jetpack_monitor_alert_contacts",
		"jetpack_monitor_alert_delivery_attempts",
		"jetmon_process_health",
		"process_health",
	}
}

func (r Runner) captureDBOperationalMetrics(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, phase string, m *RunManifest) (*DBOperationalRun, error) {
	cfg = cfg.Normalize()
	if !cfg.DBOperational.Enabled {
		return nil, nil
	}
	run := DBOperationalRun{
		Phase:           phase,
		Status:          "pass",
		CapturedAt:      time.Now().UTC(),
		Source:          "MySQL SHOW GLOBAL STATUS and SELECT COUNT(*) table snapshots",
		StatusVariables: append([]string(nil), dbOperationalStatusVariables...),
		Tables:          append([]string(nil), cfg.DBOperational.Tables...),
	}
	var errs []error
	for _, service := range services {
		item, err := r.captureServiceDBOperationalMetrics(ctx, service, cfg.DBOperational.Tables)
		if err != nil {
			item.Status = "fail"
			item.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", service.ID, err))
		}
		run.Services = append(run.Services, item)
	}
	if len(errs) > 0 {
		run.Status = "partial"
		run.Error = joinErrors(errs).Error()
	}
	m.DBOperational = append(m.DBOperational, run)
	if err := writeDBOperationalArtifact(dir, "db-operational-"+phase+".json", run, m); err != nil {
		if len(errs) > 0 {
			return &run, joinErrors(append(errs, err))
		}
		return &run, err
	}
	if len(errs) > 0 {
		return &run, joinErrors(errs)
	}
	m.DBOperationalStatus = "running"
	m.DBOperationalError = ""
	return &run, nil
}

func (r Runner) captureServiceDBOperationalMetrics(ctx context.Context, service ServiceLifecycle, tables []string) (DBOperationalService, error) {
	item := DBOperationalService{
		ID:     service.ID,
		Schema: service.Config.Schema,
		Status: "pass",
	}
	if !service.HasDSN {
		return item, fmt.Errorf("service has no DSN")
	}
	statusSQL := renderDBOperationalStatusSQL()
	statusResult, err := r.execServiceSQL(ctx, service, statusSQL)
	if err != nil {
		return item, err
	}
	statusValues := map[string]uint64{}
	for _, row := range firstStatementRows(statusResult) {
		if len(row) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(row[1], 10, 64)
		statusValues[row[0]] = n
	}
	for _, name := range dbOperationalStatusVariables {
		if _, ok := statusValues[name]; !ok {
			statusValues[name] = 0
		}
	}
	item.GlobalStatus = statusValues

	tableSQL := renderDBOperationalTableListSQL(tables)
	tableResult, err := r.execServiceSQL(ctx, service, tableSQL)
	if err != nil {
		return item, err
	}
	existing := map[string]bool{}
	for _, row := range firstStatementRows(tableResult) {
		if len(row) > 0 {
			existing[row[0]] = true
			item.ExistingTables = append(item.ExistingTables, row[0])
		}
	}
	sort.Strings(item.ExistingTables)
	for _, table := range tables {
		if !existing[table] {
			item.MissingTables = append(item.MissingTables, table)
		}
	}
	if len(item.ExistingTables) == 0 {
		item.TableRows = map[string]uint64{}
		return item, nil
	}
	countResult, err := r.execServiceSQL(ctx, service, renderDBOperationalRowCountSQL(item.ExistingTables))
	if err != nil {
		return item, err
	}
	item.TableRows = map[string]uint64{}
	for _, row := range firstStatementRows(countResult) {
		if len(row) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(row[1], 10, 64)
		item.TableRows[row[0]] = n
	}
	return item, nil
}

func (r Runner) writeDBOperationalWindow(ctx context.Context, dir string, start, end DBOperationalRun, m *RunManifest) error {
	if start.Status != "pass" || end.Status != "pass" {
		return fmt.Errorf("cannot calculate DB operational delta from non-pass snapshots start=%s end=%s", start.Status, end.Status)
	}
	run := DBOperationalRun{
		Phase:           "window",
		Status:          "pass",
		CapturedAt:      time.Now().UTC(),
		StartCapturedAt: &start.CapturedAt,
		EndCapturedAt:   &end.CapturedAt,
		ElapsedSeconds:  end.CapturedAt.Sub(start.CapturedAt).Seconds(),
		Source:          "delta between db-operational-start.json and db-operational-end.json",
		StatusVariables: append([]string(nil), dbOperationalStatusVariables...),
		Tables:          append([]string(nil), start.Tables...),
	}
	startByID := map[string]DBOperationalService{}
	for _, service := range start.Services {
		startByID[service.ID] = service
	}
	for _, finish := range end.Services {
		begin, ok := startByID[finish.ID]
		if !ok {
			run.Status = "partial"
			run.Services = append(run.Services, DBOperationalService{ID: finish.ID, Schema: finish.Schema, Status: "fail", Error: "missing start snapshot"})
			continue
		}
		run.Services = append(run.Services, computeDBOperationalServiceDelta(begin, finish))
	}
	m.DBOperational = append(m.DBOperational, run)
	if run.Status == "pass" {
		m.DBOperationalStatus = "pass"
		m.DBOperationalError = ""
	} else {
		m.DBOperationalStatus = "partial"
	}
	return writeDBOperationalArtifact(dir, "db-operational-window.json", run, m)
}

func computeDBOperationalServiceDelta(start, end DBOperationalService) DBOperationalService {
	item := DBOperationalService{
		ID:                  end.ID,
		Schema:              end.Schema,
		Database:            end.Database,
		Status:              "pass",
		GlobalStatusDelta:   map[string]int64{},
		TableRowDelta:       map[string]int64{},
		QueryCounterSource:  "Queries and Questions from SHOW GLOBAL STATUS",
		WriteCounterSource:  "Com_insert + Com_update + Com_delete + Com_replace from SHOW GLOBAL STATUS",
		RowCounterSource:    "Innodb_rows_* from SHOW GLOBAL STATUS",
		TableRowDeltaSource: "SELECT COUNT(*) per existing table before and after the mode window",
	}
	if start.Status != "pass" || end.Status != "pass" {
		item.Status = "fail"
		item.Error = strings.Trim(strings.TrimSpace(start.Error)+"; "+strings.TrimSpace(end.Error), "; ")
		return item
	}
	for _, name := range dbOperationalStatusVariables {
		item.GlobalStatusDelta[name] = int64(end.GlobalStatus[name]) - int64(start.GlobalStatus[name])
	}
	item.DatabaseQueries = firstNonZeroInt64(item.GlobalStatusDelta["Queries"], item.GlobalStatusDelta["Questions"])
	item.DatabaseWrites = item.GlobalStatusDelta["Com_insert"] + item.GlobalStatusDelta["Com_update"] + item.GlobalStatusDelta["Com_delete"] + item.GlobalStatusDelta["Com_replace"]
	item.RowsRead = item.GlobalStatusDelta["Innodb_rows_read"]
	item.RowsAdded = item.GlobalStatusDelta["Innodb_rows_inserted"]
	item.RowsUpdated = item.GlobalStatusDelta["Innodb_rows_updated"]
	item.RowsDeleted = item.GlobalStatusDelta["Innodb_rows_deleted"]

	tableNames := map[string]bool{}
	for name := range start.TableRows {
		tableNames[name] = true
	}
	for name := range end.TableRows {
		tableNames[name] = true
	}
	var sorted []string
	for name := range tableNames {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, table := range sorted {
		item.TableRowDelta[table] = int64(end.TableRows[table]) - int64(start.TableRows[table])
	}
	return item
}

func renderDBOperationalStatusSQL() string {
	var quoted []string
	for _, name := range dbOperationalStatusVariables {
		quoted = append(quoted, quoteSQLString(name))
	}
	return "SHOW GLOBAL STATUS WHERE Variable_name IN (" + strings.Join(quoted, ",") + ");"
}

func renderDBOperationalTableListSQL(tables []string) string {
	if len(tables) == 0 {
		return "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND 1 = 0;"
	}
	var quoted []string
	for _, table := range tables {
		quoted = append(quoted, quoteSQLString(table))
	}
	return "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (" + strings.Join(quoted, ",") + ") ORDER BY TABLE_NAME;"
}

func renderDBOperationalRowCountSQL(tables []string) string {
	var selects []string
	for _, table := range tables {
		selects = append(selects, "SELECT "+quoteSQLString(table)+" AS table_name, COUNT(*) AS row_count FROM "+quoteSQLIdentifier(table))
	}
	return strings.Join(selects, " UNION ALL ") + ";"
}

func firstStatementRows(result SQLExecutionResult) [][]string {
	if len(result.Statements) == 0 {
		return nil
	}
	return result.Statements[0].Rows
}

func quoteSQLString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quoteSQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func sumPositiveDeltas(values map[string]int64) int64 {
	var total int64
	for _, value := range values {
		if value > 0 {
			total += value
		}
	}
	return total
}

func sumNegativeDeltas(values map[string]int64) int64 {
	var total int64
	for _, value := range values {
		if value < 0 {
			total += -value
		}
	}
	return total
}

func latestDBOperationalWindow(runs []DBOperationalRun) *DBOperationalRun {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Phase == "window" {
			return &runs[i]
		}
	}
	return nil
}

func writeDBOperationalArtifact(dir, name string, run DBOperationalRun, m *RunManifest) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal DB operational metrics: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: strings.TrimSuffix(name, ".json"), Path: path})
	return nil
}
