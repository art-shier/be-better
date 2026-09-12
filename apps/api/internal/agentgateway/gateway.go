// Package agentgateway enforces server-owned Provider policy and accounting.
package agentgateway

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"strings"
	"sync"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type Profile struct {
	ID      string
	Model   string
	Adapter agentprovider.Adapter
}

type Config struct {
	Runs     *service.AgentReadonlyService
	Profiles []Profile
	Tools    []agentprotocol.ToolSpec
	Observer agentexecution.Observer
	Logger   *slog.Logger
}

type Gateway struct {
	runs     *service.AgentReadonlyService
	profiles map[string]Profile
	tools    []agentprotocol.ToolSpec
	registry *turnRegistry
	observer agentexecution.Observer
	logger   *slog.Logger
}

type Turn struct {
	gateway     *Gateway
	actor       agentexecution.Actor
	runID       uuid.UUID
	request     agentprotocol.ModelTurnRequest
	profile     Profile
	reservation int
	deadline    time.Time

	ctx            context.Context
	cancelCtx      context.CancelCauseFunc
	cancelDeadline context.CancelFunc
	done           chan struct{}
	consumerDone   chan struct{}
	terminalReady  chan terminalRequest
	finalOnce      sync.Once
	opGate         chan struct{}
	mu             sync.Mutex
	handle         agentexecution.Operation
	consumed       bool
	finalErr       error
	success        bool
	explicitCancel *agentprotocol.AgentError
	cleanup        cleanupTiming
	cancelStarted  time.Time
}

type cleanupTiming struct {
	endDeadline       time.Time
	authorizeDeadline time.Time
	overallDeadline   time.Time
}

type terminalCallbacks struct {
	end       func(context.Context) error
	authorize func(context.Context) (bool, error)
	fail      func(context.Context, agentprotocol.AgentError) error
}

type terminalOutcome struct {
	success   bool
	persisted bool
	failure   agentprotocol.AgentError
}

type terminalRequest struct {
	success        bool
	usage          agentprotocol.Usage
	code           agentprotocol.ErrorCode
	alreadySettled bool
}

func finalizeTerminal(timing cleanupTiming, primary func() agentprotocol.AgentError, callbacks terminalCallbacks) terminalOutcome {
	var endErr error
	if callbacks.end != nil {
		endCtx, cancelEnd := context.WithDeadline(context.Background(), timing.endDeadline)
		endErr = callbacks.end(endCtx)
		cancelEnd()
	}

	var authorizationErr error
	authorized := false
	authorizationAttempted := false
	if endErr == nil && callbacks.authorize != nil {
		authorizationAttempted = true
		authorizeCtx, cancelAuthorize := context.WithDeadline(context.Background(), timing.authorizeDeadline)
		authorized, authorizationErr = callbacks.authorize(authorizeCtx)
		cancelAuthorize()
		if authorizationErr == nil && authorized {
			return terminalOutcome{success: true}
		}
	}

	failure := primary()
	failure.Retryable = false
	failure.Details = copyErrorDetails(failure.Details)
	if endErr != nil {
		failure.Details["accounting"] = "incomplete"
	}
	if authorizationAttempted && (authorizationErr != nil || !authorized) {
		failure.Details["authorization"] = "failed"
	}
	failCtx, cancelFail := context.WithDeadline(context.Background(), timing.overallDeadline)
	failErr := callbacks.fail(failCtx, failure)
	cancelFail()
	if failErr != nil {
		failure.Details["persistence"] = "failed"
		return terminalOutcome{failure: failure}
	}
	return terminalOutcome{persisted: true, failure: failure}
}

func copyErrorDetails(details agentprotocol.AgentErrorDetails) agentprotocol.AgentErrorDetails {
	copied := make(agentprotocol.AgentErrorDetails, len(details)+3)
	for key, value := range details {
		copied[key] = value
	}
	return copied
}

