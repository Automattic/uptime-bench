package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/campaign"
	"github.com/Automattic/uptime-bench/internal/capacitybench"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/measurement"
	"github.com/Automattic/uptime-bench/internal/report"
)

const defaultCapacityInstances = "jetmon-v1.example.com,jetmon-v2.example.com"

func main() {
	campaign := flag.String("campaign", "", "campaign run ID or campaign config ID to finalize")
	outDir := flag.String("out-dir", "", "output directory (default reports/<campaign>)")
	dsnFlag := flag.String("dsn", "", "MySQL DSN (overrides DB_DSN env var)")
	derive := flag.Bool("derive", true, "recompute derived metrics before writing reports")
	capacity := flag.Bool("capacity", false, "collect Prometheus capacity metrics for the finalized campaign window and write capacity.md/capacity.json")
	capacityPromURL := flag.String("capacity-prometheus-url", "", "Prometheus base URL for -capacity (overrides CAPACITY_PROMETHEUS_URL/PROMETHEUS_URL env vars)")
	capacityInstances := flag.String("capacity-instances", "", "comma-separated Prometheus instance labels for -capacity (default CAPACITY_INSTANCES env var or Jetmon v1/v2 examples)")
	capacityStep := flag.Duration("capacity-step", 15*time.Second, "Prometheus query_range step for -capacity")
	capacityRateWindow := flag.Duration("capacity-rate-window", 2*time.Minute, "PromQL rate() window for counter-based capacity metrics")
	flag.Parse()

	if *campaign == "" {
		log.Fatal("finalize: -campaign is required")
	}
	if *outDir == "" {
		*outDir = filepath.Join("reports", sanitizePathComponent(*campaign))
	}

	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		log.Fatal("finalize: set -dsn flag or DB_DSN env var")
	}

	database, err := db.Open(dsn)
	if err != nil {
		log.Fatalf("finalize: db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	lookup, err := database.ResolveCampaign(ctx, *campaign)
	if err != nil {
		log.Fatalf("finalize: resolve campaign: %v", err)
	}
	if len(lookup.Runs) == 0 {
		log.Fatalf("finalize: no campaign_runs matched %q", *campaign)
	}
	if *derive {
		for _, run := range lookup.Runs {
			if err := measurement.DeriveCampaign(ctx, database, run.ID); err != nil {
				log.Printf("finalize: derive %s: %v", run.ID, err)
			}
		}
	}

	out, err := loadReport(ctx, database, lookup)
	if err != nil {
		log.Fatalf("finalize: report: %v", err)
	}
	artifacts, err := loadArtifacts(ctx, database, lookup)
	if err != nil {
		log.Fatalf("finalize: artifacts: %v", err)
	}

	var capacityReport *capacitybench.Report
	if *capacity {
		report, err := collectCapacityReport(ctx, out.Meta, capacityOptions{
			prometheusURL: *capacityPromURL,
			instancesRaw:  *capacityInstances,
			step:          *capacityStep,
			rateWindow:    *capacityRateWindow,
		})
		if err != nil {
			log.Fatalf("finalize: capacity: %v", err)
		}
		capacityReport = &report
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("finalize: mkdir %s: %v", *outDir, err)
	}
	if err := writeReportFiles(*outDir, out, capacityReport, artifacts); err != nil {
		log.Fatalf("finalize: write: %v", err)
	}
	log.Printf("finalize: wrote %s", *outDir)
}

type capacityOptions struct {
	prometheusURL string
	instancesRaw  string
	step          time.Duration
	rateWindow    time.Duration
}

func loadReport(ctx context.Context, database *db.DB, lookup *db.CampaignLookup) (report.Report, error) {
	runIDs := make([]string, len(lookup.Runs))
	for i, r := range lookup.Runs {
		runIDs[i] = r.ID
	}
	rows, err := database.CampaignMetricRows(ctx, runIDs)
	if err != nil {
		return report.Report{}, fmt.Errorf("load campaign metrics: %w", err)
	}
	reasonRows, err := database.CampaignReasonRows(ctx, runIDs)
	if err != nil {
		return report.Report{}, fmt.Errorf("load campaign reason codes: %w", err)
	}
	reasonDetailRows, err := database.CampaignReasonDetailRows(ctx, runIDs)
	if err != nil {
		return report.Report{}, fmt.Errorf("load campaign reason details: %w", err)
	}
	summaries := report.Summarize(rows, reasonRows)
	return report.Report{
		Meta:          report.MetaFromLookup(lookup),
		BiasChecks:    report.AnalyzeBias(summaries),
		ServiceScores: report.ScoreMetrics(rows, reasonRows),
		ReasonDetails: report.SummarizeReasonDetails(reasonDetailRows),
		Summaries:     summaries,
	}, nil
}

func collectCapacityReport(ctx context.Context, meta report.Meta, opts capacityOptions) (capacitybench.Report, error) {
	if meta.EarliestStartedAt == nil {
		return capacitybench.Report{}, fmt.Errorf("campaign start time is not available")
	}
	if meta.LatestEndedAt == nil {
		return capacitybench.Report{}, fmt.Errorf("campaign is still in progress; capacity capture requires a completed end time")
	}
	if !meta.LatestEndedAt.After(*meta.EarliestStartedAt) {
		return capacitybench.Report{}, fmt.Errorf("campaign end time must be after start time")
	}
	if opts.step <= 0 {
		return capacitybench.Report{}, fmt.Errorf("-capacity-step must be positive")
	}
	if opts.rateWindow <= 0 {
		return capacitybench.Report{}, fmt.Errorf("-capacity-rate-window must be positive")
	}

	promURL := strings.TrimSpace(opts.prometheusURL)
	if promURL == "" {
		promURL = strings.TrimSpace(os.Getenv("CAPACITY_PROMETHEUS_URL"))
	}
	if promURL == "" {
		promURL = strings.TrimSpace(os.Getenv("PROMETHEUS_URL"))
	}
	if promURL == "" {
		return capacitybench.Report{}, fmt.Errorf("set -capacity-prometheus-url, CAPACITY_PROMETHEUS_URL, or PROMETHEUS_URL")
	}

	instancesRaw := strings.TrimSpace(opts.instancesRaw)
	if env := strings.TrimSpace(os.Getenv("CAPACITY_INSTANCES")); env != "" && instancesRaw == "" {
		instancesRaw = env
	}
	if instancesRaw == "" {
		instancesRaw = defaultCapacityInstances
	}
	instances := splitCSV(instancesRaw)
	instanceRegex, err := capacitybench.InstanceRegex(instances)
	if err != nil {
		return capacitybench.Report{}, fmt.Errorf("instances: %w", err)
	}

	client := &capacitybench.PrometheusClient{
		BaseURL: promURL,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
	summaries, err := capacitybench.Collect(ctx, client, capacitybench.DefaultQueries(instanceRegex, opts.rateWindow), *meta.EarliestStartedAt, *meta.LatestEndedAt, opts.step)
	if err != nil {
		return capacitybench.Report{}, fmt.Errorf("collect: %w", err)
	}
	return capacitybench.Report{
		PrometheusURL: promURL,
		Start:         meta.EarliestStartedAt.UTC(),
		End:           meta.LatestEndedAt.UTC(),
		Step:          opts.step.String(),
		Instances:     instances,
		Summaries:     summaries,
	}, nil
}

type finalizeArtifacts struct {
	CampaignRuns []db.CampaignRunDetail
	Tables       []db.ExportTable
}

func loadArtifacts(ctx context.Context, database *db.DB, lookup *db.CampaignLookup) (*finalizeArtifacts, error) {
	runIDs := make([]string, len(lookup.Runs))
	for i, r := range lookup.Runs {
		runIDs[i] = r.ID
	}
	campaignRuns, err := database.CampaignRunDetails(ctx, runIDs)
	if err != nil {
		return nil, fmt.Errorf("campaign run details: %w", err)
	}
	tables, err := database.CampaignExportTables(ctx, runIDs)
	if err != nil {
		return nil, fmt.Errorf("raw tables: %w", err)
	}
	return &finalizeArtifacts{
		CampaignRuns: campaignRuns,
		Tables:       tables,
	}, nil
}

func writeReportFiles(dir string, r report.Report, capacityReport *capacitybench.Report, artifacts *finalizeArtifacts) error {
	var files []string
	if err := writeReport(dir, "report.json", "json", r); err != nil {
		return err
	}
	files = append(files, "report.json")
	if capacityReport != nil {
		if err := writeCapacityFiles(dir, *capacityReport); err != nil {
			return err
		}
		files = append(files, "capacity.md", "capacity.json", "capacity.txt")
	}
	if artifacts != nil {
		artifactFiles, err := writeFinalizeArtifacts(dir, r, *artifacts)
		if err != nil {
			return err
		}
		files = append(files, artifactFiles...)
	}
	controllerSummary, controllerFiles, err := writeControllerSummaryFiles(dir)
	if err != nil {
		return err
	}
	files = append(files, controllerFiles...)
	if err := writeReportMarkdown(dir, "report.md", r, controllerSummary); err != nil {
		return err
	}
	files = append([]string{"report.md"}, files...)
	existingFiles, err := discoverReportFiles(dir)
	if err != nil {
		return err
	}
	files = mergeManifestFiles(files, existingFiles)
	manifest := map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"input":         r.Meta.Input,
		"campaign_runs": r.Meta.CampaignRuns,
		"files":         files,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "manifest.json"), append(data, '\n'))
}

func discoverReportFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		base := filepath.Base(rel)
		if rel == "manifest.json" || strings.HasPrefix(base, ".") {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func mergeManifestFiles(primary, discovered []string) []string {
	seen := make(map[string]bool, len(primary)+len(discovered))
	out := make([]string, 0, len(primary)+len(discovered))
	for _, name := range primary {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range discovered {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func writeFinalizeArtifacts(dir string, r report.Report, artifacts finalizeArtifacts) ([]string, error) {
	var files []string

	if err := writeRunMeta(filepath.Join(dir, "run.meta.tsv"), filepath.Base(dir), r, artifacts.CampaignRuns); err != nil {
		return nil, err
	}
	files = append(files, "run.meta.tsv")

	for _, table := range artifacts.Tables {
		name := table.Name + ".tsv"
		if err := writeTSVFile(filepath.Join(dir, name), table.Header, table.Rows); err != nil {
			return nil, err
		}
		files = append(files, name)
	}

	planRows, scheduleRows, err := campaignPlanRows(artifacts.CampaignRuns, artifacts.Tables)
	if err != nil {
		return nil, err
	}
	if err := writeTSVFile(filepath.Join(dir, "scenario-plan.tsv"), scenarioPlanHeader, planRows); err != nil {
		return nil, err
	}
	files = append(files, "scenario-plan.tsv")
	if err := writeTSVFile(filepath.Join(dir, "schedule.tsv"), scheduleHeader, scheduleRows); err != nil {
		return nil, err
	}
	files = append(files, "schedule.tsv")

	configFiles, err := writeCampaignConfigs(filepath.Join(dir, "campaigns"), artifacts.CampaignRuns)
	if err != nil {
		return nil, err
	}
	files = append(files, configFiles...)

	return files, nil
}

type controllerCleanupSummary struct {
	CapturedAt         string
	CleanupStatus      string
	ActiveFailureCount int
	MemberErrorCount   int
	Members            []controllerMemberSummary
	ParseError         string
}

type controllerMemberSummary struct {
	Role               string
	ConfigIDs          []string
	Address            string
	ControlURL         string
	MemberID           string
	ActiveFailureCount int
	Error              string
}

type controllerStatusSnapshot struct {
	CapturedAt         time.Time                `json:"captured_at"`
	CleanupStatus      string                   `json:"cleanup_status"`
	ActiveFailureCount int                      `json:"active_failure_count"`
	MemberErrorCount   int                      `json:"member_error_count"`
	Members            []controllerMemberStatus `json:"members"`
}

type controllerMemberStatus struct {
	Role       string                  `json:"role"`
	ConfigIDs  []string                `json:"config_ids"`
	Address    string                  `json:"address"`
	ControlURL string                  `json:"control_url"`
	Status     *controllerStatusDetail `json:"status,omitempty"`
	Error      string                  `json:"error,omitempty"`
}

type controllerStatusDetail struct {
	MemberID       string            `json:"member_id"`
	ActiveFailures []json.RawMessage `json:"active_failures"`
}

func writeControllerSummaryFiles(dir string) (*controllerCleanupSummary, []string, error) {
	summary, err := loadControllerCleanupSummary(filepath.Join(dir, "target-status-after.json"))
	if err != nil {
		return nil, nil, err
	}
	if summary == nil {
		return nil, nil, nil
	}
	var b strings.Builder
	writeControllerSummaryMarkdown(&b, *summary, 1)
	if err := writeFileAtomic(filepath.Join(dir, "controller-summary.md"), []byte(b.String())); err != nil {
		return nil, nil, err
	}
	return summary, []string{"controller-summary.md"}, nil
}

func loadControllerCleanupSummary(path string) (*controllerCleanupSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snapshot controllerStatusSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return &controllerCleanupSummary{
			CleanupStatus: "unknown",
			ParseError:    err.Error(),
		}, nil
	}
	summary := &controllerCleanupSummary{
		CleanupStatus:      strings.TrimSpace(snapshot.CleanupStatus),
		ActiveFailureCount: snapshot.ActiveFailureCount,
		MemberErrorCount:   snapshot.MemberErrorCount,
		Members:            make([]controllerMemberSummary, 0, len(snapshot.Members)),
	}
	if !snapshot.CapturedAt.IsZero() {
		summary.CapturedAt = snapshot.CapturedAt.UTC().Format(time.RFC3339Nano)
	}
	if summary.CleanupStatus == "" {
		summary.CleanupStatus = "unknown"
	}
	for _, member := range snapshot.Members {
		row := controllerMemberSummary{
			Role:       member.Role,
			ConfigIDs:  append([]string(nil), member.ConfigIDs...),
			Address:    member.Address,
			ControlURL: member.ControlURL,
			Error:      member.Error,
		}
		if member.Status != nil {
			row.MemberID = member.Status.MemberID
			row.ActiveFailureCount = len(member.Status.ActiveFailures)
		}
		summary.Members = append(summary.Members, row)
	}
	return summary, nil
}

func writeReportMarkdown(dir, name string, r report.Report, controllerSummary *controllerCleanupSummary) error {
	var b strings.Builder
	if err := report.Write(&b, "markdown", r); err != nil {
		return err
	}
	if controllerSummary != nil {
		b.WriteString("\n")
		writeControllerSummaryMarkdown(&b, *controllerSummary, 2)
	}
	return writeFileAtomic(filepath.Join(dir, name), []byte(b.String()))
}

func writeControllerSummaryMarkdown(b *strings.Builder, summary controllerCleanupSummary, headingLevel int) {
	if headingLevel < 1 {
		headingLevel = 1
	}
	heading := strings.Repeat("#", headingLevel)
	fmt.Fprintf(b, "%s Controller Cleanup Summary\n\n", heading)
	if summary.ParseError != "" {
		fmt.Fprintf(b, "Cleanup Status: %s\n", summary.CleanupStatus)
		fmt.Fprintf(b, "Parse Error: %s\n", summary.ParseError)
		return
	}
	fmt.Fprintf(b, "Cleanup Status: %s\n", summary.CleanupStatus)
	if summary.CapturedAt != "" {
		fmt.Fprintf(b, "Captured At: %s\n", summary.CapturedAt)
	}
	fmt.Fprintf(b, "Member Count: %d\n", len(summary.Members))
	fmt.Fprintf(b, "Active Failures: %d\n", summary.ActiveFailureCount)
	fmt.Fprintf(b, "Member Errors: %d\n", summary.MemberErrorCount)
	if len(summary.Members) == 0 {
		return
	}
	b.WriteString("\n")
	fmt.Fprintf(b, "%s# Members\n\n", heading)
	b.WriteString("| Role | Config IDs | Address | Control URL | Member ID | Active Failures | Error |\n")
	b.WriteString("|---|---|---|---|---|---:|---|\n")
	for _, member := range summary.Members {
		fmt.Fprintf(
			b,
			"| %s | %s | %s | %s | %s | %d | %s |\n",
			markdownCell(member.Role),
			markdownCell(strings.Join(member.ConfigIDs, ",")),
			markdownCell(member.Address),
			markdownCell(member.ControlURL),
			markdownCell(member.MemberID),
			member.ActiveFailureCount,
			markdownCell(member.Error),
		)
	}
}

func markdownCell(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func writeReport(dir, name, format string, r report.Report) error {
	var b strings.Builder
	if err := report.Write(&b, format, r); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, name), []byte(b.String()))
}

func writeCapacityFiles(dir string, r capacitybench.Report) error {
	var md strings.Builder
	if err := capacitybench.WriteMarkdown(&md, r); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "capacity.md"), []byte(md.String())); err != nil {
		return err
	}
	var js strings.Builder
	if err := capacitybench.WriteJSON(&js, r); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "capacity.json"), []byte(js.String())); err != nil {
		return err
	}
	var txt strings.Builder
	if err := capacitybench.WriteTable(&txt, r); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "capacity.txt"), []byte(txt.String()))
}

