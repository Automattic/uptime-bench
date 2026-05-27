package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/jetmoncapacity"
	_ "github.com/go-sql-driver/mysql"
)

var statusVariables = []string{
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

var defaultTables = []string{
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

type snapshot struct {
	CapturedAt      time.Time                    `json:"captured_at"`
	Phase           string                       `json:"phase,omitempty"`
	ConfigPath      string                       `json:"config_path,omitempty"`
	Services        []serviceSnapshot            `json:"services"`
	Source          string                       `json:"source"`
	StatusVariables []string                     `json:"status_variables"`
	Notes           []string                     `json:"notes,omitempty"`
	Deltas          []serviceDelta               `json:"deltas,omitempty"`
	DeltaInputs     *deltaInputs                 `json:"delta_inputs,omitempty"`
	Tables          map[string]map[string]string `json:"tables,omitempty"`
}

type serviceSnapshot struct {
	ID             string            `json:"id"`
	Schema         string            `json:"schema"`
	Database       string            `json:"database,omitempty"`
	CapturedAt     time.Time         `json:"captured_at"`
	Status         string            `json:"status"`
	Error          string            `json:"error,omitempty"`
	GlobalStatus   map[string]uint64 `json:"global_status,omitempty"`
	TableRows      map[string]uint64 `json:"table_rows,omitempty"`
	MissingTables  []string          `json:"missing_tables,omitempty"`
	ExistingTables []string          `json:"existing_tables,omitempty"`
}

type serviceDelta struct {
	ID                  string            `json:"id"`
	Schema              string            `json:"schema"`
	Status              string            `json:"status"`
	Error               string            `json:"error,omitempty"`
	GlobalStatusDelta   map[string]int64  `json:"global_status_delta,omitempty"`
	TableRowDelta       map[string]int64  `json:"table_row_delta,omitempty"`
	DatabaseWrites      int64             `json:"database_writes,omitempty"`
	DatabaseQueries     int64             `json:"database_queries,omitempty"`
	RowsAdded           int64             `json:"rows_added,omitempty"`
	RowsUpdated         int64             `json:"rows_updated,omitempty"`
	RowsDeleted         int64             `json:"rows_deleted,omitempty"`
	TablesAddedRows     map[string]int64  `json:"tables_added_rows,omitempty"`
	TablesRemovedRows   map[string]int64  `json:"tables_removed_rows,omitempty"`
	UnchangedTableRows  map[string]uint64 `json:"unchanged_table_rows,omitempty"`
	StartCapturedAt     time.Time         `json:"start_captured_at,omitempty"`
	EndCapturedAt       time.Time         `json:"end_captured_at,omitempty"`
	ElapsedSeconds      float64           `json:"elapsed_seconds,omitempty"`
	QueryCounterSource  string            `json:"query_counter_source,omitempty"`
	WriteCounterSource  string            `json:"write_counter_source,omitempty"`
	RowCounterSource    string            `json:"row_counter_source,omitempty"`
	TableRowDeltaSource string            `json:"table_row_delta_source,omitempty"`
}

type deltaInputs struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func main() {
	log.SetFlags(0)
	configPath := flag.String("config", "configs/capacity/jetmon.example.toml", "Jetmon capacity TOML config")
	servicesArg := flag.String("services", "all", "comma-separated services: all, jetmon-v1, jetmon-v2")
	outPath := flag.String("out", "", "JSON output path")
	phase := flag.String("phase", "", "snapshot phase label")
	startPath := flag.String("start", "", "start snapshot for delta mode")
	endPath := flag.String("end", "", "end snapshot for delta mode")
	tablesArg := flag.String("tables", "", "comma-separated table names to count; defaults to Jetmon operational tables")
	timeout := flag.Duration("timeout", 20*time.Second, "per-service database timeout")
	flag.Parse()

	if *outPath == "" {
		log.Fatal("db-metrics: -out is required")
	}
	if *startPath != "" || *endPath != "" {
		if *startPath == "" || *endPath == "" {
			log.Fatal("db-metrics: -start and -end must be supplied together")
		}
		if err := writeDelta(*startPath, *endPath, *outPath); err != nil {
			log.Fatalf("db-metrics: %v", err)
		}
		return
	}

	cfg, err := jetmoncapacity.LoadRunConfig(*configPath)
	if err != nil {
		log.Fatalf("db-metrics: load config: %v", err)
	}
	services, err := cfg.ServiceLifecycles(parseServices(*servicesArg))
	if err != nil {
		log.Fatalf("db-metrics: services: %v", err)
	}
	tables := parseTables(*tablesArg)
	if len(tables) == 0 {
		tables = append([]string(nil), defaultTables...)
	}
	sort.Strings(tables)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(services))*(*timeout))
	defer cancel()

	snap := snapshot{
		CapturedAt:      time.Now().UTC(),
		Phase:           strings.TrimSpace(*phase),
		ConfigPath:      *configPath,
		Source:          "mysql SHOW GLOBAL STATUS and SELECT COUNT(*) table snapshots",
		StatusVariables: append([]string(nil), statusVariables...),
		Tables: map[string]map[string]string{
			"row_counts": {"source": "SELECT COUNT(*) FROM each existing table"},
			"status":     {"source": "SHOW GLOBAL STATUS"},
		},
	}
	for _, service := range services {
		serviceCtx, cancelService := context.WithTimeout(ctx, *timeout)
		item := captureService(serviceCtx, service, tables)
		cancelService()
		snap.Services = append(snap.Services, item)
	}
	if err := writeJSON(*outPath, snap); err != nil {
		log.Fatalf("db-metrics: write: %v", err)
	}
}