func New(config Config) (*Gateway, error) {
	if config.Runs == nil || len(config.Profiles) == 0 || len(config.Tools) == 0 {
		return nil, errors.New("gateway runs, profiles, and tools are required")
	}
	profiles := make(map[string]Profile, len(config.Profiles))
	for _, profile := range config.Profiles {
		if !validProfile(profile) {
			return nil, errors.New("invalid gateway profile")
		}
		if _, duplicate := profiles[profile.ID]; duplicate {
			return nil, errors.New("duplicate gateway profile")
		}
		profiles[profile.ID] = profile
	}
	tools, _, err := normalizedTools(config.Tools)
	if err != nil {
		return nil, errors.New("invalid gateway tools")
	}
	if len(tools) != 3 || tools[0].ID != "dayorder.calendar.read" || tools[1].ID != "skill_list" || tools[2].ID != "skill_load" {
		return nil, errors.New("gateway requires the readonly builtin tools")
	}
	for _, tool := range tools {
		if tool.SideEffect != agentprotocol.SideEffectRead || tool.ApprovalPolicy != agentprotocol.ToolSpecApprovalPolicyNever || !tool.Idempotent {
			return nil, errors.New("gateway tool policy is not readonly")
		}
	}
	return &Gateway{
		runs: config.Runs, profiles: profiles, tools: tools, registry: newTurnRegistry(),
		observer: config.Observer, logger: config.Logger,
	}, nil
}

func (gateway *Gateway) ForRun(actor agentexecution.Actor, runID uuid.UUID) agentprovider.StreamProvider {
	return runProvider{gateway: gateway, actor: actor, runID: runID}
}

