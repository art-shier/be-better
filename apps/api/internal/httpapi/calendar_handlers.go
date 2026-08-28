package httpapi

import (
	"net/http"

	"dayorder.local/api/internal/service"
)

var calendarPatchFields = map[string]bool{"title": true, "startAt": true, "endAt": true, "timezone": true, "location": true, "kind": true, "sourceCalendar": true, "goalId": true, "reminders": true}

func (router *Router) createCalendarEvent(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	var input service.CalendarEventInput
	if !router.decodeJSON(w, r, &input, maxResourceRequestBytes) {
		return
	}
	value, err := router.calendar.Create(r.Context(), mutation, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Event.Version)
	router.writeJSON(w, http.StatusCreated, value)
}
func (router *Router) getCalendarEvent(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "eventId")
	if !ok {
		return
	}
	value, err := router.calendar.Get(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Event.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) listCalendarEvents(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	limit, ok := router.pageSize(w, r)
	if !ok {
		return
	}
	start, err := parseOptionalTimeQuery(r, "start")
	if err != nil {
		router.writeError(w, r, http.StatusBadRequest, "INVALID_TIME_WINDOW", "start 必须是 RFC3339 时间", false, nil)
		return
	}
	end, err := parseOptionalTimeQuery(r, "end")
	if err != nil {
		router.writeError(w, r, http.StatusBadRequest, "INVALID_TIME_WINDOW", "end 必须是 RFC3339 时间", false, nil)
		return
	}
	page, err := router.calendar.List(r.Context(), auth.Account.ID, start, end, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, page)
}
func (router *Router) updateCalendarEvent(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "eventId")
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
	current, err := router.calendar.Get(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	reminders := make([]service.ReminderInput, len(current.Reminders))
	for index, reminder := range current.Reminders {
		reminders[index] = service.ReminderInput{OffsetMinutes: reminder.OffsetMinutes, Channel: reminder.Channel}
	}
	seed := service.CalendarEventInput{Title: current.Event.Title, StartAt: current.Event.StartAt, EndAt: current.Event.EndAt, Timezone: current.Event.Timezone, Location: current.Event.Location, Kind: current.Event.Kind, SourceCalendar: current.Event.SourceCalendar, GoalID: current.Event.GoalID, Reminders: reminders}
	var input service.CalendarEventInput
	if !router.decodeMergePatch(w, r, seed, calendarPatchFields, &input) {
		return
	}
	value, err := router.calendar.Update(r.Context(), mutation, id, version, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Event.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) deleteCalendarEvent(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "eventId")
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
	if err := router.calendar.Delete(r.Context(), mutation, id, version); err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
