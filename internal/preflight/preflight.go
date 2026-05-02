// Package preflight validates benchmark inputs before the harness starts
// mutating target or provider state.
package preflight

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/adapterfactory"
	"github.com/Automattic/uptime-bench/internal/campaign"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/runplan"
	"github.com/Automattic/uptime-bench/internal/scenario"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

// Diagnostic is one preflight finding.
type Diagnostic struct {
	Level   string
	Subject string
	Message string
}

// ScenarioResult records the validated shape of one scenario file.
type ScenarioResult struct {
	Path     string
	ID       string
	Target   string
	Monitors []string
	Runtime  string
}

// CampaignResult records generated campaign timing and coverage diagnostics.
type CampaignResult struct {
	Path                 string
	ID                   string
	Designs              int
	Replays              int
	ConfiguredDuration   string
	ScheduledSpan        string
	SerialRuntime        string
	MaxScenarioRuntime   string
	MaxStartsInHour      int
	MaxConcurrentSamples int
}

// Report is the full preflight output.
type Report struct {
	Scenarios   []ScenarioResult
	Campaigns   []CampaignResult
	Diagnostics []Diagnostic
}

// HasErrors reports whether the preflight found a condition that should stop a
// run before provider APIs are called.
func (r Report) HasErrors() bool {
	for _, d := range r.Diagnostics {
		if d.Level == "error" {
			return true
		}
	}
	return false
}

// Check validates the given inputs. It is intentionally I/O-light: config files
// are parsed and adapter factories are instantiated to catch auth/config shape
// problems, but no provider API calls are made.
func Check(fleetPath, servicesPath string, scenarioPaths, campaignPaths []string) Report {
	var r Report
	fl, err := fleet.Load(fleetPath)
	if err != nil {
		r.errorf(fleetPath, "load fleet: %v", err)
		return r
	}
	svcCfg, err := serviceconfig.Load(servicesPath)
	if err != nil {
		r.errorf(servicesPath, "load services: %v", err)
		return r
	}

	for _, path := range scenarioPaths {
		checkScenario(path, fl, svcCfg, &r)
	}
	for _, path := range campaignPaths {
		checkCampaign(path, fl, svcCfg, &r)
	}
	return r
}

func checkScenario(path string, fl *fleet.Config, svcCfg *serviceconfig.Config, r *Report) {
	data, err := os.ReadFile(path)
	if err != nil {
		r.errorf(path, "read scenario: %v", err)
		return
	}
	sc, err := scenario.Parse(data)
	if err != nil {
		r.errorf(path, "parse scenario: %v", err)
		return
	}
	if !targetExists(fl, sc.Target) {
		r.errorf(path, "target %q not found in fleet config", sc.Target)
	}
	if _, err := adapterfactory.ForScenario(svcCfg, sc.Monitors); err != nil {
		r.errorf(path, "monitor config: %v", err)
	}
	checkRegions(path, sc, svcCfg, r)
	r.Scenarios = append(r.Scenarios, ScenarioResult{
		Path:     path,
		ID:       sc.ID,
		Target:   sc.Target,
		Monitors: append([]string(nil), sc.Monitors...),
		Runtime:  runplan.ScenarioRuntime(sc).String(),
	})
}

func checkCampaign(path string, fl *fleet.Config, svcCfg *serviceconfig.Config, r *Report) {
	data, err := os.ReadFile(path)
	if err != nil {
		r.errorf(path, "read campaign: %v", err)
		return
	}
	c, err := campaign.Parse(data)
	if err != nil {
		r.errorf(path, "parse campaign: %v", err)
		return
	}
	services, err := adapterfactory.Enabled(svcCfg)
	if err != nil {
		r.errorf(path, "enabled service config: %v", err)
	}
	for _, targetID := range c.Targets.Pool {
		if !targetExists(fl, targetID) {
			r.errorf(path, "campaign target %q not found in fleet config", targetID)
		}
	}
	for _, pattern := range c.Targets.Patterns {
		if pattern != campaign.HostPatternSingle {
			r.errorf(path, "host pattern %q requires multi-host scenario support; current campaign runner supports only %q", pattern, campaign.HostPatternSingle)
		}
	}

	plan, err := campaign.Generate(c, c.Seed)
	if err != nil {
		r.errorf(path, "generate campaign plan: %v", err)
		return
	}
	monitors := serviceIDs(services)
	for _, d := range plan.Designs {
		if _, err := d.ToScenario(d.ID+"-preflight", monitors, c.CheckFrequency, c.GracePeriod); err != nil {
			r.errorf(path, "translate design %s: %v", d.ID, err)
		}
	}
	est := runplan.EstimateCampaign(c, plan)
	r.Campaigns = append(r.Campaigns, CampaignResult{
		Path:                 path,
		ID:                   c.ID,
		Designs:              est.Designs,
		Replays:              est.Replays,
		ConfiguredDuration:   est.ConfiguredDuration.String(),
		ScheduledSpan:        est.ScheduledSpan.String(),
		SerialRuntime:        est.SerialRuntime.String(),
		MaxScenarioRuntime:   est.MaxScenarioRuntime.String(),
		MaxStartsInHour:      est.MaxStartsInHour,
		MaxConcurrentSamples: est.MaxConcurrentSamples,
	})
}

func checkRegions(path string, sc *scenario.Scenario, svcCfg *serviceconfig.Config, r *Report) {
	if sc == nil || svcCfg == nil {
		return
	}
	servicesByID := map[string]serviceconfig.Service{}
	for _, svc := range svcCfg.Services {
		servicesByID[svc.ID] = svc
	}
	for _, f := range sc.Failures {
		for _, region := range f.Regions {
			var missing []string
			for _, monitorID := range sc.Monitors {
				svc, ok := servicesByID[monitorID]
				if !ok || !svc.Enabled {
					continue
				}
				if len(svc.ProbeRanges[region]) == 0 {
					missing = append(missing, monitorID)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				r.warnf(path, "region %q has no probe_ranges entry for enabled monitor(s): %s", region, strings.Join(missing, ","))
			}
		}
	}
}

func targetExists(fl *fleet.Config, id string) bool {
	if fl == nil {
		return false
	}
	for _, t := range fl.Targets {
		if t.ID == id {
			return true
		}
	}
	return false
}

func serviceIDs(services []adapter.Adapter) []string {
	out := make([]string, 0, len(services))
	for _, svc := range services {
		out = append(out, svc.ServiceID())
	}
	return out
}

func (r *Report) errorf(subject, format string, args ...any) {
	r.Diagnostics = append(r.Diagnostics, Diagnostic{Level: "error", Subject: subject, Message: fmt.Sprintf(format, args...)})
}

func (r *Report) warnf(subject, format string, args ...any) {
	r.Diagnostics = append(r.Diagnostics, Diagnostic{Level: "warn", Subject: subject, Message: fmt.Sprintf(format, args...)})
}