func (gateway *Gateway) Prepare(ctx context.Context, actor agentexecution.Actor, request agentprotocol.ModelTurnRequest) (*Turn, error) {
	if gateway == nil || gateway.runs == nil {
		return nil, gatewayFailure(agentprotocol.ErrorCodeInternalError, "gateway is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runID, err := uuid.Parse(request.RunID)
	if err != nil || runID == uuid.Nil {
		return nil, gatewayFailure(agentprotocol.ErrorCodeValidationFailed, "model turn run ID is invalid")
	}
	record, err := gateway.runs.Authorize(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	profile, exists := gateway.profiles[record.Execution.ModelProfile]
	if !exists || request.ModelProfile != profile.ID || request.ProtocolVersion != protocolVersion || record.Execution.ProtocolVersion != protocolVersion || record.Execution.RuntimeVersion != runtimeVersion {
		return nil, gatewayFailure(agentprotocol.ErrorCodeProtocolIncompatible, "model turn profile or protocol does not match the run")
	}
	tools, err := gateway.effectiveTools(record, request.Tools)
	if err != nil {
		return nil, err
	}
	effective, payload, reservation, err := prepareEffectiveRequest(record, request, tools)
	if err != nil {
		return nil, err
	}
	if request.TurnID == "" || len(request.TurnID) > 240 || request.TurnID != strings.TrimSpace(request.TurnID) {
		return nil, gatewayFailure(agentprotocol.ErrorCodeValidationFailed, "model turn ID is invalid")
	}
	if !gateway.registry.reserve(runID) {
		return nil, gatewayFailure(agentprotocol.ErrorCodeVersionConflict, "a model turn is already active")
	}
	handle, err := gateway.runs.BeginOperation(ctx, actor, runID, "provider_turn", request.TurnID, payload, reservation)
	if err != nil {
		gateway.registry.release(runID, nil)
		return nil, err
	}
	deadline := record.Execution.Deadline
	if provider := time.Now().Add(providerTurnLimit); provider.Before(deadline) {
		deadline = provider
	}
	causeCtx, cancel := context.WithCancelCause(ctx)
	turnCtx, cancelDeadline := context.WithDeadlineCause(causeCtx, deadline, context.DeadlineExceeded)
	turn := &Turn{
		gateway: gateway, actor: actor, runID: runID, request: effective, profile: profile,
		reservation: reservation, deadline: deadline, ctx: turnCtx, cancelCtx: cancel,
		done: make(chan struct{}), consumerDone: make(chan struct{}), terminalReady: make(chan terminalRequest, 1), opGate: make(chan struct{}, 1),
		handle: handle, cancelDeadline: cancelDeadline,
	}
	turn.opGate <- struct{}{}
	if err = gateway.registry.activate(runID, turn); err != nil {
		turn.cancel(err)
		return nil, gatewayFailure(agentprotocol.ErrorCodeCancelled, "model turn was cancelled")
	}
	go turn.watchCancellation()
	return turn, nil
}

func (gateway *Gateway) Cancel(runID uuid.UUID, cause error) {
	if gateway == nil || gateway.registry == nil || runID == uuid.Nil {
		return
	}
	if cause == nil {
		cause = context.Canceled
	}
	gateway.registry.cancel(runID, cause)
}

func (turn *Turn) Events(ctx context.Context) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		turn.mu.Lock()
		if turn.consumed {
			turn.mu.Unlock()
			yield(agentprotocol.ProviderEvent{}, gatewayFailure(agentprotocol.ErrorCodeVersionConflict, "model turn events are single-use"))
			return
		}
		turn.consumed = true
		turn.mu.Unlock()
		defer close(turn.consumerDone)

		if ctx != nil {
			if ctx.Err() != nil {
				turn.cancel(ctx.Err())
				turn.finishFailure(turn.contextCode(), agentprotocol.Usage{}, false)
				turn.publishFinalError(yield)
				return
			}
			go func() {
				select {
				case <-ctx.Done():
					turn.cancel(ctx.Err())
				case <-turn.done:
				}
			}()
		}
		turn.consume(yield)
	}
}

func (turn *Turn) consume(yield func(agentprotocol.ProviderEvent, error) bool) {
	select {
	case <-turn.done:
		turn.publishFinalError(yield)
		return
	default:
	}
	if turn.ctx.Err() != nil {
		turn.finishFailure(turn.contextCode(), agentprotocol.Usage{}, false)
		turn.publishFinalError(yield)
		return
	}
	published := false
	attempt := 1
	for {
		attemptStartedAt := time.Now()
		terminal := false
		var completed agentprotocol.ProviderEvent
		var streamErr error
		for event, err := range turn.profile.Adapter.Stream(turn.ctx, turn.request, agentprovider.TurnOptions{Model: turn.profile.Model, MaxOutputTokens: maximumOutputTokens}) {
			if err != nil {
				streamErr = err
				break
			}
			if validateErr := agentprotocol.Validate(agentprotocol.DefinitionProviderEvent, event); validateErr != nil || terminal {
				streamErr = &agentprovider.ProviderError{Code: agentprotocol.ErrorCodeProtocolIncompatible}
				break
			}
			if event.Type == agentprotocol.ProviderEventTypeError {
				streamErr = &agentprovider.ProviderError{Code: event.Error.Code, Retryable: event.Error.Retryable}
				break
			}
			if event.Type == agentprotocol.ProviderEventTypeCompleted {
				terminal = true
				completed = event
				break
			}
			published = true
			if !yield(event, nil) {
				turn.cancel(context.Canceled)
				break
			}
		}
		attemptExitedAt := time.Now()
		if terminal {
			if completed.Usage == nil || !validUsage(*completed.Usage) || !turn.finishSuccess(*completed.Usage) {
				code := turn.contextCode()
				usage, complete := agentprotocol.Usage{}, false
				if completed.Usage != nil && validUsage(*completed.Usage) {
					usage, complete = *completed.Usage, true
				}
				turn.observeProviderAttempt(providerOutcome(code), code, usage, complete, attempt, attemptStartedAt, attemptExitedAt)
				turn.publishFinalError(yield)
				return
			}
			turn.observeProviderAttempt("completed", "", *completed.Usage, true, attempt, attemptStartedAt, attemptExitedAt)
			published = true
			yield(completed, nil)
			return
		}
		if streamErr == nil {
			streamErr = &agentprovider.ProviderError{Code: agentprotocol.ErrorCodeProtocolIncompatible}
		}
		providerErr, code, usage := providerFailure(streamErr)
		usageComplete := providerUsageComplete(streamErr)
		if turn.ctx.Err() != nil {
			providerErr = nil
			code = turn.contextCode()
		}
		remaining := time.Until(turn.deadline)
		retry := allowRetry(providerErr, published, attempt, remaining) && providerErr.RetryAfter <= maximumRetryAfter
		if !retry {
			turn.finishFailure(code, usage, false)
			turn.observeProviderAttempt(providerOutcome(code), code, usage, usageComplete, attempt, attemptStartedAt, attemptExitedAt)
			turn.publishFinalError(yield)
			return
		}
		if !turn.settleAttempt(usage, false, string(code)) {
			if turn.ctx.Err() != nil {
				code = turn.contextCode()
			}
			turn.finishFailure(code, usage, false)
			turn.observeProviderAttempt(providerOutcome(code), code, usage, usageComplete, attempt, attemptStartedAt, attemptExitedAt)
			turn.publishFinalError(yield)
			return
		}
		turn.observeProviderAttempt("failed", code, usage, usageComplete, attempt, attemptStartedAt, attemptExitedAt)
		backoff := providerErr.RetryAfter
		if backoff <= 0 {
			backoff = defaultRetryBackoff
		}
		if !turn.wait(backoff) || !turn.retry() {
			turn.finishFailure(turn.contextCode(), agentprotocol.Usage{}, true)
			turn.publishFinalError(yield)
			return
		}
		attempt++
	}
}

func (turn *Turn) settleAttempt(usage agentprotocol.Usage, complete bool, code string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), settlementLimit)
	defer cancel()
	return turn.withOperation(ctx, func() error {
		return turn.gateway.runs.EndOperation(ctx, turn.actor, turn.handle, usage, complete, code)
	}) == nil
}

