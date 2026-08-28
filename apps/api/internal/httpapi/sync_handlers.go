package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

const maxSyncMutationRequestBytes = 4 << 20

type syncMutationItem struct {
	MutationID  uuid.UUID       `json:"mutationId"`
	Sequence    int64           `json:"sequence"`
	EntityType  string          `json:"entityType"`
	EntityID    uuid.UUID       `json:"entityId"`
	Operation   string          `json:"operation"`
	BaseVersion int64           `json:"baseVersion"`
	Payload     json.RawMessage `json:"payload"`
}

type syncMutationResult struct {
	MutationID uuid.UUID     `json:"mutationId"`
	Status     string        `json:"status"`
	Data       any           `json:"data,omitempty"`
	Error      *apiErrorBody `json:"error,omitempty"`
}

func (router *Router) syncBootstrap(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	deviceID, ok := router.requestDeviceID(response, request)
	if !ok {
		return
	}
	bootstrap, err := router.sync.Bootstrap(request.Context(), authenticated.Account.ID, deviceID)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, bootstrap)
}

func (router *Router) syncChanges(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	deviceID, ok := router.requestDeviceID(response, request)
	if !ok {
		return
	}
	limit, ok := router.syncPageSize(response, request)
	if !ok {
		return
	}
	cursor := strings.TrimSpace(request.URL.Query().Get("cursor"))
	if cursor == "" {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_CURSOR", "cursor 不能为空", false, nil)
		return
	}
	page, err := router.sync.DeviceChanges(request.Context(), authenticated.Account.ID, deviceID, cursor, limit)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, page)
}

func (router *Router) syncMutations(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	deviceID, ok := router.requestDeviceID(response, request)
	if !ok {
		return
	}
	var input struct {
		Mutations []syncMutationItem `json:"mutations"`
	}
	if !router.decodeJSON(response, request, &input, maxSyncMutationRequestBytes) {
		return
	}
	if len(input.Mutations) < 1 || len(input.Mutations) > 100 {
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "每批 Mutation 必须在 1 到 100 项之间", false, map[string]string{"mutations": "数量必须在 1 到 100 项之间"})
		return
	}
	identifier, _ := uuid.Parse(requestID(request))
	results := make([]syncMutationResult, 0, len(input.Mutations))
	var previousSequence int64
	for _, item := range input.Mutations {
		result := syncMutationResult{MutationID: item.MutationID}
		if err := validateSyncMutationItem(item, previousSequence); err != nil {
			result.Status = "rejected"
			if router.metrics != nil {
				router.metrics.ObserveSyncMutation(result.Status)
			}
			result.Error = &apiErrorBody{Code: "VALIDATION_FAILED", Message: "Mutation 数据不符合要求", Retryable: false, RequestID: requestID(request)}
			results = append(results, result)
			continue
		}
		previousSequence = item.Sequence
		duplicate := false
		data, err := router.applySyncMutation(request.Context(), service.MutationContext{
			UserID: authenticated.Account.ID, DeviceID: deviceID, MutationID: item.MutationID,
			RequestID: identifier, Duplicate: &duplicate,
		}, item)
		if err != nil {
			result.Status, result.Error = router.syncMutationError(request, err)
			if result.Status == "conflict" {
				current, readErr := router.currentSyncEntity(request.Context(), authenticated.Account.ID, item)
				switch {
				case readErr == nil:
					result.Data = current
				case errors.Is(readErr, model.ErrNotFound):
					result.Error.Code = "ENTITY_DELETED"
					result.Error.Message = "璧勬簮宸插垹闄ゆ垨涓嶅彲璁块棶"
				default:
					router.logger.Error("read current sync entity after conflict failed", "requestId", requestID(request), "entityType", item.EntityType, "entityId", item.EntityID, "error", readErr)
					result.Status = "rejected"
					result.Error.Code, result.Error.Message, result.Error.Retryable = "INTERNAL_ERROR", "鏈嶅姟鏆傛椂鏃犳硶璇诲彇褰撳墠璧勬簮", true
				}
			}
		} else if duplicate {
			result.Status = "duplicate"
			result.Data = data
		} else {
			result.Status = "applied"
			result.Data = data
		}
		if router.metrics != nil {
			router.metrics.ObserveSyncMutation(result.Status)
		}
		results = append(results, result)
	}
	router.writeJSON(response, http.StatusOK, map[string]any{"results": results})
}

