package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

const (
	maxAgentRequestBytes = 256 << 10
	maxAgentJSONDepth    = 16
	maxAgentOperationID  = 128
)

func (router *agentIntegrationRouter) createAgentReadonlyRun(response http.ResponseWriter, request *http.Request) {
	if !router.requireAgentPost(response, request) {
		return
	}
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(response, request, authenticated.Account.ID)
	if !ok {
		return
	}
	var input agentprotocol.ReadonlyRunStart
	if !router.decodeAgentJSON(response, request, &input) {
		return
	}
	view, err := router.runs.Create(request.Context(), mutation, input)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	setEntityVersion(response, int64(view.Version))
	router.writeJSON(response, http.StatusCreated, view)
}

func (router *agentIntegrationRouter) getAgentReadonlyRun(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	runID, ok := router.pathUUID(response, request, "runId")
	if !ok {
		return
	}
	view, err := router.runs.Get(request.Context(), authenticated.Account.ID, runID)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	setEntityVersion(response, int64(view.Version))
	router.writeJSON(response, http.StatusOK, view)
}

func (router *agentIntegrationRouter) cancelAgentReadonlyRun(response http.ResponseWriter, request *http.Request) {
	if !router.requireAgentPost(response, request) {
		return
	}
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(response, request, authenticated.Account.ID)
	if !ok {
		return
	}
	runID, ok := router.pathUUID(response, request, "runId")
	if !ok {
		return
	}
	version, ok := router.expectedVersion(response, request)
	if !ok {
		return
	}
	if !router.consumeAgentEmptyJSON(response, request) {
		return
	}
	view, err := router.runs.Cancel(request.Context(), mutation, runID, version)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	// The persisted cancellation is authoritative. Only after it succeeds may
	// the in-process turn receive the trusted user cancellation signal.
	router.gateway.Cancel(runID, context.Canceled)
	setEntityVersion(response, int64(view.Version))
	router.writeJSON(response, http.StatusOK, view)
}

func (router *agentIntegrationRouter) finishAgentReadonlyRun(response http.ResponseWriter, request *http.Request) {
	if !router.requireAgentPost(response, request) {
		return
	}
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(response, request, authenticated.Account.ID)
	if !ok {
		return
	}
	runID, ok := router.pathUUID(response, request, "runId")
	if !ok {
		return
	}
	version, ok := router.expectedVersion(response, request)
	if !ok {
		return
	}
	var input agentprotocol.ReadonlyRunFinish
	if !router.decodeAgentJSON(response, request, &input) {
		return
	}
	view, err := router.runs.Finish(request.Context(), mutation, runID, version, input)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	setEntityVersion(response, int64(view.Version))
	router.writeJSON(response, http.StatusOK, view)
}

func (router *agentIntegrationRouter) readAgentCalendar(response http.ResponseWriter, request *http.Request) {
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
	var input agentprotocol.CalendarReadRequest
	if !router.decodeAgentJSON(response, request, &input) {
		return
	}
	if !validAgentOperationID(input.CallID) {
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "请求数据不符合要求", false, nil)
		return
	}
	view, err := router.runs.Get(request.Context(), authenticated.Account.ID, runID)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	runDeadline, err := time.Parse(time.RFC3339Nano, string(view.DeadlineAt))
	if err != nil {
		router.logger.Error("agent integration run deadline invalid", "requestId", requestID(request), "category", "internal")
		router.writeError(response, request, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时无法完成请求", true, nil)
		return
	}
	toolContext, cancelTool := context.WithDeadline(request.Context(), agentIntegrationToolDeadline(time.Now(), runDeadline, router.toolTimeout))
	defer cancelTool()
	actor := agentexecution.Actor{UserID: authenticated.Account.ID, Mode: agentprotocol.ExecutionModeForeground}
	result, err := router.calendar.Read(toolContext, actor, runID, input.CallID, input.Input)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, result)
}

func agentIntegrationToolDeadline(now, runDeadline time.Time, toolTimeout time.Duration) time.Time {
	deadline := now.Add(toolTimeout)
	if runDeadline.Before(deadline) {
		return runDeadline
	}
	return deadline
}

func (router *agentIntegrationRouter) requireAgentPost(response http.ResponseWriter, request *http.Request) bool {
	origin := strings.TrimSuffix(strings.TrimSpace(request.Header.Get("Origin")), "/")
	if origin == "" {
		router.writeError(response, request, http.StatusForbidden, "ORIGIN_REQUIRED", "请求来源无法验证", false, nil)
		return false
	}
	if !router.trustedAgentOrigin(origin, request) {
		router.writeError(response, request, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "请求来源不被允许", false, nil)
		return false
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(request.Header.Get("Content-Type")))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		router.writeError(response, request, http.StatusUnsupportedMediaType, "JSON_REQUIRED", "请求必须使用 application/json", false, nil)
		return false
	}
	return true
}