func (turn *Turn) retry() bool {
	ctx, cancel := context.WithTimeout(context.Background(), settlementLimit)
	defer cancel()
	return turn.withOperation(ctx, func() error {
		select {
		case <-turn.done:
			return context.Canceled
		default:
		}
		if turn.ctx.Err() != nil {
			return turn.ctx.Err()
		}
		handle, err := turn.gateway.runs.RetryOperation(ctx, turn.actor, turn.handle, turn.reservation)
		if err == nil {
			turn.handle = handle
		}
		return err
	}) == nil
}

func (turn *Turn) wait(duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-turn.ctx.Done():
		return false
	case <-turn.done:
		return false
	}
}

func (turn *Turn) finishSuccess(usage agentprotocol.Usage) bool {
	if turn.handoffTerminal(terminalRequest{success: true, usage: usage}) {
		return false
	}
	return turn.finalizeSuccess(usage)
}

func (turn *Turn) finalizeSuccess(usage agentprotocol.Usage) bool {
	turn.finalOnce.Do(func() {
		outcome := finalizeTerminal(turn.terminalTiming(), func() agentprotocol.AgentError {
			return turn.failureFor(turn.contextCode())
		}, terminalCallbacks{
			end: func(ctx context.Context) error {
				return turn.withOperation(ctx, func() error {
					return turn.gateway.runs.EndOperation(ctx, turn.actor, turn.handle, usage, true, "")
				})
			},
			authorize: func(ctx context.Context) (bool, error) {
				if turn.ctx.Err() != nil {
					return false, nil
				}
				record, err := turn.gateway.runs.Authorize(ctx, turn.actor, turn.runID)
				if err != nil {
					return false, err
				}
				authorized := record.Execution.KnownUsage.TotalTokens <= record.Execution.Budget.MaxTokens && record.Execution.ReservedTokens <= record.Execution.Budget.MaxTokens-record.Execution.KnownUsage.TotalTokens && turn.ctx.Err() == nil
				return authorized, nil
			},
			fail: func(ctx context.Context, failure agentprotocol.AgentError) error {
				return turn.gateway.runs.Fail(ctx, turn.actor, turn.runID, failure)
			},
		})
		turn.success = outcome.success
		if !outcome.success {
			turn.finalErr = &agentexecution.Error{Agent: outcome.failure}
		}
		turn.close()
	})
	<-turn.done
	return turn.success
}

func (turn *Turn) finishFailure(code agentprotocol.ErrorCode, usage agentprotocol.Usage, alreadySettled bool) {
	if turn.handoffTerminal(terminalRequest{usage: usage, code: code, alreadySettled: alreadySettled}) {
		return
	}
	turn.finalizeFailure(code, usage, alreadySettled)
}

func (turn *Turn) finalizeFailure(code agentprotocol.ErrorCode, usage agentprotocol.Usage, alreadySettled bool) {
	turn.finalOnce.Do(func() {
		var end func(context.Context) error
		if !alreadySettled {
			end = func(ctx context.Context) error {
				return turn.withOperation(ctx, func() error {
					return turn.gateway.runs.EndOperation(ctx, turn.actor, turn.handle, usage, false, string(code))
				})
			}
		}
		outcome := finalizeTerminal(turn.terminalTiming(), func() agentprotocol.AgentError {
			if turn.ctx.Err() != nil {
				code = turn.contextCode()
			}
			return turn.failureFor(code)
		}, terminalCallbacks{
			end: end,
			fail: func(ctx context.Context, failure agentprotocol.AgentError) error {
				return turn.gateway.runs.Fail(ctx, turn.actor, turn.runID, failure)
			},
		})
		turn.finalErr = &agentexecution.Error{Agent: outcome.failure}
		turn.close()
	})
	<-turn.done
}

