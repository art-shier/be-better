package agentgateway

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agentruntime"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

const reservationRegressionIntent = "概览授权时间窗内日程。"

var reservationRegressionRunID = uuid.MustParse("11111111-1111-4111-8111-111111111111")

type reservationRegressionBinding struct {
	spec   agentprotocol.ToolSpec
	result agentprotocol.ToolResult
	calls  *int
}

func (binding reservationRegressionBinding) Spec() agentprotocol.ToolSpec { return binding.spec }

func (binding reservationRegressionBinding) Invoke(context.Context, map[string]any, agenttool.Context) (agentprotocol.ToolResult, error) {
	*binding.calls++
	return binding.result, nil
}

type reservationRegressionApprovals struct{}

func (reservationRegressionApprovals) Request(context.Context, string, agentprotocol.ToolCall) (agentruntime.Decision, error) {
	return agentruntime.DecisionDeny, nil
}

type recordingReservationProvider struct {
	adapter  agentprovider.Adapter
	requests []agentprotocol.ModelTurnRequest
}

func (provider *recordingReservationProvider) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
	provider.requests = append(provider.requests, request)
	return provider.adapter.Stream(ctx, request, agentprovider.TurnOptions{})
}

type rejectingReservationProvider struct {
	adapter      agentprovider.Adapter
	record       agentexecution.Record
	requests     []agentprotocol.ModelTurnRequest
	reservations []int
	errors       []error
}

func (provider *rejectingReservationProvider) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
	provider.requests = append(provider.requests, request)
	_, _, reservation, err := prepareEffectiveRequest(provider.record, request, request.Tools)
	provider.reservations = append(provider.reservations, reservation)
	provider.errors = append(provider.errors, err)
	if err != nil {
		return func(yield func(agentprotocol.ProviderEvent, error) bool) {
			yield(agentprotocol.ProviderEvent{}, err)
		}
	}
	// Settle the simulated first turn before the Driver can request the next
	// turn. The first attempt's unknown reservation remains authoritative.
	provider.record.Execution.ReservedTokens = reservation
	provider.record.Execution.KnownUsage = agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	provider.record.Execution.UsageComplete = false
	sequence := provider.adapter.Stream(ctx, request, agentprovider.TurnOptions{})
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		for event, eventErr := range sequence {
			if !yield(event, eventErr) {
				return
			}
		}
	}
}

func TestProvider429UnknownReservationLeavesFourTurnFakeOverBudget(t *testing.T) {
	driver, provider, record, _ := reservationRegressionDriver(t)
	trace, err := driver.Run(t.Context(), agentruntime.NewState(agentruntime.Config{
		RunID: reservationRegressionRunID.String(), ExecutionMode: agentprotocol.ExecutionModeBackground,
		CapabilitySnapshot: record.Execution.Capabilities, Budget: record.Execution.Budget,
	}), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: pointer(reservationRegressionIntent)})
	if err != nil {
		t.Fatal(err)
	}
	if trace.State.Phase != agentprotocol.RuntimePhaseCompleted || len(provider.requests) != 4 {
		t.Fatalf("clean Fake trace phase=%s requests=%d", trace.State.Phase, len(provider.requests))
	}

	type measurement struct {
		rawBytes    int
		reservation int
		available   int
		code        agentprotocol.ErrorCode
	}
	want := []measurement{
		{rawBytes: 5189, reservation: 7749, available: 16000},
		{rawBytes: 5754, reservation: 8314, available: 8236, code: agentprotocol.ErrorCodeValidationFailed},
		{rawBytes: 6931, reservation: 9491, available: 8221, code: agentprotocol.ErrorCodeValidationFailed},
		{rawBytes: 7322, reservation: 9882, available: 8206, code: agentprotocol.ErrorCodeValidationFailed},
	}
	firstReservation := 0
	for index, request := range provider.requests {
		measurementRecord := record
		measurementRecord.Execution.Budget.MaxTokens = 1_000_000
		_, raw, reservation, measureErr := prepareEffectiveRequest(measurementRecord, request, request.Tools)
		if measureErr != nil {
			t.Fatalf("turn %d measurement Prepare() error = %v", index+1, measureErr)
		}
		if index == 0 {
			firstReservation = reservation
		}

		admissionRecord := record
		admissionRecord.Execution.KnownUsage = agentprotocol.Usage{
			InputTokens: 10 * index, OutputTokens: 5 * index, TotalTokens: 15 * index,
		}
		if index > 0 {
			admissionRecord.Execution.ReservedTokens = firstReservation
			admissionRecord.Execution.UsageComplete = false
		}
		available := admissionRecord.Execution.Budget.MaxTokens - admissionRecord.Execution.KnownUsage.TotalTokens - admissionRecord.Execution.ReservedTokens
		_, _, _, admissionErr := prepareEffectiveRequest(admissionRecord, request, request.Tools)
		code := gatewayErrorCode(admissionErr)
		t.Logf("turn=%d raw_bytes=%d reservation=%d available=%d prepare_error_code=%s", index+1, len(raw), reservation, available, code)

		got := measurement{rawBytes: len(raw), reservation: reservation, available: available, code: code}
		if got != want[index] {
			t.Fatalf("turn %d measurement = %#v, want %#v", index+1, got, want[index])
		}
	}
}