func writeRunMeta(path, runTag string, r report.Report, campaignRuns []db.CampaignRunDetail) error {
	rows := [][]string{
		{"run_tag", runTag},
		{"input", r.Meta.Input},
		{"matched_as_run_id", strconv.FormatBool(r.Meta.MatchedAsRunID)},
		{"matched_as_config_id", strconv.FormatBool(r.Meta.MatchedAsConfigID)},
		{"campaign_runs", strconv.Itoa(r.Meta.CampaignRuns)},
	}
	if r.Meta.EarliestStartedAt != nil {
		rows = append(rows, []string{"started_at_utc", r.Meta.EarliestStartedAt.UTC().Format(time.RFC3339Nano)})
	}
	if r.Meta.LatestEndedAt != nil {
		rows = append(rows, []string{"finished_at_utc", r.Meta.LatestEndedAt.UTC().Format(time.RFC3339Nano)})
	} else if r.Meta.CampaignRuns > 0 {
		rows = append(rows, []string{"finished_at_utc", "in_progress"})
	}
	rows = append(rows,
		[]string{"campaign_run_ids", strings.Join(campaignRunIDs(campaignRuns), ",")},
		[]string{"campaign_ids", strings.Join(campaignIDs(campaignRuns), ",")},
		[]string{"resolution_reasons", strings.Join(campaignResolutionReasons(campaignRuns), ",")},
	)
	return writeTSVFile(path, []string{"key", "value"}, rows)
}