func (turn *Turn) handoffTerminal(request terminalRequest) bool {
	if turn.ctx.Err() == nil {
		return false
	}
	select {
	case turn.terminalReady <- request:
	case <-turn.done:
	}
	<-turn.done
	return true
}

func (turn *Turn) contextCode() agentprotocol.ErrorCode {
	turn.mu.Lock()
	explicit := turn.explicitCancel
	turn.mu.Unlock()
	if explicit != nil {
		return explicit.Code
	}
	if errors.Is(turn.ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(turn.ctx), context.DeadlineExceeded) {
		return agentprotocol.ErrorCodeTimeout
	}
	if turn.ctx.Err() != nil {
		return agentprotocol.ErrorCodeInternalError
	}
	return agentprotocol.ErrorCodeInternalError
}

func (turn *Turn) failureFor(code agentprotocol.ErrorCode) agentprotocol.AgentError {
	turn.mu.Lock()
	explicit := turn.explicitCancel
	turn.mu.Unlock()
	if explicit != nil && explicit.Code == code {
		return *explicit
	}
	message := "model turn failed"
	if code == agentprotocol.ErrorCodeTimeout {
		message = "model turn timed out"
	} else if code == agentprotocol.ErrorCodeInternalError && turn.ctx.Err() != nil {
		message = "execution_interrupted"
	}
	return agentprotocol.AgentError{Code: code, Message: message, Retryable: false}
}

func (turn *Turn) publishFinalError(yield func(agentprotocol.ProviderEvent, error) bool) {
	<-turn.done
	turn.mu.Lock()
	err := turn.finalErr
	turn.mu.Unlock()
	if err == nil {
		err = gatewayFailure(agentprotocol.ErrorCodeInternalError, "model turn failed")
	}
	yield(agentprotocol.ProviderEvent{}, err)
}

func (turn *Turn) cancel(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	turn.mu.Lock()
	if turn.cancelStarted.IsZero() {
		turn.cancelStarted = time.Now()
	}
	turn.mu.Unlock()
	turn.cancelCtx(cause)
}

func (turn *Turn) observeProviderAttempt(outcome string, code agentprotocol.ErrorCode, usage agentprotocol.Usage, usageComplete bool, attempt int, startedAt, exitedAt time.Time) {
	if turn == nil || turn.gateway == nil {
		return
	}
	cancelLatency := turn.providerCancelLatency(code, exitedAt)
	duration := exitedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	observation := agentexecution.Observation{
		Kind: "provider", Mode: string(turn.actor.Mode), ModelProfile: turn.profile.ID,
		Outcome: outcome, ErrorCode: string(code), Duration: duration, CancelLatency: cancelLatency,
		Usage: usage, UsageComplete: usageComplete, Attempts: attempt,
	}
	if turn.gateway.observer != nil {
		turn.gateway.observer.ObserveAgent(observation)
	}
	if turn.gateway.logger != nil {
		turn.gateway.logger.Info("agent operation completed",
			"runId", turn.runID.String(), "turnId", turn.request.TurnID, "mode", turn.actor.Mode,
			"profile", turn.profile.ID, "outcome", outcome, "errorCode", code,
			"durationMs", duration.Milliseconds(), "cancelLatencyMs", cancelLatency.Milliseconds(),
			"inputTokens", usage.InputTokens, "outputTokens", usage.OutputTokens, "totalTokens", usage.TotalTokens,
			"usageComplete", usageComplete, "attempt", attempt,
		)
	}
}

