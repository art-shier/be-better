package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

const maxResourceRequestBytes = 1 << 20

func (router *Router) mutationContext(response http.ResponseWriter, request *http.Request, userID uuid.UUID) (service.MutationContext, bool) {
	deviceID, ok := router.requestDeviceID(response, request)
	if !ok {
		return service.MutationContext{}, false
	}
	mutationID, err := uuid.Parse(strings.TrimSpace(request.Header.Get("Idempotency-Key")))
	if err != nil {
		router.writeError(response, request, http.StatusPreconditionRequired, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key 必须是有效 UUID", false, nil)
		return service.MutationContext{}, false
	}
	identifier, _ := uuid.Parse(requestID(request))
	return service.MutationContext{UserID: userID, DeviceID: deviceID, MutationID: mutationID, RequestID: identifier}, true
}

func (router *Router) requestDeviceID(response http.ResponseWriter, request *http.Request) (uuid.UUID, bool) {
	deviceID, err := uuid.Parse(strings.TrimSpace(request.Header.Get("X-Device-ID")))
	if err != nil {
		router.writeError(response, request, http.StatusPreconditionRequired, "DEVICE_ID_REQUIRED", "X-Device-ID 必须是有效 UUID", false, nil)
		return uuid.Nil, false
	}
	return deviceID, true
}

func (router *Router) pathUUID(response http.ResponseWriter, request *http.Request, name string) (uuid.UUID, bool) {
	identifier, err := uuid.Parse(request.PathValue(name))
	if err != nil {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_RESOURCE_ID", "资源 ID 格式无效", false, nil)
		return uuid.Nil, false
	}
	return identifier, true
}

func (router *Router) expectedVersion(response http.ResponseWriter, request *http.Request) (int64, bool) {
	value := strings.TrimSpace(request.Header.Get("If-Match"))
	if value == "" {
		router.writeError(response, request, http.StatusPreconditionRequired, "IF_MATCH_REQUIRED", "修改或删除必须提供 If-Match", false, nil)
		return 0, false
	}
	value = strings.TrimPrefix(value, "W/")
	value = strings.Trim(value, `"`)
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_IF_MATCH", "If-Match 必须包含正整数版本", false, nil)
		return 0, false
	}
	return parsed, true
}

func setEntityVersion(response http.ResponseWriter, version int64) {
	response.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
}

func (router *Router) pageSize(response http.ResponseWriter, request *http.Request) (int, bool) {
	value := request.URL.Query().Get("limit")
	if value == "" {
		return 50, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 100 {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_PAGE_SIZE", "limit 必须在 1 到 100 之间", false, nil)
		return 0, false
	}
	return parsed, true
}

func (router *Router) syncPageSize(response http.ResponseWriter, request *http.Request) (int, bool) {
	value := request.URL.Query().Get("limit")
	if value == "" {
		return 100, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 500 {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_PAGE_SIZE", "limit 必须在 1 到 500 之间", false, nil)
		return 0, false
	}
	return parsed, true
}

func (router *Router) decodeMergePatch(response http.ResponseWriter, request *http.Request, seed any, allowed map[string]bool, target any) bool {
	if !router.requireMergePatch(response, request) {
		return false
	}
	seedBytes, err := json.Marshal(seed)
	if err != nil {
		router.handleServiceError(response, request, err)
		return false
	}
	var merged map[string]json.RawMessage
	if err = json.Unmarshal(seedBytes, &merged); err != nil {
		router.handleServiceError(response, request, err)
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxResourceRequestBytes)
	decoder := json.NewDecoder(request.Body)
	var patch map[string]json.RawMessage
	if err = decoder.Decode(&patch); err != nil || patch == nil {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_MERGE_PATCH", "Merge Patch 必须是 JSON 对象", false, nil)
		return false
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		router.writeError(response, request, http.StatusBadRequest, "INVALID_MERGE_PATCH", "请求只能包含一个 JSON 对象", false, nil)
		return false
	}
	for key, value := range patch {
		if !allowed[key] {
			router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Merge Patch 包含不可修改字段", false, map[string]string{key: "字段不可修改"})
			return false
		}
		merged[key] = value
	}
	encoded, _ := json.Marshal(merged)
	if err = json.Unmarshal(encoded, target); err != nil {
		router.writeError(response, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "Merge Patch 字段类型无效", false, nil)
		return false
	}
	return true
}

func (router *Router) requireMergePatch(response http.ResponseWriter, request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(request.Header.Get("Content-Type")))
	if err != nil || !strings.EqualFold(mediaType, "application/merge-patch+json") {
		router.writeError(
			response, request, http.StatusUnsupportedMediaType, "MERGE_PATCH_REQUIRED",
			"PATCH 必须使用 application/merge-patch+json", false, nil,
		)
		return false
	}
	return true
}

func parseOptionalTimeQuery(request *http.Request, key string) (*time.Time, error) {
	value := strings.TrimSpace(request.URL.Query().Get(key))
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}
