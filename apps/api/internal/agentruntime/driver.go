package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strings"
	"time"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agenttool"
)

// Decision is the runtime-checked response from an ApprovalBroker.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

// ApprovalBroker obtains an approval decision for one Tool call.
type ApprovalBroker interface {
	Request(context.Context, string, agentprotocol.ToolCall) (Decision, error)
}

// DriverConfig supplies server-owned dependencies and policy to a Driver.
type DriverConfig struct {
	Provider     agentprovider.StreamProvider
	Tools        *agenttool.Registry
	Approvals    ApprovalBroker
	ModelProfile string
	Policy       agenttool.Policy
	Deadline     time.Time
}

// Driver executes reducer-emitted effects through server-owned boundaries.
type Driver struct {
	provider     agentprovider.StreamProvider
	tools        *agenttool.Registry
	approvals    ApprovalBroker
	modelProfile string
	policy       agenttool.Policy
	deadline     time.Time
}

// Trace records reducer-normalized inputs and emitted effects.
type Trace struct {
	State      agentprotocol.RuntimeState
	Inputs     []agentprotocol.RuntimeInput
	Effects    []agentprotocol.RuntimeEffect
	ModelTurns int
}

// NewDriver constructs an effect Driver from server-owned configuration.
func NewDriver(config DriverConfig) (*Driver, error) {
	profile := strings.TrimSpace(config.ModelProfile)
	if profile == "" {
		return nil, fmt.Errorf("model Profile is required")
	}
	if urlLikeProfile(profile) {
		return nil, fmt.Errorf("model Profile must not be URL-like")
	}
	if nilInterface(config.Provider) {
		return nil, fmt.Errorf("StreamProvider is required")
	}
	if config.Tools == nil {
		return nil, fmt.Errorf("Tool Registry is required")
	}
	if nilInterface(config.Approvals) {
		return nil, fmt.Errorf("ApprovalBroker is required")
	}
	return &Driver{
		provider: config.Provider, tools: config.Tools, approvals: config.Approvals,
		modelProfile: profile,
		deadline:     config.Deadline,
		policy: agenttool.Policy{
			Allow: slices.Clone(config.Policy.Allow), Deny: slices.Clone(config.Policy.Deny),
			ApprovalFor: slices.Clone(config.Policy.ApprovalFor),
		},
	}, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func urlLikeProfile(value string) bool {
	if strings.HasPrefix(value, "//") {
		return true
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 {
		return false
	}
	for index := 0; index < colon; index++ {
		character := value[index]
		if index == 0 {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')) {
				return false
			}
			continue
		}
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '+' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

type streamItem struct {
	event   agentprotocol.ProviderEvent
	err     error
	invalid bool
	ack     chan bool
}

type asyncOutcome[T any] struct {
	value T
	err   error
}

type runDriver struct {
	driver  *Driver
	caller  context.Context
	ctx     context.Context
	state   agentprotocol.RuntimeState
	inputs  []agentprotocol.RuntimeInput
	effects []agentprotocol.RuntimeEffect
	pending []agentprotocol.RuntimeEffect
	turns   int
}

// Run advances initialState to a terminal state by processing Effects FIFO.
func (d *Driver) Run(caller context.Context, initialState agentprotocol.RuntimeState, initialInput agentprotocol.RuntimeInput) (Trace, error) {
	state, err := clone(initialState)
	if err != nil {
		return Trace{}, err
	}
	deadline := time.Now().Add(time.Duration(state.Budget.MaxDurationMs) * time.Millisecond)
	if !d.deadline.IsZero() && d.deadline.Before(deadline) {
		deadline = d.deadline
	}
	ctx, cancel := context.WithDeadline(caller, deadline)
	defer cancel()
	run := &runDriver{driver: d, caller: caller, ctx: ctx, state: state, inputs: []agentprotocol.RuntimeInput{}, effects: []agentprotocol.RuntimeEffect{}, pending: []agentprotocol.RuntimeEffect{}}

	if stopped, err := run.feedAbort(); stopped || err != nil {
		return run.trace(err)
	}
	if run.state.ExecutionMode != agentprotocol.ExecutionModeBackground {
		failure := agentError(agentprotocol.ErrorCodeProtocolIncompatible, "background Driver requires background execution mode")
		if err := run.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure}); err != nil {
			return Trace{}, err
		}
		return run.trace(nil)
	}
	switch initialInput.Type {
	case agentprotocol.RuntimeInputTypeUserMessage, agentprotocol.RuntimeInputTypeCancel, agentprotocol.RuntimeInputTypeRuntimeError:
	default:
		failure := agentError(agentprotocol.ErrorCodeProtocolIncompatible, "Driver initial input must be user_message, cancel, or runtime_error")
		if err := run.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure}); err != nil {
			return Trace{}, err
		}
		return run.trace(nil)
	}
	if message := taggedPayloadError(initialInput); message != "" {
		failure := agentError(agentprotocol.ErrorCodeProtocolIncompatible, message)
		if err := run.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure}); err != nil {
			return Trace{}, err
		}
		return run.trace(nil)
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeInput, initialInput); err != nil {
		failure := agentError(agentprotocol.ErrorCodeValidationFailed, "invalid RuntimeInput")
		if err := run.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure}); err != nil {
			return Trace{}, err
		}
		return run.trace(nil)
	}
	if err := run.feed(initialInput); err != nil {
		return Trace{}, err
	}

	for !terminal(run.state.Phase) {
		if stopped, err := run.feedAbort(); stopped || err != nil {
			return run.trace(err)
		}
		if len(run.pending) == 0 {
			return Trace{}, fmt.Errorf("runtime stalled without an effect")
		}
		effect := run.pending[0]
		run.pending = run.pending[1:]
		if err := run.process(effect); err != nil {
			return Trace{}, err
		}
	}
	return run.trace(nil)
}

