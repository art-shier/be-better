package httpapi

import (
	"errors"
	"net/http"
	"time"

	"dayorder.local/api/internal/service"
)

func (router *Router) registerAccount(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
	}
	if !router.decodeJSON(response, request, &input, 64<<10) {
		return
	}
	account, err := router.accounts.Register(request.Context(), service.RegisterInput{
		Email: input.Email, DisplayName: input.DisplayName, Password: input.Password,
	})
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusCreated, map[string]any{
		"user": account, "verificationRequired": true,
	})
}

func (router *Router) verifyEmail(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if !router.decodeJSON(response, request, &input, 16<<10) {
		return
	}
	account, err := router.accounts.VerifyEmail(request.Context(), input.Token)
	if err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusOK, map[string]any{"user": account})
}

func (router *Router) resendVerification(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !router.decodeJSON(response, request, &input, 16<<10) {
		return
	}
	if err := router.accounts.ResendVerification(request.Context(), input.Email); err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (router *Router) requestPasswordReset(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !router.decodeJSON(response, request, &input, 16<<10) {
		return
	}
	if err := router.accounts.RequestPasswordReset(request.Context(), input.Email); err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.writeJSON(response, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (router *Router) completePasswordReset(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !router.decodeJSON(response, request, &input, 32<<10) {
		return
	}
	if _, err := router.accounts.ResetPassword(request.Context(), input.Token, input.Password); err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (router *Router) loginAccount(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !router.decodeJSON(response, request, &input, 64<<10) {
		return
	}
	result, err := router.sessions.Login(request.Context(), service.LoginInput{
		Email: input.Email, Password: input.Password, IP: requestClientIP(request), UserAgent: request.UserAgent(),
	})
	if err != nil {
		var rateLimit *service.RateLimitError
		if errors.As(err, &rateLimit) {
			retrySeconds := max(int(time.Until(rateLimit.RetryAt).Seconds()), 1)
			response.Header().Set("Retry-After", formatInteger(retrySeconds))
			router.writeError(response, request, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "尝试次数过多，请稍后再试", true, nil)
			return
		}
		router.handleServiceError(response, request, err)
		return
	}
	router.setSessionCookie(response, request, result.Token, result.Session.ExpiresAt)
	router.writeJSON(response, http.StatusOK, map[string]any{
		"user": result.Account, "expiresAt": result.Session.ExpiresAt,
	})
}

func (router *Router) logoutAccount(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	if err := router.sessions.Logout(request.Context(), authenticated); err != nil {
		router.handleServiceError(response, request, err)
		return
	}
	router.expireSessionCookie(response, request)
	response.WriteHeader(http.StatusNoContent)
}

func (router *Router) currentAccountSession(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := router.authenticateRequest(response, request)
	if !ok {
		return
	}
	router.writeJSON(response, http.StatusOK, map[string]any{
		"user": authenticated.Account, "expiresAt": authenticated.Session.ExpiresAt,
	})
}
