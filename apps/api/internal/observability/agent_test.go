package observability

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestAgentMetricsExposeOnlyControlledLabels(t *testing.T) {
	metrics := NewMetrics("agent-integration", nil, nil)
	metrics.ObserveAgent(agentexecution.Observation{
		Kind: "tool", Mode: "background", ToolID: "dayorder.calendar.read",
		ModelProfile: "readonly-default", Outcome: "failed", ErrorCode: "timeout",
	})
	metrics.ObserveAgent(agentexecution.Observation{
		Kind: "canary-kind", Mode: "canary-mode", ToolID: "canary-tool-cookie",
		ModelProfile: "canary-profile-key", Outcome: "canary-outcome", ErrorCode: "canary-error",
	})

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, "dayorder_agent_operations_total") {
		t.Fatal("agent metrics missing")
	}
	for _, forbidden := range []string{"runId", "canary", "cookie", "key"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("metrics contain uncontrolled label fragment %q:\n%s", forbidden, body)
		}
	}
}

func TestAgentMetricsCountAttemptUsageRetriesRunsAndSlotOwnership(t *testing.T) {
	metrics := NewMetrics("agent-integration", nil, nil)
	known := agentexecution.Observation{
		Kind: "provider", Mode: "background", ModelProfile: "readonly-default",
		Outcome: "failed", ErrorCode: "provider_unavailable", Duration: 40 * time.Millisecond,
		Usage: agentprotocol.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, UsageComplete: true, Attempts: 1,
	}
	unknownRetry := agentexecution.Observation{
		Kind: "provider", Mode: "background", ModelProfile: "readonly-default",
		Outcome: "completed", Duration: 60 * time.Millisecond, UsageComplete: false, Attempts: 2,
	}
	partialKnown := agentexecution.Observation{
		Kind: "provider", Mode: "background", ModelProfile: "readonly-default",
		Outcome: "failed", ErrorCode: "protocol_incompatible",
		Usage: agentprotocol.Usage{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}, UsageComplete: false, Attempts: 1,
	}
	metrics.ObserveAgent(known)
	metrics.ObserveAgent(unknownRetry)
	metrics.ObserveAgent(partialKnown)
	metrics.ObserveAgent(agentexecution.Observation{
		Kind: "run", Mode: "background", ModelProfile: "readonly-default", Outcome: "completed", Duration: 150 * time.Millisecond,
	})
	metrics.ObserveAgent(agentexecution.Observation{
		Kind: "background_slot", Mode: "background", ModelProfile: "readonly-default", Outcome: "started", QueueWait: 25 * time.Millisecond,
	})
	metrics.ObserveAgent(agentexecution.Observation{
		Kind: "background_slot", Mode: "background", ModelProfile: "readonly-default", Outcome: "canary-terminal",
	})
	if got := testutil.ToFloat64(metrics.agentActiveBackgroundSlots); got != 1 {
		t.Fatalf("active slots after unknown terminal = %v, want 1", got)
	}
	metrics.ObserveAgent(agentexecution.Observation{
		Kind: "background_slot", Mode: "background", ModelProfile: "readonly-default", Outcome: "completed",
	})

	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"provider operations", testutil.ToFloat64(metrics.agentOperations.WithLabelValues("provider", "background", "unknown", "readonly-default", "failed", "provider_unavailable")), 1},
		{"run outcomes", testutil.ToFloat64(metrics.agentRuns.WithLabelValues("background", "readonly-default", "completed", "none")), 1},
		{"input usage", testutil.ToFloat64(metrics.agentUsageTokens.WithLabelValues("provider", "background", "unknown", "readonly-default", "input")), 8},
		{"output usage", testutil.ToFloat64(metrics.agentUsageTokens.WithLabelValues("provider", "background", "unknown", "readonly-default", "output")), 5},
		{"total usage", testutil.ToFloat64(metrics.agentUsageTokens.WithLabelValues("provider", "background", "unknown", "readonly-default", "total")), 13},
		{"unknown usage attempts", testutil.ToFloat64(metrics.agentUsageUnknown.WithLabelValues("provider", "background", "unknown", "readonly-default", "completed", "none")), 1},
		{"partial known usage attempts", testutil.ToFloat64(metrics.agentUsageUnknown.WithLabelValues("provider", "background", "unknown", "readonly-default", "failed", "protocol_incompatible")), 1},
		{"retry attempts", testutil.ToFloat64(metrics.agentRetries.WithLabelValues("background", "readonly-default", "completed", "none")), 1},
		{"released active slots", testutil.ToFloat64(metrics.agentActiveBackgroundSlots), 0},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, metric := range []string{
		"dayorder_agent_operation_duration_seconds", "dayorder_agent_run_duration_seconds",
		"dayorder_agent_queue_wait_seconds", "dayorder_agent_usage_unknown_total",
		"dayorder_agent_retries_total", "dayorder_agent_active_background_slots", "go_goroutines",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("metrics output missing %s", metric)
		}
	}
}

func TestAgentMetricsRejectInvalidUsageCountsWithoutPanicking(t *testing.T) {
	metrics := NewMetrics("agent-integration", nil, nil)
	invalid := []agentprotocol.Usage{
		{InputTokens: -1, OutputTokens: 1, TotalTokens: 0},
		{InputTokens: 1, OutputTokens: 1, TotalTokens: 3},
	}
	for _, usage := range invalid {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("ObserveAgent(%#v) panicked: %v", usage, recovered)
				}
			}()
			metrics.ObserveAgent(agentexecution.Observation{
				Kind: "provider", Mode: "foreground", ModelProfile: "readonly-default",
				Outcome: "failed", ErrorCode: "internal_error", Usage: usage, UsageComplete: true,
			})
		}()
	}

	for _, component := range []string{"input", "output", "total"} {
		if got := testutil.ToFloat64(metrics.agentUsageTokens.WithLabelValues("provider", "foreground", "unknown", "readonly-default", component)); got != 0 {
			t.Errorf("invalid %s token total = %v, want 0", component, got)
		}
	}
	if got := testutil.ToFloat64(metrics.agentUsageUnknown.WithLabelValues("provider", "foreground", "unknown", "readonly-default", "failed", "internal_error")); got != 2 {
		t.Errorf("invalid usage incomplete markers = %v, want 2", got)
	}
}
