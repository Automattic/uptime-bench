package runner

import (
	"testing"

	"github.com/Automattic/uptime-bench/internal/campaign"
)

func TestCampaignRunParametersFlagsMixedContentEscalation(t *testing.T) {
	d := &campaign.Design{
		ID:   "d-mixed",
		Cell: campaign.Cell{FailureType: "http_body", DurationBucket: "brief", HostPattern: campaign.HostPatternSingle},
		Escalation: &campaign.EscalationDesign{
			Pattern: campaign.EscalationPatternLayered,
			Stages: []campaign.EscalationStage{
				{FailureType: "http_body", Params: map[string]any{"content": "keyword_injected"}},
				{FailureType: "http_body", Params: map[string]any{"content": "defacement"}},
			},
		},
	}

	params := campaignRunParameters(d, campaign.ReplaySlot{DesignID: "d-mixed", Index: 2})
	if params["campaign_mixed_content_escalation"] != true {
		t.Fatalf("campaign_mixed_content_escalation = %#v, want true", params["campaign_mixed_content_escalation"])
	}
	if params["campaign_failure_label"] != "layered:http_body>http_body" {
		t.Fatalf("campaign_failure_label = %#v", params["campaign_failure_label"])
	}
}

func TestMixedContentEscalationCount(t *testing.T) {
	designs := []campaign.Design{
		{
			ID: "mixed",
			Escalation: &campaign.EscalationDesign{Stages: []campaign.EscalationStage{
				{FailureType: "http_body", Params: map[string]any{"content": "keyword_injected"}},
				{FailureType: "http_body", Params: map[string]any{"content": "ransomware"}},
			}},
		},
		{
			ID: "plain",
			Escalation: &campaign.EscalationDesign{Stages: []campaign.EscalationStage{
				{FailureType: "http_body", Params: map[string]any{"content": "keyword_injected"}},
				{FailureType: "tcp_refused"},
			}},
		},
	}

	if got := mixedContentEscalationCount(designs); got != 1 {
		t.Fatalf("mixedContentEscalationCount = %d, want 1", got)
	}
}