func (r *runDriver) trace(runErr error) (Trace, error) {
	if runErr != nil {
		return Trace{}, runErr
	}
	return clone(Trace{State: r.state, Inputs: r.inputs, Effects: r.effects, ModelTurns: r.turns})
}

func (r *runDriver) feed(candidate agentprotocol.RuntimeInput) error {
	input, err := clone(candidate)
	if err != nil {
		return err
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeInput, input); err != nil {
		return err
	}
	transition, err := Advance(r.state, input)
	if err != nil {
		return err
	}
	r.state, err = clone(transition.State)
	if err != nil {
		return err
	}
	inputRecord, err := clone(input)
	if err != nil {
		return err
	}
	r.inputs = append(r.inputs, inputRecord)
	for _, effect := range transition.Effects {
		if err := agentprotocol.Validate(agentprotocol.DefinitionRuntimeEffect, effect); err != nil {
			return err
		}
		recorded, err := clone(effect)
		if err != nil {
			return err
		}
		queued, err := clone(effect)
		if err != nil {
			return err
		}
		r.effects = append(r.effects, recorded)
		r.pending = append(r.pending, queued)
	}
	return nil
}

func (r *runDriver) feedAbort() (bool, error) {
	if terminal(r.state.Phase) {
		return true, nil
	}
	if r.caller.Err() != nil {
		return true, r.feed(StopInput(context.Cause(r.caller)))
	}
	if r.ctx.Err() != nil {
		failure := agentError(agentprotocol.ErrorCodeTimeout, "runtime deadline exceeded")
		return true, r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
	}
	return false, nil
}

func (r *runDriver) process(effect agentprotocol.RuntimeEffect) error {
	switch effect.Type {
	case agentprotocol.RuntimeEffectTypeRequestModelTurn:
		return r.requestModelTurn(effect)
	case agentprotocol.RuntimeEffectTypeResolveTool:
		return r.resolveTool(*effect.ToolCall)
	case agentprotocol.RuntimeEffectTypeExecuteTool:
		return r.executeTool(*effect.ToolCall)
	case agentprotocol.RuntimeEffectTypeRequestApproval:
		return r.requestApproval(effect)
	case agentprotocol.RuntimeEffectTypeEmitText,
		agentprotocol.RuntimeEffectTypeCompleteRun,
		agentprotocol.RuntimeEffectTypeFailRun,
		agentprotocol.RuntimeEffectTypeCancelRun:
		return nil
	default:
		return fmt.Errorf("unsupported RuntimeEffect %q", effect.Type)
	}
}

