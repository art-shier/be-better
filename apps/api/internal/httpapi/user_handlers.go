package httpapi

import (
	"net/http"

	"dayorder.local/api/internal/service"
)

func (router *Router) updateAccountProfile(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if !router.decodeJSON(response, request, &input, 32<<10) {
		return
	}
	account, err := router.accounts.UpdateDisplayName(request.Context(), authenticated.Account.ID, input.DisplayName)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, map[string]any{"user": account})
}

func (router *Router) changeAccountEmail(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		Email           string `json:"email"`
	}
	if !router.decodeJSON(response, request, &input, 32<<10) {
		return
	}
	if err := router.sessions.VerifyPassword(request.Context(), authenticated.Account.ID, input.CurrentPassword); err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	account, err := router.accounts.UpdateEmail(request.Context(), authenticated.Account, input.Email)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, map[string]any{"user": account})
}

func (router *Router) changeAccountPassword(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if !router.decodeJSON(response, request, &input, 64<<10) {
		return
	}
	result, err := router.sessions.ChangePassword(request.Context(), service.ChangePasswordInput{
		Account: authenticated.Account, CurrentPassword: input.CurrentPassword,
		NewPassword: input.Password, UserAgent: request.UserAgent(),
	})
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.setSessionCookie(response, request, result.Token, result.Session.ExpiresAt)
	response.WriteHeader(http.StatusNoContent)
}