func (router *agentIntegrationRouter) trustedAgentOrigin(origin string, request *http.Request) bool {
	if _, allowed := router.allowedOrigins[origin]; allowed {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	wantScheme := "http"
	if requestIsHTTPS(request) {
		wantScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, wantScheme) && strings.EqualFold(parsed.Host, request.Host)
}

func (router *agentIntegrationRouter) ownedAgentDevice(response http.ResponseWriter, request *http.Request, userID uuid.UUID) (uuid.UUID, bool) {
	deviceID, ok := router.requestDeviceID(response, request)
	if !ok {
		return uuid.Nil, false
	}
	devices, err := router.devices.List(request.Context(), userID)
	if err != nil {
		router.handleAgentIntegrationError(response, request, err)
		return uuid.Nil, false
	}
	for _, device := range devices {
		if device.ID == deviceID && device.UserID == userID && device.RevokedAt == nil {
			return deviceID, true
		}
	}
	router.writeError(response, request, http.StatusPreconditionRequired, "DEVICE_REGISTRATION_REQUIRED", "设备未注册或已被撤销，请重新注册设备", false, nil)
	return uuid.Nil, false
}

func (router *agentIntegrationRouter) decodeAgentJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(response, request.Body, maxAgentRequestBytes+1)
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			router.writeError(response, request, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "请求内容过大", false, nil)
			return false
		}
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求内容不完整或格式不正确", false, nil)
		return false
	}
	if len(raw) > maxAgentRequestBytes {
		router.writeError(response, request, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "请求内容过大", false, nil)
		return false
	}
	if !json.Valid(raw) || agentJSONDepth(raw) > maxAgentJSONDepth {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求内容不完整或格式不正确", false, nil)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求内容不完整或格式不正确", false, nil)
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求只能包含一个 JSON 对象", false, nil)
		return false
	}
	return true
}

func (router *agentIntegrationRouter) consumeAgentEmptyJSON(response http.ResponseWriter, request *http.Request) bool {
	request.Body = http.MaxBytesReader(response, request.Body, maxAgentRequestBytes+1)
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			router.writeError(response, request, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "请求内容过大", false, nil)
			return false
		}
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求内容不完整或格式不正确", false, nil)
		return false
	}
	if len(raw) > maxAgentRequestBytes {
		router.writeError(response, request, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "请求内容过大", false, nil)
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	var body map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&body); err != nil || body == nil || len(body) != 0 {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "取消请求不能包含字段", false, nil)
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_REQUEST", "请求只能包含一个 JSON 对象", false, nil)
		return false
	}
	return true
}

func agentJSONDepth(raw []byte) int {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	depth, maximum := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return maximum
		}
		if err != nil {
			return maxAgentJSONDepth + 1
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delimiter {
		case '{', '[':
			depth++
			if depth > maximum {
				maximum = depth
			}
		case '}', ']':
			depth--
		}
	}
}

func validAgentOperationID(identifier string) bool {
	return identifier != "" && identifier == strings.TrimSpace(identifier) && len(identifier) <= maxAgentOperationID
}

func (router *agentIntegrationRouter) handleAgentIntegrationError(response http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, model.ErrNotFound) {
		router.writeError(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在", false, nil)
		return
	}
	if errors.Is(err, model.ErrConflict) {
		router.writeError(response, request, http.StatusConflict, "ENTITY_VERSION_CONFLICT", "资源版本或状态已变化", false, nil)
		return
	}
	if errors.Is(err, model.ErrDeviceNotActive) {
		router.writeError(response, request, http.StatusPreconditionRequired, "DEVICE_REGISTRATION_REQUIRED", "设备未注册或已被撤销，请重新注册设备", false, nil)
		return
	}
	if errors.Is(err, service.ErrIdempotencyConflict) {
		router.writeError(response, request, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "同一幂等键不能用于不同请求", false, nil)
		return
	}
	if errors.Is(err, service.ErrValidation) {
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "请求数据不符合要求", false, nil)
		return
	}
	if protocolError, ok := agentIntegrationProtocolError(err); ok {
		router.writeAgentProtocolHTTPError(response, request, protocolError.Code)
		return
	}
	// This scoped boundary deliberately records only a controlled category;
	// Provider, Tool, and persistence error text may contain private input.
	router.logger.Error("agent integration request failed", "requestId", requestID(request), "category", "internal")
	router.writeError(response, request, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时无法完成请求", true, nil)
}

func agentIntegrationProtocolError(err error) (agentprotocol.AgentError, bool) {
	var pointer *agentexecution.Error
	if errors.As(err, &pointer) && pointer != nil {
		return pointer.Agent, true
	}
	var value agentexecution.Error
	if errors.As(err, &value) {
		return value.Agent, true
	}
	return agentprotocol.AgentError{}, false
}

func (router *agentIntegrationRouter) writeAgentProtocolHTTPError(response http.ResponseWriter, request *http.Request, code agentprotocol.ErrorCode) {
	switch code {
	case agentprotocol.ErrorCodeValidationFailed, agentprotocol.ErrorCodeProtocolIncompatible:
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "请求数据不符合要求", false, nil)
	case agentprotocol.ErrorCodePermissionDenied, agentprotocol.ErrorCodeCapabilityUnavailable, agentprotocol.ErrorCodeApprovalDenied:
		router.writeError(response, request, http.StatusForbidden, "PERMISSION_DENIED", "没有权限执行该请求", false, nil)
	case agentprotocol.ErrorCodeVersionConflict, agentprotocol.ErrorCodeCancelled:
		router.writeError(response, request, http.StatusConflict, "ENTITY_VERSION_CONFLICT", "资源版本或状态已变化", false, nil)
	case agentprotocol.ErrorCodeTimeout:
		router.writeError(response, request, http.StatusRequestTimeout, "AGENT_TIMEOUT", "Agent 请求已超时", false, nil)
	case agentprotocol.ErrorCodeProviderRateLimited:
		router.writeError(response, request, http.StatusTooManyRequests, "PROVIDER_RATE_LIMITED", "模型服务暂时限流", true, nil)
	case agentprotocol.ErrorCodeProviderUnavailable, agentprotocol.ErrorCodeToolFailed:
		router.writeError(response, request, http.StatusServiceUnavailable, "AGENT_DEPENDENCY_UNAVAILABLE", "Agent 依赖服务暂时不可用", true, nil)
	default:
		router.writeError(response, request, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时无法完成请求", true, nil)
	}
}
