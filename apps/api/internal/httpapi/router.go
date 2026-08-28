package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type AccountApplication interface {
	Register(context.Context, service.RegisterInput) (model.Account, error)
	VerifyEmail(context.Context, string) (model.Account, error)
	ResendVerification(context.Context, string) error
	RequestPasswordReset(context.Context, string) error
	ResetPassword(context.Context, string, string) (model.Account, error)
	UpdateDisplayName(context.Context, uuid.UUID, string) (model.Account, error)
	UpdateEmail(context.Context, model.Account, string) (model.Account, error)
}

type SessionApplication interface {
	Login(context.Context, service.LoginInput) (service.SessionResult, error)
	Authenticate(context.Context, string) (model.AuthenticatedSession, error)
	Logout(context.Context, model.AuthenticatedSession) error
	ChangePassword(context.Context, service.ChangePasswordInput) (service.SessionResult, error)
	VerifyPassword(context.Context, uuid.UUID, string) error
}

type RouterOptions struct {
	Accounts       AccountApplication
	Sessions       SessionApplication
	AllowedOrigins []string
	Logger         *slog.Logger
	Ready          func(context.Context) error
}

type Router struct {
	accounts       AccountApplication
	sessions       SessionApplication
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	ready          func(context.Context) error
}

func NewRouter(options RouterOptions) (http.Handler, error) {
	if options.Accounts == nil || options.Sessions == nil {
		return nil, errMissingApplications
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	router := &Router{
		accounts: options.Accounts, sessions: options.Sessions,
		allowedOrigins: make(map[string]struct{}), logger: logger, ready: options.Ready,
	}
	for _, origin := range options.AllowedOrigins {
		if origin = strings.TrimSuffix(strings.TrimSpace(origin), "/"); origin != "" {
			router.allowedOrigins[origin] = struct{}{}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", router.live)
	mux.HandleFunc("GET /health/ready", router.readiness)
	mux.HandleFunc("POST /api/v1/auth/register", router.registerAccount)
	mux.HandleFunc("POST /api/v1/auth/verify-email", router.verifyEmail)
	mux.HandleFunc("POST /api/v1/auth/resend-verification", router.resendVerification)
	mux.HandleFunc("POST /api/v1/auth/password-reset/request", router.requestPasswordReset)
	mux.HandleFunc("POST /api/v1/auth/password-reset/complete", router.completePasswordReset)
	mux.HandleFunc("POST /api/v1/auth/login", router.loginAccount)
	mux.HandleFunc("POST /api/v1/auth/logout", router.logoutAccount)
	mux.HandleFunc("GET /api/v1/auth/session", router.currentAccountSession)
	mux.HandleFunc("PATCH /api/v1/users/me", router.updateAccountProfile)
	mux.HandleFunc("PUT /api/v1/users/me/email", router.changeAccountEmail)
	mux.HandleFunc("PUT /api/v1/users/me/password", router.changeAccountPassword)
	return router.middleware(mux), nil
}

func (router *Router) live(response http.ResponseWriter, request *http.Request) {
	router.writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (router *Router) readiness(response http.ResponseWriter, request *http.Request) {
	if router.ready != nil {
		if err := router.ready(request.Context()); err != nil {
			router.logger.Warn("readiness dependency failed", "requestId", requestID(request), "error", err)
			router.writeError(response, request, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "服务尚未准备好", true, nil)
			return
		}
	}
	router.writeJSON(response, http.StatusOK, map[string]string{"status": "ready"})
}