func (r *runDriver) requestModelTurn(effect agentprotocol.RuntimeEffect) error {
	r.turns++
	ids := agenttool.EffectiveToolIDs(r.state.CapabilitySnapshot.ToolIds, r.driver.tools, r.state.CapabilitySnapshot, r.driver.policy)
	tools := make([]agentprotocol.ToolSpec, 0, len(ids))
	for _, id := range ids {
		binding, _ := r.driver.tools.Resolve(id)
		tools = append(tools, binding.Spec())
	}
	messages, err := clone(r.state.Messages)
	if err != nil {
		return err
	}
	request := agentprotocol.ModelTurnRequest{
		ProtocolVersion: "2.0", RunID: r.state.RunID, TurnID: *effect.TurnID,
		ModelProfile: r.driver.modelProfile, Messages: messages, Tools: tools,
	}
	if err := agentprotocol.Validate(agentprotocol.DefinitionModelTurnRequest, request); err != nil {
		return err
	}

	sequence, providerErr, aborted := awaitCall(r.ctx, func() (iter.Seq2[agentprotocol.ProviderEvent, error], error) {
		return openProviderStream(r.driver.provider, r.ctx, request)
	})
	if aborted {
		_, err := r.feedAbort()
		return err
	}
	if providerErr != nil {
		failure := agentError(agentprotocol.ErrorCodeProviderUnavailable, "provider stream failed")
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
	}
	items := streamSequence(r.ctx, sequence)
	for {
		select {
		case <-r.ctx.Done():
			_, err := r.feedAbort()
			return err
		case item, ok := <-items:
			if !ok {
				if stopped, err := r.feedAbort(); stopped || err != nil {
					return err
				}
				if r.state.Phase == agentprotocol.RuntimePhaseModelPending || r.state.Phase == agentprotocol.RuntimePhaseModelStreaming {
					failure := agentError(agentprotocol.ErrorCodeProviderUnavailable, "provider stream ended before turn completion")
					return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
				}
				return nil
			}
			if stopped, err := r.feedAbort(); stopped || err != nil {
				item.ack <- false
				return err
			}
			if item.err != nil {
				item.ack <- false
				failure := agentError(agentprotocol.ErrorCodeProviderUnavailable, "provider stream failed")
				return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
			}
			if item.invalid {
				item.ack <- false
				failure := agentError(agentprotocol.ErrorCodeValidationFailed, "invalid ProviderEvent")
				return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
			}
			event, err := clone(item.event)
			if err != nil {
				item.ack <- false
				failure := agentError(agentprotocol.ErrorCodeValidationFailed, "invalid ProviderEvent")
				return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
			}
			if message := providerEventPayloadError(event); message != "" {
				item.ack <- false
				failure := agentError(agentprotocol.ErrorCodeProtocolIncompatible, message)
				return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
			}
			if err := agentprotocol.Validate(agentprotocol.DefinitionProviderEvent, event); err != nil {
				item.ack <- false
				failure := agentError(agentprotocol.ErrorCodeValidationFailed, "invalid ProviderEvent")
				return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
			}
			if err := r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeProviderEvent, ProviderEvent: &event}); err != nil {
				item.ack <- false
				return err
			}
			if r.state.Phase != agentprotocol.RuntimePhaseModelPending && r.state.Phase != agentprotocol.RuntimePhaseModelStreaming {
				item.ack <- false
				return nil
			}
			item.ack <- true
		}
	}
}

func openProviderStream(provider agentprovider.StreamProvider, ctx context.Context, request agentprotocol.ModelTurnRequest) (sequence iter.Seq2[agentprotocol.ProviderEvent, error], err error) {
	defer func() {
		if recover() != nil {
			sequence = nil
			err = fmt.Errorf("Provider stream construction panicked")
		}
	}()
	sequence = provider.Stream(ctx, request)
	if sequence == nil {
		return nil, fmt.Errorf("Provider returned a nil stream")
	}
	return sequence, nil
}

func streamSequence(ctx context.Context, sequence iter.Seq2[agentprotocol.ProviderEvent, error]) <-chan streamItem {
	items := make(chan streamItem)
	go func() {
		defer func() {
			if recover() != nil {
				item := streamItem{err: fmt.Errorf("Provider stream panicked"), ack: make(chan bool, 1)}
				select {
				case items <- item:
				case <-ctx.Done():
				}
			}
			close(items)
		}()
		sequence(func(event agentprotocol.ProviderEvent, err error) bool {
			item := streamItem{event: event, err: err, ack: make(chan bool, 1)}
			if err == nil {
				copied, copyErr := clone(event)
				if copyErr != nil {
					item.event = agentprotocol.ProviderEvent{}
					item.invalid = true
				} else {
					item.event = copied
				}
			}
			select {
			case items <- item:
			case <-ctx.Done():
				return false
			}
			select {
			case keepGoing := <-item.ack:
				return keepGoing
			case <-ctx.Done():
				return false
			}
		})
	}()
	return items
}