func parseServices(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseTables(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func captureService(ctx context.Context, service jetmoncapacity.ServiceLifecycle, tables []string) serviceSnapshot {
	out := serviceSnapshot{
		ID:         service.ID,
		Schema:     service.Config.Schema,
		CapturedAt: time.Now().UTC(),
		Status:     "pass",
	}
	if !service.HasDSN {
		out.Status = "fail"
		out.Error = "service has no DSN"
		return out
	}
	db, err := sql.Open("mysql", service.DSN)
	if err != nil {
		out.Status = "fail"
		out.Error = err.Error()
		return out
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		out.Status = "fail"
		out.Error = err.Error()
		return out
	}
	defer conn.Close()
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&out.Database); err != nil {
		out.Status = "fail"
		out.Error = err.Error()
		return out
	}
	status, err := captureStatus(ctx, conn)
	if err != nil {
		out.Status = "fail"
		out.Error = err.Error()
		return out
	}
	out.GlobalStatus = status
	rowCounts, existing, missing, err := captureRowCounts(ctx, conn, tables)
	if err != nil {
		out.Status = "fail"
		out.Error = err.Error()
		return out
	}
	out.TableRows = rowCounts
	out.ExistingTables = existing
	out.MissingTables = missing
	return out
}

func captureStatus(ctx context.Context, conn *sql.Conn) (map[string]uint64, error) {
	query := "SHOW GLOBAL STATUS WHERE Variable_name IN (" + placeholders(len(statusVariables)) + ")"
	args := make([]any, 0, len(statusVariables))
	for _, name := range statusVariables {
		args = append(args, name)
	}
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]uint64{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		n, _ := strconv.ParseUint(value, 10, 64)
		out[name] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, name := range statusVariables {
		if _, ok := out[name]; !ok {
			out[name] = 0
		}
	}
	return out, nil
}

func captureRowCounts(ctx context.Context, conn *sql.Conn, tables []string) (map[string]uint64, []string, []string, error) {
	exists, err := existingTables(ctx, conn, tables)
	if err != nil {
		return nil, nil, nil, err
	}
	counts := map[string]uint64{}
	var existing []string
	var missing []string
	for _, table := range tables {
		if !exists[table] {
			missing = append(missing, table)
			continue
		}
		var count uint64
		query := fmt.Sprintf("SELECT COUNT(*) FROM `%s`", strings.ReplaceAll(table, "`", "``"))
		if err := conn.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", table, err)
		}
		counts[table] = count
		existing = append(existing, table)
	}
	return counts, existing, missing, nil
}

