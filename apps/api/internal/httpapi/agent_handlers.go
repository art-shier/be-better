package httpapi

import (
	"net/http"

	"dayorder.local/api/internal/service"
)

func (router *Router) createAgentRun(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(response, request, authenticated.Account.ID)
	if !ok {
		return
	}
	var input service.StartAgentInput
	if !router.decodeJSON(response, request, &input, 32<<10) {
		return
	}
	run, err := router.agents.Create(request.Context(), mutation, input)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	setEntityVersion(response, run.Version)
	router.writeJSON(response, http.StatusCreated, run)
}

func (router *Router) getAgentRun(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	runID, ok := router.pathUUID(response, request, "runId")
	if !ok {
		return
	}
	run, err := router.agents.Get(request.Context(), authenticated.Account.ID, runID)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	setEntityVersion(response, run.Version)
	router.writeJSON(response, http.StatusOK, run)
}

func (router *Router) listAgentRuns(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	limit, ok := router.pageSize(response, request)
	if !ok {
		return
	}
	page, err := router.agents.List(request.Context(), authenticated.Account.ID, request.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, page)
}

func (router *Router) acceptAgentChange(response http.ResponseWriter, request *http.Request) {
	router.resolveAgentChange(response, request, true)
}

func (router *Router) rejectAgentChange(response http.ResponseWriter, request *http.Request) {
	router.resolveAgentChange(response, request, false)
}

func (router *Router) resolveAgentChange(response http.ResponseWriter, request *http.Request, accept bool) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	changeID, ok := router.pathUUID(response, request, "changeId")
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
	var result any
	var version int64
	if accept {
		applied, err := router.agents.Accept(request.Context(), mutation, changeID, expected)
		if err != nil {
			router.handleServiceError(response, request, err)
			return
		}
		result, version = applied, applied.Change.Version
	} else {
		rejected, err := router.agents.Reject(request.Context(), mutation, changeID, expected)
		if err != nil {
			router.handleServiceError(response, request, err)
			return
		}
		result, version = rejected, rejected.Change.Version
	}
	setEntityVersion(response, version)
	router.writeJSON(response, http.StatusOK, result)
}

func (router *Router) stopAgentRun(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	runID, ok := router.pathUUID(response, request, "runId")
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
	run, err := router.agents.Stop(request.Context(), mutation, runID, expected)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	setEntityVersion(response, run.Version)
	router.writeJSON(response, http.StatusOK, run)
}
