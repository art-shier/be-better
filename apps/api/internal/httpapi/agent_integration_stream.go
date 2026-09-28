package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"

	"github.com/google/uuid"
)

const (
	agentSSEHeartbeatInterval = 10 * time.Second
	agentSSEWriteLimit        = 15 * time.Second
	maxAgentSSEDataBytes      = 64 << 10
	maxAgentSSETurnBytes      = 1 << 20
)

type agentStreamItem struct {
	event agentprotocol.ProviderEvent
	err   error
}

func (router *agentIntegrationRouter) streamAgentTurn(response http.ResponseWriter, request *http.Request) {
	if !router.requireAgentPost(response, request) {
		return
	}
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	if _, ok = router.ownedAgentDevice(response, request, authenticated.Account.ID); !ok {
		return
	}
	runID, ok := router.pathUUID(response, request, "runId")
	if !ok {
		return
	}
	turnID := request.PathValue("turnId")
	if !validAgentOperationID(turnID) {
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "请求数据不符合要求", false, nil)
		return
	}
	var input agentprotocol.ModelTurnRequest
	if !router.decodeAgentJSON(response, request, &input) {
		return
	}
	requestRunID, err := parseAgentRequestRunID(input.RunID)
	if err != nil || requestRunID != runID || input.TurnID != turnID || !validAgentOperationID(input.TurnID) {
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "请求数据不符合要求", false, nil)
		return
	}
	input.RunID = runID.String()
	view, err := router.runs.Get(request.Context(), authenticated.Account.ID, runID)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	if view.ExecutionMode != agentprotocol.ExecutionModeForeground {
		router.writeError(response, request, http.StatusConflict, "EXECUTION_MODE_CONFLICT", "该 Run 不允许前台模型流", false, nil)
		return
	}
	runDeadline, err := time.Parse(time.RFC3339Nano, string(view.DeadlineAt))
	if err != nil {
		router.logger.Error("agent integration run deadline invalid", "requestId", requestID(request), "category", "internal")
		router.writeError(response, request, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时无法完成请求", true, nil)
		return
	}

	streamContext, cancelStream := context.WithCancel(request.Context())
	actor := agentexecution.Actor{UserID: authenticated.Account.ID, Mode: agentprotocol.ExecutionModeForeground}
	turn, err := router.gateway.Prepare(streamContext, actor, input)
	if err != nil {
		cancelStream()
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	deliveredTerminal := false
	defer func() {
		cancelStream()
		if !deliveredTerminal {
			router.convergeAgentTransportFailure(actor, runID)
		}
	}()

	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")

	items := make(chan agentStreamItem)
	go func() {
		defer close(items)
		for event, eventErr := range turn.Events(streamContext) {
			select {
			case items <- agentStreamItem{event: event, err: eventErr}:
			case <-streamContext.Done():
				return
			}
		}
	}()

	heartbeat := time.NewTicker(agentSSEHeartbeatInterval)
	defer heartbeat.Stop()
	sequence, totalBytes := 1, 0
	for {
		select {
		case <-request.Context().Done():
			return
		case <-heartbeat.C:
			if err = writeAgentSSEHeartbeat(response, runDeadline); err != nil {
				return
			}
		case item, open := <-items:
			if !open {
				failure := controlledAgentStreamError(nil)
				_, _ = writeAgentSSEEnvelope(response, input, sequence, failure, runDeadline, totalBytes)
				return
			}
			if item.err != nil {
				failure := controlledAgentStreamError(item.err)
				_, writeErr := writeAgentSSEEnvelope(response, input, sequence, failure, runDeadline, totalBytes)
				deliveredTerminal = writeErr == nil
				return
			}
			written, writeErr := writeAgentSSEEnvelope(response, input, sequence, item.event, runDeadline, totalBytes)
			if writeErr != nil {
				if errors.Is(writeErr, errAgentSSELimit) {
					failure := controlledAgentStreamError(writeErr)
					_, _ = writeAgentSSEEnvelope(response, input, sequence, failure, runDeadline, totalBytes)
				}
				return
			}
			totalBytes += written
			sequence++
			if item.event.Type == agentprotocol.ProviderEventTypeCompleted || item.event.Type == agentprotocol.ProviderEventTypeError {
				deliveredTerminal = true
				return
			}
		}
	}
}

func writeAgentSSEHeartbeat(response http.ResponseWriter, runDeadline time.Time) error {
	if err := setAgentWriteDeadline(response, runDeadline); err != nil {
		return err
	}
	if _, err := fmt.Fprint(response, ": heartbeat\n\n"); err != nil {
		return err
	}
	return http.NewResponseController(response).Flush()
}

func (router *agentIntegrationRouter) convergeAgentTransportFailure(actor agentexecution.Actor, runID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	failure := agentprotocol.AgentError{Code: agentprotocol.ErrorCodeInternalError, Message: "execution_interrupted", Retryable: false}
	if err := router.runs.Fail(ctx, actor, runID, failure); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// Runs.Fail preserves every existing terminal state and gives the Run
		// deadline precedence. EndOperation may still settle accounting after
		// this transport transition, so Usage completeness is not ownership.
		router.logger.Error("agent transport convergence failed", "category", "internal")
	}
}