func campaignRunIDs(rows []db.CampaignRunDetail) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func campaignIDs(rows []db.CampaignRunDetail) []string {
	seen := make(map[string]bool, len(rows))
	var out []string
	for _, r := range rows {
		if r.CampaignID == "" || seen[r.CampaignID] {
			continue
		}
		seen[r.CampaignID] = true
		out = append(out, r.CampaignID)
	}
	return out
}

func campaignResolutionReasons(rows []db.CampaignRunDetail) []string {
	seen := make(map[string]bool, len(rows))
	var out []string
	for _, r := range rows {
		reason := r.ResolutionReason
		if reason == "" {
			reason = "in_progress"
		}
		if seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, reason)
	}
	return out
}

var scenarioPlanHeader = []string{
	"campaign_run_id",
	"design_id",
	"failure_label",
	"failure_type",
	"duration_bucket",
	"host_pattern",
	"replays",
	"targets",
	"scenario_duration",
	"seed",
	"params_json",
	"escalation_pattern",
	"escalation_stages_json",
	"mixed_content_escalation",
}

var scheduleHeader = []string{
	"campaign_run_id",
	"scenario_id",
	"design_id",
	"replay_index",
	"offset",
	"planned_start_utc",
	"actual_run_id",
	"actual_started_at_utc",
	"actual_ended_at_utc",
	"resolution_reason",
}

