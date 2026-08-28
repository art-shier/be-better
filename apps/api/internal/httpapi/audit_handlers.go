package httpapi

import "net/http"

func (router *Router) listAuditEvents(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	limit, ok := router.pageSize(response, request)
	if !ok {
		return
	}
	page, err := router.audits.List(request.Context(), authenticated.Account.ID, request.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, page)
}

func (router *Router) getAuditEvent(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	eventID, ok := router.pathUUID(response, request, "auditEventId")
	if !ok {
		return
	}
	event, err := router.audits.Get(request.Context(), authenticated.Account.ID, eventID)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, event)
}

func (router *Router) undoAuditEvent(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	eventID, ok := router.pathUUID(response, request, "auditEventId")
	if !ok {
		return
	}
	expected, ok := router.expectedVersion(response, request)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(response, request, authenticated.Account.ID)
	if !ok {
		return
	}
	result, err := router.undos.Undo(request.Context(), mutation, eventID, expected)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	setEntityVersion(response, result.EntityVersion)
	router.writeJSON(response, http.StatusOK, result)
}
