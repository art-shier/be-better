package httpapi

import (
	"net/http"

	"dayorder.local/api/internal/service"
)

var goalPatchFields = map[string]bool{"title": true, "why": true, "area": true, "metricType": true, "targetValue": true, "currentValue": true, "unit": true, "startDate": true, "dueDate": true, "status": true, "health": true}
var milestonePatchFields = map[string]bool{"title": true, "dueAt": true, "completedAt": true, "sortOrder": true}

func (router *Router) createGoal(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	var input service.CreateGoalInput
	if !router.decodeJSON(w, r, &input, maxResourceRequestBytes) {
		return
	}
	goal, err := router.goals.Create(r.Context(), mutation, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, goal.Version)
	router.writeJSON(w, http.StatusCreated, goal)
}
func (router *Router) getGoal(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "goalId")
	if !ok {
		return
	}
	goal, err := router.goals.Get(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, goal.Version)
	router.writeJSON(w, http.StatusOK, goal)
}
func (router *Router) listGoals(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	limit, ok := router.pageSize(w, r)
	if !ok {
		return
	}
	page, err := router.goals.List(r.Context(), auth.Account.ID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, page)
}
func (router *Router) updateGoal(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "goalId")
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
	current, err := router.goals.Get(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	seed := service.UpdateGoalInput{Title: current.Title, Why: current.Why, Area: current.Area, MetricType: current.MetricType, TargetValue: current.TargetValue, CurrentValue: current.CurrentValue, Unit: current.Unit, StartDate: current.StartDate, DueDate: current.DueDate, Status: current.Status, Health: current.Health}
	var input service.UpdateGoalInput
	if !router.decodeMergePatch(w, r, seed, goalPatchFields, &input) {
		return
	}
	goal, err := router.goals.Update(r.Context(), mutation, id, version, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, goal.Version)
	router.writeJSON(w, http.StatusOK, goal)
}
func (router *Router) deleteGoal(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "goalId")
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
	if err := router.goals.Delete(r.Context(), mutation, id, version); err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (router *Router) createMilestone(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	goalID, ok := router.pathUUID(w, r, "goalId")
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	var input service.CreateMilestoneInput
	if !router.decodeJSON(w, r, &input, maxResourceRequestBytes) {
		return
	}
	value, err := router.goals.CreateMilestone(r.Context(), mutation, goalID, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusCreated, value)
}
func (router *Router) listMilestones(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	goalID, ok := router.pathUUID(w, r, "goalId")
	if !ok {
		return
	}
	values, err := router.goals.ListMilestones(r.Context(), auth.Account.ID, goalID)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, map[string]any{"milestones": values})
}
func (router *Router) updateMilestone(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "milestoneId")
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
	current, err := router.goals.GetMilestone(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	seed := service.UpdateMilestoneInput{Title: current.Title, DueAt: current.DueAt, CompletedAt: current.CompletedAt, SortOrder: current.SortOrder}
	var input service.UpdateMilestoneInput
	if !router.decodeMergePatch(w, r, seed, milestonePatchFields, &input) {
		return
	}
	value, err := router.goals.UpdateMilestone(r.Context(), mutation, id, version, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) deleteMilestone(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "milestoneId")
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
	if err := router.goals.DeleteMilestone(r.Context(), mutation, id, version); err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