func campaignPlanRows(campaignRuns []db.CampaignRunDetail, tables []db.ExportTable) ([][]string, [][]string, error) {
	actualRuns := scenarioRunsByCampaignAndScenario(tables)
	var planRows [][]string
	var scheduleRows [][]string
	for _, run := range campaignRuns {
		c, err := campaign.Parse([]byte(run.ConfigTOML))
		if err != nil {
			return nil, nil, fmt.Errorf("parse campaign config for %s: %w", run.ID, err)
		}
		plan, err := campaign.Generate(c, run.MasterSeed)
		if err != nil {
			return nil, nil, fmt.Errorf("generate campaign plan for %s: %w", run.ID, err)
		}
		for _, design := range plan.Designs {
			params, err := json.Marshal(design.Params)
			if err != nil {
				return nil, nil, fmt.Errorf("marshal design params for %s/%s: %w", run.ID, design.ID, err)
			}
			escalationPattern := ""
			var stages []byte
			if design.Escalation != nil {
				escalationPattern = design.Escalation.Pattern
				stages, err = json.Marshal(design.Escalation.Stages)
				if err != nil {
					return nil, nil, fmt.Errorf("marshal escalation stages for %s/%s: %w", run.ID, design.ID, err)
				}
			}
			planRows = append(planRows, []string{
				run.ID,
				design.ID,
				design.ReportLabel(),
				design.FailureType,
				design.Cell.DurationBucket,
				design.Cell.HostPattern,
				strconv.Itoa(design.Replays),
				strings.Join(design.Targets, ","),
				design.Duration.String(),
				strconv.FormatInt(design.Seed, 10),
				string(params),
				escalationPattern,
				string(stages),
				strconv.FormatBool(design.HasMixedContentEscalation()),
			})
		}
		for _, slot := range plan.Schedule {
			scenarioID := fmt.Sprintf("%s-%s-r%d", c.ID, slot.DesignID, slot.Index)
			actual := actualRuns[run.ID+"\x00"+scenarioID]
			plannedStart := run.StartedAt.Add(slot.Offset).UTC().Format(time.RFC3339Nano)
			scheduleRows = append(scheduleRows, []string{
				run.ID,
				scenarioID,
				slot.DesignID,
				strconv.Itoa(slot.Index),
				slot.Offset.String(),
				plannedStart,
				actual.ID,
				actual.StartedAt,
				actual.EndedAt,
				actual.ResolutionReason,
			})
		}
	}
	return planRows, scheduleRows, nil
}

