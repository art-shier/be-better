package httpapi

import (
	"errors"
	"net/http"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"
)

var errMissingApplications = errors.New("account and session applications are required")

type apiErrorEnvelope struct {
	Error apiErrorBody `json:"error"`
}

type apiErrorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	Retryable bool              `json:"retryable"`
	RequestID string            `json:"requestId"`
}

func (router *Router) handleServiceError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, model.ErrNotFound):
		router.writeError(response, request, http.StatusNotFound, "RESOURCE_NOT_FOUND", "资源不存在", false, nil)
	case errors.Is(err, model.ErrConflict):
		router.writeError(response, request, http.StatusConflict, "ENTITY_VERSION_CONFLICT", "资源版本已变化，请获取最新版本", false, nil)
	case errors.Is(err, model.ErrDeviceNotActive):
		router.writeError(response, request, http.StatusPreconditionRequired, "DEVICE_REGISTRATION_REQUIRED", "设备未注册或已被撤销，请重新注册设备", false, nil)
	case errors.Is(err, service.ErrIdempotencyConflict):
		router.writeError(response, request, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "同一幂等键不能用于不同请求", false, nil)
	case errors.Is(err, service.ErrInvalidCursor), errors.Is(err, service.ErrInvalidSyncCursor):
		router.writeError(response, request, http.StatusBadRequest, "INVALID_CURSOR", "游标无效", false, nil)
	case errors.Is(err, service.ErrSyncResetRequired):
		router.writeError(response, request, http.StatusConflict, "SYNC_RESET_REQUIRED", "同步游标已过期，需要重建本地缓存", false, nil)
	case errors.Is(err, service.ErrValidation):
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "请求数据不符合要求", false, nil)
	case errors.Is(err, service.ErrEmailInUse):
		router.writeError(response, request, http.StatusConflict, "EMAIL_ALREADY_REGISTERED", "该邮箱已注册", false, nil)
	case errors.Is(err, service.ErrInvalidCredentials):
		router.writeError(response, request, http.StatusUnauthorized, "INVALID_CREDENTIALS", "邮箱或密码不正确", false, nil)
	case errors.Is(err, service.ErrInvalidSession):
		router.writeError(response, request, http.StatusUnauthorized, "AUTH_REQUIRED", "需要登录后继续", false, nil)
	case errors.Is(err, service.ErrAccountNotActive):
		router.writeError(response, request, http.StatusForbidden, "ACCOUNT_NOT_ACTIVE", "账号尚未激活或已停用", false, nil)
	case errors.Is(err, service.ErrInvalidToken):
		router.writeError(response, request, http.StatusUnprocessableEntity, "TOKEN_INVALID", "令牌无效或已过期", false, nil)
	default:
		router.logger.Error("request failed", "requestId", requestID(request), "error", err)
		router.writeError(response, request, http.StatusInternalServerError, "INTERNAL_ERROR", "服务暂时无法完成请求", true, nil)
	}
}

func (router *Router) writeError(response http.ResponseWriter, request *http.Request, status int, code, message string, retryable bool, fields map[string]string) {
	router.writeJSON(response, status, apiErrorEnvelope{Error: apiErrorBody{
		Code: code, Message: message, Fields: fields, Retryable: retryable, RequestID: requestID(request),
	}})
}