func parseAgentRequestRunID(value string) (uuid.UUID, error) {
	return uuid.Parse(value)
}

func setAgentWriteDeadline(response http.ResponseWriter, runDeadline time.Time) error {
	deadline := time.Now().Add(agentSSEWriteLimit)
	if runDeadline.Before(deadline) {
		deadline = runDeadline
	}
	err := http.NewResponseController(response).SetWriteDeadline(deadline)
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

var errAgentSSELimit = errors.New("agent SSE data limit exceeded")

func writeAgentSSEEnvelope(response http.ResponseWriter, request agentprotocol.ModelTurnRequest, sequence int, event agentprotocol.ProviderEvent, runDeadline time.Time, totalBytes int) (int, error) {
	envelope := agentprotocol.ProviderEnvelope{
		ProtocolVersion: "2.0", RunID: request.RunID, TurnID: request.TurnID, Sequence: sequence, Event: event,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return 0, err
	}
	if len(payload) > maxAgentSSEDataBytes || totalBytes < 0 || totalBytes > maxAgentSSETurnBytes-len(payload) {
		return 0, errAgentSSELimit
	}
	if event.Type != agentprotocol.ProviderEventTypeCompleted && event.Type != agentprotocol.ProviderEventTypeError {
		terminalBytes, reserveErr := agentSSEControlledTerminalReserve(request, sequence+1)
		if reserveErr != nil {
			return 0, reserveErr
		}
		if terminalBytes > maxAgentSSETurnBytes-totalBytes-len(payload) {
			return 0, errAgentSSELimit
		}
	}
	if err = setAgentWriteDeadline(response, runDeadline); err != nil {
		return 0, err
	}
	if _, err = fmt.Fprintf(response, "data: %s\n\n", payload); err != nil {
		return 0, err
	}
	if err = http.NewResponseController(response).Flush(); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func agentSSEControlledTerminalReserve(request agentprotocol.ModelTurnRequest, sequence int) (int, error) {
	inputs := []error{nil}
	for _, code := range []agentprotocol.ErrorCode{
		agentprotocol.ErrorCodeCancelled,
		agentprotocol.ErrorCodeTimeout,
		agentprotocol.ErrorCodeProviderRateLimited,
		agentprotocol.ErrorCodeProviderUnavailable,
		agentprotocol.ErrorCodePermissionDenied,
		agentprotocol.ErrorCodeProtocolIncompatible,
		agentprotocol.ErrorCodeValidationFailed,
		agentprotocol.ErrorCodeVersionConflict,
	} {
		inputs = append(inputs, &agentexecution.Error{Agent: agentprotocol.AgentError{Code: code}})
	}
	maximum := 0
	for _, input := range inputs {
		payload, err := json.Marshal(agentprotocol.ProviderEnvelope{
			ProtocolVersion: "2.0", RunID: request.RunID, TurnID: request.TurnID, Sequence: sequence,
			Event: controlledAgentStreamError(input),
		})
		if err != nil {
			return 0, err
		}
		if len(payload) > maximum {
			maximum = len(payload)
		}
	}
	return maximum, nil
}

func controlledAgentStreamError(err error) agentprotocol.ProviderEvent {
	code := agentprotocol.ErrorCodeInternalError
	if protocol, ok := agentIntegrationProtocolError(err); ok {
		code = protocol.Code
	}
	message, retryable := "model turn failed", false
	switch code {
	case agentprotocol.ErrorCodeCancelled:
		message = "model turn cancelled"
	case agentprotocol.ErrorCodeTimeout:
		message = "model turn timed out"
	case agentprotocol.ErrorCodeProviderRateLimited:
		message, retryable = "model provider rate limited", true
	case agentprotocol.ErrorCodeProviderUnavailable:
		message, retryable = "model provider unavailable", true
	case agentprotocol.ErrorCodePermissionDenied:
		message = "model turn not permitted"
	case agentprotocol.ErrorCodeProtocolIncompatible:
		message = "model protocol incompatible"
	case agentprotocol.ErrorCodeValidationFailed:
		message = "model turn invalid"
	case agentprotocol.ErrorCodeVersionConflict:
		message = "model turn state conflict"
	default:
		code = agentprotocol.ErrorCodeInternalError
	}
	return agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeError, Error: &agentprotocol.AgentError{Code: code, Message: message, Retryable: retryable}}
}