func (r *runDriver) resolveTool(call agentprotocol.ToolCall) error {
	binding, found := r.driver.tools.Resolve(call.Name)
	if !found {
		failure := agentError(agentprotocol.ErrorCodeCapabilityUnavailable, call.Name+" is unavailable")
		resolution := agentprotocol.ToolResolution{CallID: call.ID, Decision: agentprotocol.ToolResolutionDecisionUnavailable, Error: &failure}
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &resolution})
	}
	ids := agenttool.EffectiveToolIDs(r.state.CapabilitySnapshot.ToolIds, r.driver.tools, r.state.CapabilitySnapshot, r.driver.policy)
	if !slices.Contains(ids, call.Name) {
		failure := agentError(agentprotocol.ErrorCodePermissionDenied, "tool denied: "+call.Name)
		resolution := agentprotocol.ToolResolution{CallID: call.ID, Decision: agentprotocol.ToolResolutionDecisionDenied, Error: &failure}
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &resolution})
	}
	spec := binding.Spec()
	if spec.ApprovalPolicy == agentprotocol.ToolSpecApprovalPolicyAlways ||
		(spec.ApprovalPolicy == agentprotocol.ToolSpecApprovalPolicyIfNeeded && slices.Contains(r.driver.policy.ApprovalFor, spec.SideEffect)) {
		approvalID := "approval-" + call.ID
		resolution := agentprotocol.ToolResolution{CallID: call.ID, Decision: agentprotocol.ToolResolutionDecisionApproval, ApprovalID: &approvalID}
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &resolution})
	}
	resolution := agentprotocol.ToolResolution{CallID: call.ID, Decision: agentprotocol.ToolResolutionDecisionExecute}
	return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResolution, ToolResolution: &resolution})
}

func (r *runDriver) executeTool(call agentprotocol.ToolCall) error {
	binding, found := r.driver.tools.Resolve(call.Name)
	if !found {
		return fmt.Errorf("resolved Tool %q disappeared", call.Name)
	}
	spec := binding.Spec()
	if !validDynamicSchema(spec.InputSchema, call.Input) {
		result := resultFailure(agentprotocol.ErrorCodeValidationFailed, "tool input schema validation failed")
		resultInput := agentprotocol.RuntimeInputToolResult{CallID: call.ID, Result: result}
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &resultInput})
	}
	input, err := clone(map[string]any(call.Input))
	if err != nil {
		return err
	}
	toolContext := agenttool.Context{RunID: r.state.RunID, CallID: call.ID}
	toolCtx, cancelTool := context.WithTimeout(r.ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
	var result agentprotocol.ToolResult
	var invokeErr error
	if toolCtx.Err() == nil {
		result, invokeErr = invokeToolBinding(binding, toolCtx, input, toolContext)
	}
	toolErr := toolCtx.Err()
	cancelTool()
	if toolErr != nil {
		if stopped, abortErr := r.feedAbort(); stopped || abortErr != nil {
			return abortErr
		}
		if errors.Is(toolErr, context.DeadlineExceeded) {
			result = resultFailure(agentprotocol.ErrorCodeTimeout, "tool deadline exceeded")
			resultInput := agentprotocol.RuntimeInputToolResult{CallID: call.ID, Result: result}
			return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &resultInput})
		}
		return fmt.Errorf("Tool invocation aborted without a Run or Tool deadline")
	}
	if invokeErr != nil {
		result = resultFailure(agentprotocol.ErrorCodeToolFailed, "tool invocation failed")
		resultInput := agentprotocol.RuntimeInputToolResult{CallID: call.ID, Result: result}
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &resultInput})
	}
	canonical, resultBytes, ok := canonicalizeToolResult(result)
	if !ok {
		result = resultFailure(agentprotocol.ErrorCodeValidationFailed, "tool result is not JSON serializable")
	} else {
		result = canonical
	}
	validResult := ok
	if err := agentprotocol.Validate(agentprotocol.DefinitionToolResult, result); err != nil {
		result = resultFailure(agentprotocol.ErrorCodeValidationFailed, "invalid ToolResult")
		validResult = false
	}
	if validResult && result.Data != nil && !validDynamicSchema(spec.OutputSchema, result.Data) {
		result = resultFailure(agentprotocol.ErrorCodeValidationFailed, "tool output schema validation failed")
	} else if validResult && len(resultBytes) > spec.ResultMaxBytes {
		result = resultFailure(agentprotocol.ErrorCodeValidationFailed, "tool result exceeds resultMaxBytes")
	}
	resultInput := agentprotocol.RuntimeInputToolResult{CallID: call.ID, Result: result}
	return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeToolResult, ToolResult: &resultInput})
}

