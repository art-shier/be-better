package httpapi

import (
	"net/http"

	"dayorder.local/api/internal/service"
)

var recordPatchFields = map[string]bool{"rawText": true, "kind": true, "occurredAt": true, "mood": true, "energy": true, "archivedAt": true, "tags": true}
var notePatchFields = map[string]bool{"title": true, "bodyMarkdown": true, "category": true, "archivedAt": true, "tags": true, "linkedEntityIds": true}
var reviewPatchFields = map[string]bool{"reviewDate": true, "wins": true, "blockers": true, "mood": true, "energy": true, "tomorrowFocus": true, "aiSummary": true}

func (router *Router) createRecord(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	var input service.RecordInput
	if !router.decodeJSON(w, r, &input, maxResourceRequestBytes) {
		return
	}
	value, err := router.content.CreateRecord(r.Context(), mutation, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusCreated, value)
}
func (router *Router) getRecord(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "recordId")
	if !ok {
		return
	}
	value, err := router.content.GetRecord(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) listRecords(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	limit, ok := router.pageSize(w, r)
	if !ok {
		return
	}
	page, err := router.content.ListRecords(r.Context(), auth.Account.ID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, page)
}
func (router *Router) updateRecord(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "recordId")
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
	current, err := router.content.GetRecord(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	tagNames := make([]string, len(current.Tags))
	for i, tag := range current.Tags {
		tagNames[i] = tag.Name
	}
	seed := service.RecordInput{RawText: current.RawText, Kind: current.Kind, OccurredAt: current.OccurredAt, Mood: current.Mood, Energy: current.Energy, ArchivedAt: current.ArchivedAt, Tags: tagNames}
	var input service.RecordInput
	if !router.decodeMergePatch(w, r, seed, recordPatchFields, &input) {
		return
	}
	value, err := router.content.UpdateRecord(r.Context(), mutation, id, version, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) deleteRecord(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "recordId")
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
	if err := router.content.DeleteRecord(r.Context(), mutation, id, version); err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (router *Router) createNote(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	var input service.NoteInput
	if !router.decodeJSON(w, r, &input, maxResourceRequestBytes) {
		return
	}
	value, err := router.content.CreateNote(r.Context(), mutation, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusCreated, value)
}
func (router *Router) getNote(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "noteId")
	if !ok {
		return
	}
	value, err := router.content.GetNote(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) listNotes(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	limit, ok := router.pageSize(w, r)
	if !ok {
		return
	}
	page, err := router.content.ListNotes(r.Context(), auth.Account.ID, r.URL.Query().Get("q"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, page)
}
func (router *Router) updateNote(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "noteId")
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
	current, err := router.content.GetNote(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	tagNames := make([]string, len(current.Tags))
	for i, tag := range current.Tags {
		tagNames[i] = tag.Name
	}
	seed := service.NoteInput{Title: current.Title, BodyMarkdown: current.BodyMarkdown, Category: current.Category, ArchivedAt: current.ArchivedAt, Tags: tagNames, LinkedEntityIDs: current.LinkedEntityIDs}
	var input service.NoteInput
	if !router.decodeMergePatch(w, r, seed, notePatchFields, &input) {
		return
	}
	value, err := router.content.UpdateNote(r.Context(), mutation, id, version, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) deleteNote(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "noteId")
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
	if err := router.content.DeleteNote(r.Context(), mutation, id, version); err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (router *Router) createReview(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	mutation, ok := router.mutationContext(w, r, auth.Account.ID)
	if !ok {
		return
	}
	var input service.ReviewInput
	if !router.decodeJSON(w, r, &input, maxResourceRequestBytes) {
		return
	}
	value, err := router.content.CreateReview(r.Context(), mutation, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusCreated, value)
}
func (router *Router) getReview(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "reviewId")
	if !ok {
		return
	}
	value, err := router.content.GetReview(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) listReviews(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	limit, ok := router.pageSize(w, r)
	if !ok {
		return
	}
	page, err := router.content.ListReviews(r.Context(), auth.Account.ID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, page)
}
func (router *Router) updateReview(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "reviewId")
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
	current, err := router.content.GetReview(r.Context(), auth.Account.ID, id)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	seed := service.ReviewInput{ReviewDate: current.ReviewDate, Wins: current.Wins, Blockers: current.Blockers, Mood: current.Mood, Energy: current.Energy, TomorrowFocus: current.TomorrowFocus, AISummary: current.AISummary}
	var input service.ReviewInput
	if !router.decodeMergePatch(w, r, seed, reviewPatchFields, &input) {
		return
	}
	value, err := router.content.UpdateReview(r.Context(), mutation, id, version, input)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) deleteReview(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	id, ok := router.pathUUID(w, r, "reviewId")
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
	if err := router.content.DeleteReview(r.Context(), mutation, id, version); err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (router *Router) listTags(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	values, err := router.content.ListTags(r.Context(), auth.Account.ID)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	router.writeJSON(w, http.StatusOK, map[string]any{"tags": values})
}
