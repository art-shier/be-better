package httpapi

import (
	"net/http"
	"strings"
)

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