func existingTables(ctx context.Context, conn *sql.Conn, tables []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(tables) == 0 {
		return out, nil
	}
	query := "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN (" + placeholders(len(tables)) + ")"
	args := make([]any, 0, len(tables))
	for _, table := range tables {
		args = append(args, table)
	}
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func writeDelta(startPath, endPath, outPath string) error {
	var start, end snapshot
	if err := readJSON(startPath, &start); err != nil {
		return fmt.Errorf("read start: %w", err)
	}
	if err := readJSON(endPath, &end); err != nil {
		return fmt.Errorf("read end: %w", err)
	}
	startByID := map[string]serviceSnapshot{}
	for _, item := range start.Services {
		startByID[item.ID] = item
	}
	delta := snapshot{
		CapturedAt:      time.Now().UTC(),
		Phase:           "delta",
		ConfigPath:      end.ConfigPath,
		Source:          "difference between db-operational-start.json and db-operational-end.json",
		StatusVariables: append([]string(nil), statusVariables...),
		DeltaInputs:     &deltaInputs{Start: startPath, End: endPath},
	}
	for _, finish := range end.Services {
		begin, ok := startByID[finish.ID]
		if !ok {
			delta.Deltas = append(delta.Deltas, serviceDelta{ID: finish.ID, Schema: finish.Schema, Status: "fail", Error: "missing start snapshot"})
			continue
		}
		delta.Deltas = append(delta.Deltas, computeServiceDelta(begin, finish))
	}
	return writeJSON(outPath, delta)
}

func computeServiceDelta(start, end serviceSnapshot) serviceDelta {
	out := serviceDelta{
		ID:                  end.ID,
		Schema:              end.Schema,
		Status:              "pass",
		GlobalStatusDelta:   map[string]int64{},
		TableRowDelta:       map[string]int64{},
		TablesAddedRows:     map[string]int64{},
		TablesRemovedRows:   map[string]int64{},
		UnchangedTableRows:  map[string]uint64{},
		StartCapturedAt:     start.CapturedAt,
		EndCapturedAt:       end.CapturedAt,
		ElapsedSeconds:      end.CapturedAt.Sub(start.CapturedAt).Seconds(),
		QueryCounterSource:  "Questions and Queries from SHOW GLOBAL STATUS",
		WriteCounterSource:  "Com_insert + Com_update + Com_delete + Com_replace from SHOW GLOBAL STATUS",
		RowCounterSource:    "Innodb_rows_inserted/updated/deleted from SHOW GLOBAL STATUS",
		TableRowDeltaSource: "SELECT COUNT(*) per existing table before and after the mode window",
	}
	if start.Status != "pass" || end.Status != "pass" {
		out.Status = "fail"
		out.Error = strings.TrimSpace(start.Error + "; " + end.Error)
		return out
	}
	for _, name := range statusVariables {
		out.GlobalStatusDelta[name] = int64(end.GlobalStatus[name]) - int64(start.GlobalStatus[name])
	}
	out.DatabaseQueries = firstNonZeroDelta(out.GlobalStatusDelta["Queries"], out.GlobalStatusDelta["Questions"])
	out.DatabaseWrites = out.GlobalStatusDelta["Com_insert"] + out.GlobalStatusDelta["Com_update"] + out.GlobalStatusDelta["Com_delete"] + out.GlobalStatusDelta["Com_replace"]
	out.RowsAdded = out.GlobalStatusDelta["Innodb_rows_inserted"]
	out.RowsUpdated = out.GlobalStatusDelta["Innodb_rows_updated"]
	out.RowsDeleted = out.GlobalStatusDelta["Innodb_rows_deleted"]

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
		d := int64(end.TableRows[table]) - int64(start.TableRows[table])
		out.TableRowDelta[table] = d
		switch {
		case d > 0:
			out.TablesAddedRows[table] = d
		case d < 0:
			out.TablesRemovedRows[table] = -d
		default:
			out.UnchangedTableRows[table] = end.TableRows[table]
		}
	}
	return out
}

func firstNonZeroDelta(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