func invokeToolBinding(binding agenttool.Binding, ctx context.Context, input map[string]any, toolContext agenttool.Context) (result agentprotocol.ToolResult, err error) {
	defer func() {
		if recover() != nil {
			result = agentprotocol.ToolResult{}
			err = fmt.Errorf("adapter panicked")
		}
	}()
	return binding.Invoke(ctx, input, toolContext)
}

func canonicalizeToolResult(value agentprotocol.ToolResult) (canonical agentprotocol.ToolResult, resultBytes []byte, ok bool) {
	defer func() {
		if recover() != nil {
			canonical = agentprotocol.ToolResult{}
			resultBytes = nil
			ok = false
		}
	}()
	raw, err := json.Marshal(value)
	if err != nil {
		return agentprotocol.ToolResult{}, nil, false
	}
	resultBytes, err = canonicalJSONBytes(raw)
	if err != nil {
		return agentprotocol.ToolResult{}, nil, false
	}
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return agentprotocol.ToolResult{}, nil, false
	}
	return canonical, slices.Clone(resultBytes), true
}

func awaitCall[T any](ctx context.Context, start func() (T, error)) (T, error, bool) {
	var zero T
	if ctx.Err() != nil {
		return zero, nil, true
	}
	outcomes := make(chan asyncOutcome[T])
	go func() {
		if ctx.Err() != nil {
			return
		}
		var outcome asyncOutcome[T]
		func() {
			defer func() {
				if recover() != nil {
					outcome.err = fmt.Errorf("adapter panicked")
				}
			}()
			outcome.value, outcome.err = start()
		}()
		select {
		case outcomes <- outcome:
		case <-ctx.Done():
		}
	}()
	select {
	case <-ctx.Done():
		return zero, nil, true
	case outcome := <-outcomes:
		if ctx.Err() != nil {
			return zero, nil, true
		}
		return outcome.value, outcome.err, false
	}
}

func (r *runDriver) requestApproval(effect agentprotocol.RuntimeEffect) error {
	call, err := clone(*effect.ToolCall)
	if err != nil {
		return err
	}
	approvalID := *effect.ApprovalID
	decision, err, aborted := awaitCall(r.ctx, func() (Decision, error) {
		return r.driver.approvals.Request(r.ctx, approvalID, call)
	})
	if aborted {
		_, err := r.feedAbort()
		return err
	}
	if err != nil {
		failure := agentError(agentprotocol.ErrorCodeInternalError, "approval request failed")
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
	}
	if decision != DecisionAllow && decision != DecisionDeny {
		failure := agentError(agentprotocol.ErrorCodeProtocolIncompatible, "invalid ApprovalBroker decision")
		return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeRuntimeError, Error: &failure})
	}
	response := agentprotocol.RuntimeInputApprovalResponse{ApprovalID: approvalID, Decision: agentprotocol.RuntimeInputApprovalResponseDecision(decision)}
	return r.feed(agentprotocol.RuntimeInput{Type: agentprotocol.RuntimeInputTypeApprovalResponse, ApprovalResponse: &response})
}

func agentError(code agentprotocol.ErrorCode, message string) agentprotocol.AgentError {
	return agentprotocol.AgentError{Code: code, Message: message, Retryable: false}
}

func resultFailure(code agentprotocol.ErrorCode, message string) agentprotocol.ToolResult {
	return agentprotocol.ToolResult{Ok: false, Error: ptrError(agentError(code, message))}
}

func validDynamicSchema(schema map[string]any, value any) bool {
	const schemaID = "urn:dayorder:agent:dynamic-schema"
	schemaRaw, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	var schemaDocument any
	if err := json.Unmarshal(schemaRaw, &schemaDocument); err != nil {
		return false
	}
	compiler, err := agentprotocol.NewSchemaCompiler()
	if err != nil {
		return false
	}
	if err := compiler.AddResource(schemaID, schemaDocument); err != nil {
		return false
	}
	compiled, err := compiler.Compile(schemaID)
	if err != nil {
		return false
	}
	valueRaw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var document any
	if err := json.Unmarshal(valueRaw, &document); err != nil {
		return false
	}
	return compiled.Validate(document) == nil
}
