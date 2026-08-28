package httpapi

import (
	"net/http"

	"dayorder.local/api/internal/service"
)

func (router *Router) registerDevice(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	deviceID, ok := router.pathUUID(response, request, "deviceId")
	if !ok {
		return
	}
	var input service.RegisterDeviceInput
	if !router.decodeJSON(response, request, &input, 32<<10) {
		return
	}
	registration, err := router.devices.Register(request.Context(), authenticated.Account.ID, deviceID, input)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	status := http.StatusOK
	if registration.Created {
		status = http.StatusCreated
	}
	router.writeJSON(response, status, registration)
}

func (router *Router) listDevices(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	devices, err := router.devices.List(request.Context(), authenticated.Account.ID)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, map[string]any{"devices": devices})
}
