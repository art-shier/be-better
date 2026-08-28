package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
)

type requestIDKey struct{}

func (router *Router) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		identifier := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if _, err := uuid.Parse(identifier); err != nil {
			identifier = uuid.NewString()
		}
		request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, identifier))
		response.Header().Set("X-Request-ID", identifier)
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Frame-Options", "DENY")

		origin := strings.TrimSuffix(strings.TrimSpace(request.Header.Get("Origin")), "/")
		if origin != "" {
			_, allowed := router.allowedOrigins[origin]
			if !allowed && !sameRequestOrigin(origin, request.Host) {
				router.writeError(response, request, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "请求来源不被允许", false, nil)
				return
			}
			if allowed {
				response.Header().Set("Access-Control-Allow-Origin", origin)
				response.Header().Set("Access-Control-Allow-Credentials", "true")
				response.Header().Set("Vary", "Origin")
				response.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID, X-Device-ID, Idempotency-Key, If-Match")
				response.Header().Set("Access-Control-Allow-Methods", "GET, PUT, PATCH, POST, DELETE, OPTIONS")
			}
		}
		if request.Method == http.MethodOptions {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(response, request)
		router.logger.Info(
			"http request", "requestId", identifier, "method", request.Method,
			"path", request.URL.Path, "duration", time.Since(started),
		)
	})
}

func (router *Router) authenticateRequest(response http.ResponseWriter, request *http.Request) (model.AuthenticatedSession, bool) {
	cookie, err := request.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		router.writeError(response, request, http.StatusUnauthorized, "AUTH_REQUIRED", "需要登录后继续", false, nil)
		return model.AuthenticatedSession{}, false
	}
	authenticated, err := router.sessions.Authenticate(request.Context(), cookie.Value)
	if err != nil {
		router.handleServiceError(response, request, err)
		return model.AuthenticatedSession{}, false
	}
	return authenticated, true
}

func (router *Router) setSessionCookie(response http.ResponseWriter, request *http.Request, token string, expires time.Time) {
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: requestIsHTTPS(request), SameSite: http.SameSiteLaxMode,
		Expires: expires.UTC(), MaxAge: int(sessionDuration.Seconds()),
	})
}

func (router *Router) expireSessionCookie(response http.ResponseWriter, request *http.Request) {
	http.SetCookie(response, &http.Cookie{
		Name: sessionCookie, Path: "/", HttpOnly: true, Secure: requestIsHTTPS(request),
		SameSite: http.SameSiteLaxMode, Expires: time.Unix(1, 0), MaxAge: -1,
	})
}

func requestID(request *http.Request) string {
	identifier, _ := request.Context().Value(requestIDKey{}).(string)
	return identifier
}

func requestIsHTTPS(request *http.Request) bool {
	return request.TLS != nil || strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")), "https")
}

func sameRequestOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme != "" && parsed.Host != "" && strings.EqualFold(parsed.Host, requestHost)
}

func requestClientIP(request *http.Request) string {
	value := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-For"), ",")[0])
	if value == "" {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err == nil {
			value = host
		} else {
			value = request.RemoteAddr
		}
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return address.Unmap().String()
	}
	return "unknown"
}
