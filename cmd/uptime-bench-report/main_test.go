package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/db"
)

func TestLogLookup_DisclosesMatchType(t *testing.T) {
	cases := []struct {
		name string
		in   *db.CampaignLookup
		want string
	}{
		{
			name: "nil lookup",
			in:   nil,
			want: "no campaign_runs matched",
		},
		{
			name: "no match",
			in:   &db.CampaignLookup{Input: "missing"},
			want: "no campaign_runs matched",
		},
		{
			name: "run id",
			in: &db.CampaignLookup{
				Input:          "run-1",
				MatchedAsRunID: true,
				Runs:           []db.CampaignRunSummary{{ID: "run-1", CampaignID: "weekly", StartedAt: time.Now()}},
			},
			want: "matched as campaign_runs.id",
		},
		{
			name: "config id",
			in: &db.CampaignLookup{
				Input:             "weekly",
				MatchedAsConfigID: true,
				Runs: []db.CampaignRunSummary{
					{ID: "run-1", CampaignID: "weekly", StartedAt: time.Now()},
					{ID: "run-2", CampaignID: "weekly", StartedAt: time.Now()},
				},
			},
			want: "matched as stable campaign_id",
		},
		{
			name: "both",
			in: &db.CampaignLookup{
				Input:             "collision",
				MatchedAsRunID:    true,
				MatchedAsConfigID: true,
				Runs:              []db.CampaignRunSummary{{ID: "collision", CampaignID: "collision", StartedAt: time.Now()}},
			},
			want: "matched both",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			oldOutput := log.Writer()
			oldFlags := log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			t.Cleanup(func() {
				log.SetOutput(oldOutput)
				log.SetFlags(oldFlags)
			})

			logLookup(tc.in)
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("log output = %q, want %q", buf.String(), tc.want)
			}
		})
	}
}
