package httpapi

import (
	"net/http"

	"dayorder.local/api/internal/service"
)

var taskPatchFields = map[string]bool{"title": true, "status": true, "priority": true, "estimateMinutes": true, "dueAt": true, "scheduledStart": true, "scheduledEnd": true, "goalId": true, "sourceRecordId": true}

func (router *Router) createTask(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	var input service.TaskInput
	if !router.decodeJSON(w, r, &input, maxResourceRequestBytes) {
		return
	}
	value, err := router.tasks.Create(r.Context(), mutation, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusCreated, value)
}
func (router *Router) getTask(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "taskId")
	if !ok {
		return
	}
	value, err := router.tasks.Get(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) listTasks(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	limit, ok := router.pageSize(w, r)
	if !ok {
		return
	}
	page, err := router.tasks.List(r.Context(), auth.Account.ID, r.URL.Query().Get("status"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, page)
}
func (router *Router) updateTask(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "taskId")
	if !ok {
		return
	}
	version, ok := router.expectedVersion(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	current, err := router.tasks.Get(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	seed := service.TaskInput{Title: current.Title, Status: current.Status, Priority: current.Priority, EstimateMinutes: current.EstimateMinutes, DueAt: current.DueAt, ScheduledStart: current.ScheduledStart, ScheduledEnd: current.ScheduledEnd, GoalID: current.GoalID, SourceRecordID: current.SourceRecordID}
	var input service.TaskInput
	if !router.decodeMergePatch(w, r, seed, taskPatchFields, &input) {
		return
	}
	value, err := router.tasks.Update(r.Context(), mutation, id, version, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) deleteTask(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "taskId")
	if !ok {
		return
	}
	version, ok := router.expectedVersion(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	if err := router.tasks.Delete(r.Context(), mutation, id, version); err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