func (router *Router) currentSyncEntity(ctx context.Context, userID uuid.UUID, item syncMutationItem) (any, error) {
	switch item.EntityType {
	case "goal":
		return router.goals.Get(ctx, userID, item.EntityID)
	case "milestone":
		return router.goals.GetMilestone(ctx, userID, item.EntityID)
	case "task":
		return router.tasks.Get(ctx, userID, item.EntityID)
	case "calendar_event":
		return router.calendar.Get(ctx, userID, item.EntityID)
	case "record":
		return router.content.GetRecord(ctx, userID, item.EntityID)
	case "note":
		return router.content.GetNote(ctx, userID, item.EntityID)
	case "daily_review":
		return router.content.GetReview(ctx, userID, item.EntityID)
	case "settings":
		return router.settings.Get(ctx, userID)
	default:
		return nil, service.ErrValidation
	}
}

func validateSyncMutationItem(item syncMutationItem, previousSequence int64) error {
	if item.MutationID == uuid.Nil || item.EntityID == uuid.Nil || item.Sequence <= previousSequence {
		return service.ErrValidation
	}
	if item.Operation != "create" && item.Operation != "update" && item.Operation != "delete" {
		return service.ErrValidation
	}
	if (item.Operation == "create" && item.BaseVersion != 0) || (item.Operation != "create" && item.BaseVersion < 1) {
		return service.ErrValidation
	}
	var payload map[string]any
	if len(item.Payload) == 0 || json.Unmarshal(item.Payload, &payload) != nil || payload == nil {
		return service.ErrValidation
	}
	return nil
}

func decodeSyncPayload(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid mutation payload", service.ErrValidation)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("%w: mutation payload must contain one object", service.ErrValidation)
	}
	return nil
}

