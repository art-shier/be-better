package httpapi

import (
	"encoding/json"
	"net/http"
)

func (router *Router) getSettings(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
	if !ok {
		return
	}
	value, err := router.settings.Get(r.Context(), auth.Account.ID)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
func (router *Router) updateSettings(w http.ResponseWriter, r *http.Request) {
	auth, ok := router.authenticateRequest(w, r)
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
	if !router.requireMergePatch(w, r) {
		return
	}
	var patch json.RawMessage
	if !router.decodeJSON(w, r, &patch, maxResourceRequestBytes) {
		return
	}
	value, err := router.settings.Patch(r.Context(), mutation, version, patch)
	if err != nil {
		router.handleServiceError(w, r, err)
		return
	}
	setEntityVersion(w, value.Version)
	router.writeJSON(w, http.StatusOK, value)
}