func (turn *Turn) providerCancelLatency(code agentprotocol.ErrorCode, exitedAt time.Time) time.Duration {
	if code == agentprotocol.ErrorCodeCancelled {
		timing := turn.terminalTiming()
		if time.Until(timing.overallDeadline) <= 0 {
			return 0
		}
		ctx, cancel := context.WithDeadline(context.Background(), timing.overallDeadline)
		defer cancel()
		latency, present, _ := turn.gateway.runs.CancellationLatency(ctx, turn.actor, turn.runID, exitedAt)
		if present {
			return latency
		}
		return 0
	}
	if code != agentprotocol.ErrorCodeTimeout && code != agentprotocol.ErrorCodeInternalError {
		return 0
	}
	turn.mu.Lock()
	started := turn.cancelStarted
	turn.mu.Unlock()
	if code == agentprotocol.ErrorCodeTimeout && turn.deadline.Before(exitedAt) && (started.IsZero() || turn.deadline.Before(started)) {
		started = turn.deadline
	}
	if started.IsZero() || started.After(exitedAt) {
		return 0
	}
	return exitedAt.Sub(started)
}

func providerOutcome(code agentprotocol.ErrorCode) string {
	if code == agentprotocol.ErrorCodeCancelled {
		return "cancelled"
	}
	return "failed"
}

func (turn *Turn) cancelExplicit(cause error) {
	failure := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeCancelled, Message: "cancelled", Retryable: false}
	if errors.Is(cause, context.DeadlineExceeded) {
		failure = agentprotocol.AgentError{Code: agentprotocol.ErrorCodeTimeout, Message: "model turn timed out", Retryable: false}
	}
	var classified *agentexecution.Error
	if errors.As(cause, &classified) && agentprotocol.Validate(agentprotocol.DefinitionAgentError, classified.Agent) == nil {
		failure = classified.Agent
		failure.Retryable = false
	}
	turn.mu.Lock()
	if turn.explicitCancel == nil {
		copied := failure
		turn.explicitCancel = &copied
	}
	turn.mu.Unlock()
	turn.cancel(cause)
}

func (turn *Turn) watchCancellation() {
	select {
	case <-turn.ctx.Done():
		timing := turn.terminalTiming()
		turn.mu.Lock()
		consumed := turn.consumed
		turn.mu.Unlock()
		if !consumed {
			turn.finalizeFailure(turn.contextCode(), agentprotocol.Usage{}, false)
			return
		}
		accounting := time.NewTimer(time.Until(timing.endDeadline))
		defer accounting.Stop()
		select {
		case <-turn.done:
			return
		case terminal := <-turn.terminalReady:
			if terminal.success {
				turn.finalizeSuccess(terminal.usage)
			} else {
				turn.finalizeFailure(terminal.code, terminal.usage, terminal.alreadySettled)
			}
			return
		case <-turn.consumerDone:
		case <-accounting.C:
		}
		select {
		case <-turn.done:
			return
		default:
			turn.finalizeFailure(turn.contextCode(), agentprotocol.Usage{}, false)
		}
	case <-turn.done:
	}
}

func (turn *Turn) terminalTiming() cleanupTiming {
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.cleanup.overallDeadline.IsZero() {
		started := time.Now()
		turn.cleanup.overallDeadline = started.Add(settlementLimit)
		turn.cleanup.endDeadline = turn.cleanup.overallDeadline.Add(-2 * time.Second)
		turn.cleanup.authorizeDeadline = turn.cleanup.overallDeadline.Add(-time.Second)
	}
	return turn.cleanup
}

func (turn *Turn) withOperation(ctx context.Context, operation func() error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-turn.opGate:
	}
	defer func() { turn.opGate <- struct{}{} }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return operation()
}

func (turn *Turn) close() {
	turn.cancelDeadline()
	turn.cancelCtx(context.Canceled)
	turn.gateway.registry.release(turn.runID, turn)
	close(turn.done)
}

type runProvider struct {
	gateway *Gateway
	actor   agentexecution.Actor
	runID   uuid.UUID
}

func (provider runProvider) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		if request.RunID != provider.runID.String() {
			yield(agentprotocol.ProviderEvent{}, gatewayFailure(agentprotocol.ErrorCodeValidationFailed, "model turn run ID does not match the stream"))
			return
		}
		turn, err := provider.gateway.Prepare(ctx, provider.actor, request)
		if err != nil {
			yield(agentprotocol.ProviderEvent{}, err)
			return
		}
		for event, eventErr := range turn.Events(ctx) {
			if !yield(event, eventErr) {
				turn.cancel(context.Canceled)
				return
			}
		}
	}
}

var _ agentprovider.StreamProvider = runProvider{}
