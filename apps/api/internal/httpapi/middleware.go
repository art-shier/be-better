package httpapi

import (
	"context"
	"io"
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

type responseStatusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *responseStatusWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseStatusWriter) Write(payload []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(payload)
}

func (writer *responseStatusWriter) ReadFrom(reader io.Reader) (int64, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if destination, ok := writer.ResponseWriter.(io.ReaderFrom); ok {
		return destination.ReadFrom(reader)
	}
	return io.Copy(writer.ResponseWriter, reader)
}

func (writer *responseStatusWriter) Flush() {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (router *Router) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		statusWriter := &responseStatusWriter{ResponseWriter: response}
		response = statusWriter
		identifier := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if _, err := uuid.Parse(identifier); err != nil {
			identifier = uuid.NewString()
		}
		request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, identifier))
		defer func() {
			status := statusWriter.status
			if status == 0 {
				status = http.StatusOK
			}
			route := request.Pattern
			if route == "" {
				route = "unmatched"
			}
			elapsed := time.Since(started)
			router.logger.Info(
				"http request", "requestId", identifier, "method", request.Method,
				"route", route, "status", status, "duration", elapsed,
			)
			if router.metrics != nil {
				router.metrics.ObserveHTTPRequest(route, request.Method, status, elapsed)
			}
		}()
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