func (router *Router) applySyncMutation(ctx context.Context, mutation service.MutationContext, item syncMutationItem) (any, error) {
	switch item.EntityType {
	case "goal":
		if router.goals == nil {
			return nil, service.ErrValidation
		}
		var input service.CreateGoalInput
		if err := decodeSyncPayload(item.Payload, &input); err != nil {
			return nil, err
		}
		switch item.Operation {
		case "create":
			if input.ID != nil && *input.ID != item.EntityID {
				return nil, service.ErrValidation
			}
			input.ID = &item.EntityID
			return router.goals.Create(ctx, mutation, input)
		case "update":
			return router.goals.Update(ctx, mutation, item.EntityID, item.BaseVersion, input)
		default:
			return nil, router.goals.Delete(ctx, mutation, item.EntityID, item.BaseVersion)
		}
	case "milestone":
		if router.goals == nil {
			return nil, service.ErrValidation
		}
		var input struct {
			ID          *uuid.UUID `json:"id,omitempty"`
			GoalID      uuid.UUID  `json:"goalId"`
			Title       string     `json:"title"`
			DueAt       *time.Time `json:"dueAt"`
			CompletedAt *time.Time `json:"completedAt"`
			SortOrder   int        `json:"sortOrder"`
		}
		if err := decodeSyncPayload(item.Payload, &input); err != nil {
			return nil, err
		}
		if item.Operation == "create" {
			if input.ID != nil && *input.ID != item.EntityID {
				return nil, service.ErrValidation
			}
			return router.goals.CreateMilestone(ctx, mutation, input.GoalID, service.CreateMilestoneInput{ID: &item.EntityID, Title: input.Title, DueAt: input.DueAt, CompletedAt: input.CompletedAt, SortOrder: input.SortOrder})
		}
		if item.Operation == "update" {
			return router.goals.UpdateMilestone(ctx, mutation, item.EntityID, item.BaseVersion, service.UpdateMilestoneInput{Title: input.Title, DueAt: input.DueAt, CompletedAt: input.CompletedAt, SortOrder: input.SortOrder})
		}
		return nil, router.goals.DeleteMilestone(ctx, mutation, item.EntityID, item.BaseVersion)
	case "task":
		if router.tasks == nil {
			return nil, service.ErrValidation
		}
		var input service.TaskInput
		if err := decodeSyncPayload(item.Payload, &input); err != nil {
			return nil, err
		}
		if item.Operation == "create" {
			if input.ID != nil && *input.ID != item.EntityID {
				return nil, service.ErrValidation
			}
			input.ID = &item.EntityID
			return router.tasks.Create(ctx, mutation, input)
		}
		if item.Operation == "update" {
			return router.tasks.Update(ctx, mutation, item.EntityID, item.BaseVersion, input)
		}
		return nil, router.tasks.Delete(ctx, mutation, item.EntityID, item.BaseVersion)
	case "calendar_event":
		if router.calendar == nil {
			return nil, service.ErrValidation
		}
		var input service.CalendarEventInput
		if err := decodeSyncPayload(item.Payload, &input); err != nil {
			return nil, err
		}
		if item.Operation == "create" {
			if input.ID != nil && *input.ID != item.EntityID {
				return nil, service.ErrValidation
			}
			input.ID = &item.EntityID
			return router.calendar.Create(ctx, mutation, input)
		}
		if item.Operation == "update" {
			return router.calendar.Update(ctx, mutation, item.EntityID, item.BaseVersion, input)
		}
		return nil, router.calendar.Delete(ctx, mutation, item.EntityID, item.BaseVersion)
	case "record":
		if router.content == nil {
			return nil, service.ErrValidation
		}
		var input service.RecordInput
		if err := decodeSyncPayload(item.Payload, &input); err != nil {
			return nil, err
		}
		if item.Operation == "create" {
			if input.ID != nil && *input.ID != item.EntityID {
				return nil, service.ErrValidation
			}
			input.ID = &item.EntityID
			return router.content.CreateRecord(ctx, mutation, input)
		}
		if item.Operation == "update" {
			return router.content.UpdateRecord(ctx, mutation, item.EntityID, item.BaseVersion, input)
		}
		return nil, router.content.DeleteRecord(ctx, mutation, item.EntityID, item.BaseVersion)
	case "note":
		if router.content == nil {
			return nil, service.ErrValidation
		}
		var input service.NoteInput
		if err := decodeSyncPayload(item.Payload, &input); err != nil {
			return nil, err
		}
		if item.Operation == "create" {
			if input.ID != nil && *input.ID != item.EntityID {
				return nil, service.ErrValidation
			}
			input.ID = &item.EntityID
			return router.content.CreateNote(ctx, mutation, input)
		}
		if item.Operation == "update" {
			return router.content.UpdateNote(ctx, mutation, item.EntityID, item.BaseVersion, input)
		}
		return nil, router.content.DeleteNote(ctx, mutation, item.EntityID, item.BaseVersion)
	case "daily_review":
		if router.content == nil {
			return nil, service.ErrValidation
		}
		var input service.ReviewInput
		if err := decodeSyncPayload(item.Payload, &input); err != nil {
			return nil, err
		}
		if item.Operation == "create" {
			if input.ID != nil && *input.ID != item.EntityID {
				return nil, service.ErrValidation
			}
			input.ID = &item.EntityID
			return router.content.CreateReview(ctx, mutation, input)
		}
		if item.Operation == "update" {
			return router.content.UpdateReview(ctx, mutation, item.EntityID, item.BaseVersion, input)
		}
		return nil, router.content.DeleteReview(ctx, mutation, item.EntityID, item.BaseVersion)
	case "settings":
		if router.settings == nil || item.Operation != "update" {
			return nil, service.ErrValidation
		}
		return router.settings.Patch(ctx, mutation, item.BaseVersion, item.Payload)
	default:
		return nil, service.ErrValidation
	}
}

func (router *Router) syncMutationError(request *http.Request, err error) (string, *apiErrorBody) {
	body := &apiErrorBody{Retryable: false, RequestID: requestID(request)}
	switch {
	case errors.Is(err, model.ErrConflict):
		body.Code, body.Message = "ENTITY_VERSION_CONFLICT", "资源版本已变化，请获取最新版本"
		return "conflict", body
	case errors.Is(err, model.ErrNotFound):
		body.Code, body.Message = "ENTITY_DELETED", "资源已删除或不可访问"
		return "conflict", body
	case errors.Is(err, model.ErrDeviceNotActive):
		body.Code, body.Message = "DEVICE_REGISTRATION_REQUIRED", "设备未注册或已被撤销，请重新注册设备"
		return "rejected", body
	case errors.Is(err, service.ErrIdempotencyConflict):
		body.Code, body.Message = "IDEMPOTENCY_CONFLICT", "同一 Mutation ID 不能用于不同请求"
		return "rejected", body
	case errors.Is(err, service.ErrValidation):
		body.Code, body.Message = "VALIDATION_FAILED", "Mutation 数据不符合要求"
		return "rejected", body
	default:
		router.logger.Error("sync mutation failed", "requestId", requestID(request), "error", err)
		body.Code, body.Message, body.Retryable = "INTERNAL_ERROR", "服务暂时无法完成 Mutation", true
		return "rejected", body
	}
}
