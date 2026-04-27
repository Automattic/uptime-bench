package runner

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/campaign"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

// campaignRecorder is the additional interface RunCampaign needs
// beyond the per-scenario recorder. *db.DB satisfies it structurally.
type campaignRecorder interface {
	recorder
	InsertCampaignRun(ctx context.Context, r db.CampaignRunRecord) error
	CloseCampaignRun(ctx context.Context, campaignRunID string, endedAt time.Time, reason string) error
}

// RunCampaignOptions carries optional metadata recorded on the
// campaign_runs row at start time. None of these fields affect
// runtime behaviour; they exist for the methodology audit trail
// (a reader of a published comparison post must be able to recover
// the exact code state that produced the data — see ROADMAP.md
// "Methodology audit trail").
type RunCampaignOptions struct {
	// ConfigTOML is the verbatim campaign config; usually the bytes
	// the parser was given. RunCampaign records it on the row so the
	// row alone is enough for replay.
	ConfigTOML string

	// AdapterVersions maps service IDs to commit SHAs for the adapter
	// implementations active at campaign start. When the runner-side
	// of the harness pins this from cmd/harness, downstream readers
	// can re-check out the exact tree.
	AdapterVersions map[string]string

	// TargetFleetVersion is the commit SHA of the target / DNS /
	// harness binaries. Same audit-trail purpose as AdapterVersions.
	TargetFleetVersion string
}

// RunCampaign drives a campaign end-to-end: generates the deterministic
// Plan from (campaign config, masterSeed), persists a campaign_runs
// row with the audit-trail columns, walks the schedule serially, and
// invokes runner.Run() for each replay. Returns the campaign run ID
// for downstream queries.
//
// First-iteration scope (serial): replays run one at a time. Concurrent
// execution is the explicit deferred design question in ROADMAP.md.
//
// Failure isolation: per-replay errors are logged but do NOT abort the
// campaign. A run that records adapter_error or aborted is data; the
// campaign continues to the next slot. Only a Plan-level failure
// (campaign.Generate returning an error) or a database failure on
// InsertCampaignRun aborts before any replay starts.
//
// Caveat: campaign.Generate currently produces multi-host designs for
// host_pattern = "two_random" / "all", but the scenario format only
// supports single-host scenarios. Multi-host replays surface as
// per-replay errors from Design.ToScenario and are skipped — the
// campaign still completes; those cells just have zero samples. The
// scenario-format multi-host extension is on the roadmap; until it
// lands, configure campaigns with patterns = ["single"] only.
func RunCampaign(
	ctx context.Context,
	c *campaign.Campaign,
	masterSeed int64,
	fl *fleet.Config,
	database campaignRecorder,
	adapters []adapter.Adapter,
	svcCfg *serviceconfig.Config,
	opts RunCampaignOptions,
) (string, error) {
	if c == nil {
		return "", fmt.Errorf("runner: RunCampaign: nil campaign")
	}

	plan, err := campaign.Generate(c, masterSeed)
	if err != nil {
		return "", fmt.Errorf("runner: RunCampaign: %w", err)
	}

	campaignRunID := newRunID()
	startedAt := time.Now()

	if err := database.InsertCampaignRun(ctx, db.CampaignRunRecord{
		ID:                 campaignRunID,
		CampaignID:         c.ID,
		ConfigTOML:         opts.ConfigTOML,
		MasterSeed:         masterSeed,
		StartedAt:          startedAt,
		AdapterVersions:    opts.AdapterVersions,
		TargetFleetVersion: opts.TargetFleetVersion,
	}); err != nil {
		return "", fmt.Errorf("runner: RunCampaign: insert campaign row: %w", err)
	}

	resolutionReason := "planned_completion"
	defer func() {
		if err := database.CloseCampaignRun(context.Background(), campaignRunID, time.Now(), resolutionReason); err != nil {
			log.Printf("runner: RunCampaign: close campaign row: %v", err)
		}
		log.Printf("runner: campaign %s closed: %s", campaignRunID, resolutionReason)
	}()

	log.Printf("runner: campaign %s started: %d designs, %d replays scheduled over %v",
		campaignRunID, len(plan.Designs), len(plan.Schedule), c.Duration)

	// Index designs by ID for the schedule walk.
	designByID := make(map[string]*campaign.Design, len(plan.Designs))
	for i := range plan.Designs {
		designByID[plan.Designs[i].ID] = &plan.Designs[i]
	}

	// Active monitors come from the adapter list at campaign start —
	// every replay tests every enabled service, so per-service
	// comparisons span the same scenario distribution.
	monitors := make([]string, len(adapters))
	for i, a := range adapters {
		monitors[i] = a.ServiceID()
	}

	for _, slot := range plan.Schedule {
		// Wait until the slot's offset relative to campaign start.
		// Slots are sorted; sleeping until each one keeps the runner
		// honest about the configured cadence.
		if waitFor := time.Until(startedAt.Add(slot.Offset)); waitFor > 0 {
			select {
			case <-time.After(waitFor):
			case <-ctx.Done():
				resolutionReason = "aborted"
				return campaignRunID, ctx.Err()
			}
		}

		d := designByID[slot.DesignID]
		if d == nil {
			// Defensive: schedule references an unknown design.
			// Generator produces stable IDs so this shouldn't happen,
			// but skip-and-continue is the right behaviour.
			log.Printf("runner: campaign %s: schedule references unknown design %s; skipping", campaignRunID, slot.DesignID)
			continue
		}

		scenarioID := fmt.Sprintf("%s-%s-r%d", c.ID, d.ID, slot.Index)
		sc, err := d.ToScenario(scenarioID, monitors, c.CheckFrequency, c.GracePeriod)
		if err != nil {
			// Most likely cause: multi-host design from a host pattern
			// the scenario format doesn't yet support. Log and skip;
			// the cell takes a zero-sample loss but the campaign
			// continues.
			log.Printf("runner: campaign %s: design %s replay %d: ToScenario: %v", campaignRunID, d.ID, slot.Index, err)
			continue
		}

		if _, err := Run(ctx, sc, fl, database, adapters, svcCfg, WithCampaignRunID(campaignRunID)); err != nil {
			// Per-replay failure isolation: log and continue. The
			// scenario_runs row carries its own resolution_reason for
			// downstream querying.
			log.Printf("runner: campaign %s: design %s replay %d: %v", campaignRunID, d.ID, slot.Index, err)
		}
	}

	return campaignRunID, nil
}