func TestDriverClassifiesSecondTurnBudgetRejectionAsProviderUnavailable(t *testing.T) {
	_, _, record, fixture := reservationRegressionDriver(t)
	provider := &rejectingReservationProvider{adapter: fixture, record: record}
	calendarCalls := 0
	driver, err := agentruntime.NewDriver(agentruntime.DriverConfig{
		Provider: provider, Tools: reservationRegressionTools(t, record.Execution.Capabilities, &calendarCalls),
		Approvals: reservationRegressionApprovals{}, ModelProfile: record.Execution.ModelProfile,
		Policy: agenttool.Policy{Allow: []string{"*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := driver.Run(t.Context(), agentruntime.NewState(agentruntime.Config{
		RunID: reservationRegressionRunID.String(), ExecutionMode: agentprotocol.ExecutionModeBackground,
		CapabilitySnapshot: record.Execution.Capabilities, Budget: record.Execution.Budget,
	}), agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeUserMessage, Text: pointer(reservationRegressionIntent)})
	if err != nil {
		t.Fatal(err)
	}

	wantError := &agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "provider stream failed", Retryable: false}
	if trace.State.Phase != agentprotocol.RuntimePhaseFailed || !reflect.DeepEqual(trace.State.Error, wantError) ||
		trace.State.Usage != (agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) || trace.ModelTurns != 2 {
		gotCode := agentprotocol.ErrorCode("")
		if trace.State.Error != nil {
			gotCode = trace.State.Error.Code
		}
		t.Fatalf("Driver phase=%s errorCode=%s usage=%+v modelTurns=%d", trace.State.Phase, gotCode, trace.State.Usage, trace.ModelTurns)
	}
	if len(provider.requests) != 2 || provider.reservations[0] != 7749 || provider.reservations[1] != 0 || provider.errors[0] != nil ||
		gatewayErrorCode(provider.errors[1]) != agentprotocol.ErrorCodeValidationFailed {
		t.Fatalf("Provider requests=%d reservations=%v errors=%v", len(provider.requests), provider.reservations, provider.errors)
	}
	if provider.record.Execution.KnownUsage != (agentprotocol.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}) ||
		provider.record.Execution.ReservedTokens != 7749 || provider.record.Execution.UsageComplete || calendarCalls != 0 {
		t.Fatalf("post-retry knownUsage=%+v reservedTokens=%d usageComplete=%t calendarCalls=%d",
			provider.record.Execution.KnownUsage, provider.record.Execution.ReservedTokens, provider.record.Execution.UsageComplete, calendarCalls)
	}
	lastInput := trace.Inputs[len(trace.Inputs)-1]
	lastEffect := trace.Effects[len(trace.Effects)-1]
	if lastInput.Type != agentprotocol.RuntimeInputTypeRuntimeError || !reflect.DeepEqual(lastInput.Error, wantError) ||
		lastEffect.Type != agentprotocol.RuntimeEffectTypeFailRun || !reflect.DeepEqual(lastEffect.Error, wantError) {
		t.Fatalf("Driver terminal inputType=%s inputErrorCode=%s effectType=%s effectErrorCode=%s",
			lastInput.Type, optionalAgentErrorCode(lastInput.Error), lastEffect.Type, optionalAgentErrorCode(lastEffect.Error))
	}
	t.Logf("Driver phase=%s error_code=%s usage=%+v model_turns=%d effect_types=%v terminal_input_type=%s",
		trace.State.Phase, trace.State.Error.Code, trace.State.Usage, trace.ModelTurns, runtimeEffectTypes(trace.Effects), lastInput.Type)
}