type scenarioRunExport struct {
	ID               string
	StartedAt        string
	EndedAt          string
	ResolutionReason string
}

func scenarioRunsByCampaignAndScenario(tables []db.ExportTable) map[string]scenarioRunExport {
	out := make(map[string]scenarioRunExport)
	for _, table := range tables {
		if table.Name != "scenario_runs" {
			continue
		}
		idx := indexHeader(table.Header)
		for _, row := range table.Rows {
			campaignID := tableValue(row, idx, "campaign_id")
			scenarioID := tableValue(row, idx, "scenario_id")
			if campaignID == "" || scenarioID == "" {
				continue
			}
			out[campaignID+"\x00"+scenarioID] = scenarioRunExport{
				ID:               tableValue(row, idx, "id"),
				StartedAt:        tableValue(row, idx, "started_at"),
				EndedAt:          tableValue(row, idx, "ended_at"),
				ResolutionReason: tableValue(row, idx, "resolution_reason"),
			}
		}
	}
	return out
}

func indexHeader(header []string) map[string]int {
	out := make(map[string]int, len(header))
	for i, name := range header {
		out[name] = i
	}
	return out
}

func tableValue(row []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i < 0 || i >= len(row) {
		return ""
	}
	return row[i]
}

func writeCampaignConfigs(dir string, campaignRuns []db.CampaignRunDetail) ([]string, error) {
	if len(campaignRuns) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var files []string
	for _, run := range campaignRuns {
		name := sanitizePathComponent(run.ID) + ".toml"
		path := filepath.Join(dir, name)
		if err := writeFileAtomic(path, []byte(ensureTrailingNewline(run.ConfigTOML))); err != nil {
			return nil, err
		}
		files = append(files, filepath.ToSlash(filepath.Join(filepath.Base(dir), name)))
	}
	return files, nil
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func writeTSVFile(path string, header []string, rows [][]string) error {
	var b strings.Builder
	writeTSVRow(&b, header)
	for _, row := range rows {
		writeTSVRow(&b, row)
	}
	return writeFileAtomic(path, []byte(b.String()))
}

func writeTSVRow(b *strings.Builder, fields []string) {
	for i, field := range fields {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(escapeTSVField(field))
	}
	b.WriteByte('\n')
}

func escapeTSVField(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\t", "\\t")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sanitizePathComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "campaign"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
