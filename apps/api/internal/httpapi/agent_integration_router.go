package httpapi

import (
	"errors"
	"net/http"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentgateway"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/service"
)

var (
	errAgentIntegrationEnvironment = errors.New("agent integration router is limited to development and test")
	errAgentIntegrationServices    = errors.New("agent integration runs, calendar, gateway, and devices are required")
)

type AgentIntegrationOptions struct {
	Environment config.Environment
	Runs        *service.AgentReadonlyService
	Calendar    *service.AgentCalendarReadService
	Gateway     *agentgateway.Gateway
}

type agentIntegrationRouter struct {
	*Router
	runs        *service.AgentReadonlyService
	calendar    *service.AgentCalendarReadService
	gateway     *agentgateway.Gateway
	toolTimeout time.Duration
}

func NewAgentIntegrationRouter(base RouterOptions, agent AgentIntegrationOptions) (http.Handler, error) {
	if agent.Environment != config.Development && agent.Environment != config.Test {
		return nil, errAgentIntegrationEnvironment
	}
	if agent.Runs == nil || agent.Calendar == nil || agent.Gateway == nil || base.Devices == nil {
		return nil, errAgentIntegrationServices
	}
	calendarSpec, err := agentassets.CalendarReadSpec()
	if err != nil {
		return nil, errAgentIntegrationServices
	}
	base.AgentAvailable = false
	router, mux, err := buildRouter(base)
	if err != nil {
		return nil, err
	}
	integration := &agentIntegrationRouter{
		Router: router, runs: agent.Runs, calendar: agent.Calendar, gateway: agent.Gateway,
		toolTimeout: time.Duration(calendarSpec.TimeoutMs) * time.Millisecond,
	}
	registerAgentIntegrationRoutes(mux, integration)
	return router.middleware(mux), nil
}

func registerAgentIntegrationRoutes(mux *http.ServeMux, router *agentIntegrationRouter) {
	mux.HandleFunc("POST /api/v1/agent/runs", router.createAgentReadonlyRun)
	mux.HandleFunc("GET /api/v1/agent/runs/{runId}", router.getAgentReadonlyRun)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/cancel", router.cancelAgentReadonlyRun)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/finish", router.finishAgentReadonlyRun)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/tools/calendar-read", router.readAgentCalendar)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/turns/{turnId}/stream", router.streamAgentTurn)
}

func registerDisabledAgentIntegrationRoutes(mux *http.ServeMux, router *Router) {
	mux.HandleFunc("POST /api/v1/agent/runs", router.agentUnavailable)
	mux.HandleFunc("GET /api/v1/agent/runs/{runId}", router.agentUnavailable)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/cancel", router.agentUnavailable)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/finish", router.agentUnavailable)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/tools/calendar-read", router.agentUnavailable)
	mux.HandleFunc("POST /api/v1/agent/runs/{runId}/turns/{turnId}/stream", router.agentUnavailable)
}