func reservationRegressionDriver(t *testing.T) (*agentruntime.Driver, *recordingReservationProvider, agentexecution.Record, agentprovider.Adapter) {
	t.Helper()
	profile, err := agentskill.ParseBundle(agentassets.CalendarOverviewBundle())
	if err != nil {
		t.Fatal(err)
	}
	from, to := "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z"
	capabilities := agentprotocol.CapabilitySnapshot{
		RuntimeVersion: "2.0.0", ExecutionMode: agentprotocol.ExecutionModeBackground,
		ToolIds: []string{"skill_list", "skill_load", "dayorder.calendar.read"},
		Skills:  []agentprotocol.SkillRef{{Name: profile.Descriptor.Name, Version: profile.Descriptor.Version, Digest: profile.Descriptor.Digest}},
		Scope:   agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
	}
	record := agentexecution.Record{
		Run: model.AgentRun{ID: reservationRegressionRunID},
		Execution: agentexecution.Execution{
			RunID: reservationRegressionRunID, Mode: agentprotocol.ExecutionModeBackground,
			ProtocolVersion: "2.0", RuntimeVersion: "2.0.0", ModelProfile: "readonly-default",
			Capabilities: capabilities,
			Budget: agentprotocol.Budget{
				MaxSteps: 8, MaxTokens: 16000, MaxDurationMs: 120000,
				MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2,
			},
		},
	}
	calendarCalls := 0
	tools := reservationRegressionTools(t, capabilities, &calendarCalls)
	fixture, err := agentprovider.NewFake(agentprovider.FakeConfig{Window: agentprotocol.CalendarReadInput{
		Start: agentprotocol.DateTime(from), End: agentprotocol.DateTime(to),
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingReservationProvider{adapter: fixture}
	driver, err := agentruntime.NewDriver(agentruntime.DriverConfig{
		Provider: provider, Tools: tools, Approvals: reservationRegressionApprovals{},
		ModelProfile: record.Execution.ModelProfile, Policy: agenttool.Policy{Allow: []string{"*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return driver, provider, record, fixture
}

func reservationRegressionTools(t *testing.T, capabilities agentprotocol.CapabilitySnapshot, calendarCalls *int) *agenttool.Registry {
	t.Helper()
	profile, err := agentskill.ParseBundle(agentassets.CalendarOverviewBundle())
	if err != nil {
		t.Fatal(err)
	}
	skills, err := agentskill.NewRegistry([]agentskill.SkillProfile{profile})
	if err != nil {
		t.Fatal(err)
	}
	calendarSpec, err := agentassets.CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	calendarResult := agentprotocol.ToolResult{Ok: true, Data: agentprotocol.ToolResultData{
		"events":  []any{},
		"window":  map[string]any{"start": "2026-09-05T00:00:00Z", "end": "2026-09-06T00:00:00Z"},
		"hasMore": false, "nextCursor": nil,
	}}
	tools := &agenttool.Registry{}
	policy := agenttool.Policy{Allow: []string{"*"}}
	bindings := []agenttool.Binding{reservationRegressionBinding{spec: calendarSpec, result: calendarResult, calls: calendarCalls}}
	bindings = append(bindings, agentskill.MetaBindings(skills, capabilities, tools, policy)...)
	for _, binding := range bindings {
		if err = tools.Register(binding); err != nil {
			t.Fatal(err)
		}
	}
	return tools
}

func gatewayErrorCode(err error) agentprotocol.ErrorCode {
	if err == nil {
		return ""
	}
	var failure *agentexecution.Error
	if errors.As(err, &failure) && failure != nil {
		return failure.Agent.Code
	}
	return ""
}

func optionalAgentErrorCode(failure *agentprotocol.AgentError) agentprotocol.ErrorCode {
	if failure == nil {
		return ""
	}
	return failure.Code
}

func runtimeEffectTypes(effects []agentprotocol.RuntimeEffect) []agentprotocol.RuntimeEffectType {
	result := make([]agentprotocol.RuntimeEffectType, len(effects))
	for index, effect := range effects {
		result[index] = effect.Type
	}
	return result
}

func pointer[T any](value T) *T { return &value }
