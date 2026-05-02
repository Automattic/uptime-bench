package providerstate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/fleet"
)

type ServiceSummary struct {
	ServiceID    string
	Supported    bool
	Found        int
	WouldDelete  int
	Deleted      int
	Skipped      int
	Errors       int
	Error        string
	ReasonCounts map[string]int
	KindCounts   map[string]int
	Actions      []adapter.CleanupAction
}

func ScopeFromFleet(fl *fleet.Config) adapter.CleanupScope {
	if fl == nil {
		return adapter.CleanupScope{}
	}

	hosts := make(map[string]struct{})
	urls := make(map[string]struct{})
	for _, target := range fl.Targets {
		for _, site := range target.Sites {
			host := strings.TrimSpace(site.Host)
			if host == "" {
				continue
			}
			hosts[host] = struct{}{}
			paths := site.Paths
			if len(paths) == 0 {
				paths = []string{"/"}
			}
			for _, path := range paths {
				path = strings.TrimSpace(path)
				if path == "" {
					path = "/"
				}
				if !strings.HasPrefix(path, "/") {
					path = "/" + path
				}
				urls["http://"+host+path] = struct{}{}
				urls["https://"+host+path] = struct{}{}
			}
		}
	}

	return adapter.CleanupScope{
		TargetHosts: sortedKeys(hosts),
		TargetURLs:  sortedKeys(urls),
	}
}

func Run(ctx context.Context, adapters []adapter.Adapter, opts adapter.CleanupOptions) []ServiceSummary {
	summaries := make([]ServiceSummary, 0, len(adapters))
	for _, a := range adapters {
		summary := ServiceSummary{
			ServiceID:    a.ServiceID(),
			ReasonCounts: map[string]int{},
			KindCounts:   map[string]int{},
			Actions:      nil,
		}
		cleaner, ok := a.(adapter.StaleCleaner)
		if !ok {
			summary.Supported = false
			summary.Skipped = 1
			summary.Actions = []adapter.CleanupAction{{
				Candidate: adapter.CleanupCandidate{
					ServiceID: a.ServiceID(),
					Kind:      "provider-state-cleanup",
					Reason:    "adapter does not implement stale cleanup",
				},
				Action: adapter.CleanupActionSkipped,
			}}
			tallyCleanupAction(&summary, summary.Actions[0])
			summaries = append(summaries, summary)
			continue
		}

		summary.Supported = true
		result, err := cleaner.CleanupStale(ctx, opts)
		if err != nil {
			summary.Error = err.Error()
			summary.Actions = append(summary.Actions, adapter.CleanupAction{
				Candidate: adapter.CleanupCandidate{
					ServiceID: a.ServiceID(),
					Kind:      "provider-state-cleanup",
					Reason:    "cleanup failed",
				},
				Action: adapter.CleanupActionError,
				Error:  err.Error(),
			})
		}
		summary.Actions = append(summary.Actions, result.Actions...)
		for _, action := range summary.Actions {
			tallyCleanupAction(&summary, action)
			if action.Candidate.ResourceID != "" {
				summary.Found++
			}
			switch action.Action {
			case adapter.CleanupActionWouldDelete:
				summary.WouldDelete++
			case adapter.CleanupActionDeleted:
				summary.Deleted++
			case adapter.CleanupActionSkipped:
				summary.Skipped++
			case adapter.CleanupActionError:
				summary.Errors++
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func FormatSummary(s ServiceSummary) string {
	status := "supported"
	if !s.Supported {
		status = "unsupported"
	}
	if s.Error != "" {
		status = "error"
	}
	return fmt.Sprintf(
		"%s\t%s\tfound=%d\twould_delete=%d\tdeleted=%d\tskipped=%d\terrors=%d\treasons=%s\tkinds=%s",
		s.ServiceID,
		status,
		s.Found,
		s.WouldDelete,
		s.Deleted,
		s.Skipped,
		s.Errors,
		formatIntCounts(s.ReasonCounts),
		formatIntCounts(s.KindCounts),
	)
}

func tallyCleanupAction(summary *ServiceSummary, action adapter.CleanupAction) {
	if summary == nil {
		return
	}
	reason := strings.TrimSpace(action.Candidate.Reason)
	if reason == "" {
		reason = "<none>"
	}
	kind := strings.TrimSpace(action.Candidate.Kind)
	if kind == "" {
		kind = "<none>"
	}
	summary.ReasonCounts[reason]++
	summary.KindCounts[kind]++
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatIntCounts(values map[string]int) string {
	if len(values) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, values[k]))
	}
	return strings.Join(parts, ",")
}
