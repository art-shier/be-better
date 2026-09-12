package observability

import (
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
)

func (metrics *Metrics) ObserveAgent(observation agentexecution.Observation) {
	if metrics == nil {
		return
	}
	kind := controlledAgentKind(observation.Kind)
	mode := controlledAgentMode(observation.Mode)
	tool := controlledAgentTool(observation.ToolID)
	profile := controlledAgentProfile(observation.ModelProfile)
	outcome := controlledAgentOutcome(observation.Outcome)
	code := controlledAgentErrorCode(observation.ErrorCode)

	if kind == "background_slot" {
		switch outcome {
		case "started":
			metrics.agentActiveBackgroundSlots.Inc()
		case "completed", "failed", "cancelled":
			metrics.agentActiveBackgroundSlots.Dec()
		}
		if observation.QueueWait > 0 {
			metrics.agentQueueWait.WithLabelValues(mode, profile).Observe(observation.QueueWait.Seconds())
		}
		return
	}

	if kind == "run" {
		metrics.agentRuns.WithLabelValues(mode, profile, outcome, code).Inc()
		metrics.agentRunDuration.WithLabelValues(mode, profile, outcome, code).Observe(observation.Duration.Seconds())
		return
	}

	labels := []string{kind, mode, tool, profile, outcome, code}
	metrics.agentOperations.WithLabelValues(labels...).Inc()
	metrics.agentOperationDuration.WithLabelValues(labels...).Observe(observation.Duration.Seconds())
	if observation.CancelLatency > 0 {
		metrics.agentCancelLatency.WithLabelValues(labels...).Observe(observation.CancelLatency.Seconds())
	}
	if kind != "provider" || !terminalAgentOutcome(outcome) {
		return
	}
	validUsage := validAgentUsage(observation.Usage)
	if validUsage {
		metrics.agentUsageTokens.WithLabelValues(kind, mode, tool, profile, "input").Add(float64(observation.Usage.InputTokens))
		metrics.agentUsageTokens.WithLabelValues(kind, mode, tool, profile, "output").Add(float64(observation.Usage.OutputTokens))
		metrics.agentUsageTokens.WithLabelValues(kind, mode, tool, profile, "total").Add(float64(observation.Usage.TotalTokens))
	}
	if !observation.UsageComplete || !validUsage {
		metrics.agentUsageUnknown.WithLabelValues(kind, mode, tool, profile, outcome, code).Inc()
	}
	if observation.Attempts > 1 {
		metrics.agentRetries.WithLabelValues(mode, profile, outcome, code).Inc()
	}
}

func controlledAgentKind(value string) string {
	switch value {
	case "run", "provider", "tool", "background_slot":
		return value
	default:
		return "unknown"
	}
}

func controlledAgentMode(value string) string {
	switch agentprotocol.ExecutionMode(value) {
	case agentprotocol.ExecutionModeForeground, agentprotocol.ExecutionModeBackground:
		return value
	default:
		return "unknown"
	}
}

func controlledAgentTool(value string) string {
	switch value {
	case "skill_list", "skill_load", "dayorder.calendar.read":
		return value
	default:
		return "unknown"
	}
}

func controlledAgentProfile(value string) string {
	switch value {
	case "readonly-default", "readonly-fake", "readonly-deepseek":
		return value
	default:
		return "unknown"
	}
}

func controlledAgentOutcome(value string) string {
	switch value {
	case "started", "completed", "failed", "cancelled":
		return value
	default:
		return "unknown"
	}
}

func controlledAgentErrorCode(value string) string {
	switch agentprotocol.ErrorCode(value) {
	case "":
		return "none"
	case agentprotocol.ErrorCodeApprovalDenied,
		agentprotocol.ErrorCodeCancelled,
		agentprotocol.ErrorCodeCapabilityUnavailable,
		agentprotocol.ErrorCodeInternalError,
		agentprotocol.ErrorCodePermissionDenied,
		agentprotocol.ErrorCodeProtocolIncompatible,
		agentprotocol.ErrorCodeProviderRateLimited,
		agentprotocol.ErrorCodeProviderUnavailable,
		agentprotocol.ErrorCodeTimeout,
		agentprotocol.ErrorCodeToolFailed,
		agentprotocol.ErrorCodeValidationFailed,
		agentprotocol.ErrorCodeVersionConflict:
		return value
	default:
		return "unknown"
	}
}

func terminalAgentOutcome(outcome string) bool {
	return outcome == "completed" || outcome == "failed" || outcome == "cancelled"
}

func validAgentUsage(usage agentprotocol.Usage) bool {
	return usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.TotalTokens >= 0 &&
		usage.TotalTokens == usage.InputTokens+usage.OutputTokens
}
