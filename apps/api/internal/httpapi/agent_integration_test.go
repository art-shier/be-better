package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentgateway"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

func TestAgentIntegrationRouterRejectsProduction(t *testing.T) {
	_, err := NewAgentIntegrationRouter(RouterOptions{}, AgentIntegrationOptions{Environment: config.Production})
	if err == nil {
		t.Fatal("production agent integration router accepted")
	}
}

func TestAgentIntegrationRouterRequiresSession(t *testing.T) {
	handler := newAgentIntegrationStubRouter(t, &stubSessionApplication{}, &stubDeviceApplication{})
	runID := uuid.NewString()
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/agent/runs"},
		{http.MethodGet, "/api/v1/agent/runs/" + runID},
		{http.MethodPost, "/api/v1/agent/runs/" + runID + "/cancel"},
		{http.MethodPost, "/api/v1/agent/runs/" + runID + "/finish"},
		{http.MethodPost, "/api/v1/agent/runs/" + runID + "/tools/calendar-read"},
		{http.MethodPost, "/api/v1/agent/runs/" + runID + "/turns/turn-1/stream"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, "https://dayorder.example"+route.path, strings.NewReader(`{}`))
			if route.method == http.MethodPost {
				request.Header.Set("Origin", "https://dayorder.example")
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertAgentIntegrationError(t, response, http.StatusUnauthorized, "AUTH_REQUIRED")
		})
	}
}

func TestAgentIntegrationRouterRejectsUntrustedOrigin(t *testing.T) {
	handler := newAgentIntegrationStubRouter(t, &stubSessionApplication{}, &stubDeviceApplication{})
	request := httptest.NewRequest(http.MethodPost, "https://dayorder.example/api/v1/agent/runs", bytes.NewBufferString(`{}`))
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertAgentIntegrationError(t, response, http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
}

func TestProductionRouterExplicitlyDisablesReadonlyAgentRoute(t *testing.T) {
	handler := newTestRouter(t, &stubAccountApplication{}, &stubSessionApplication{}, nil)
	request := httptest.NewRequest(http.MethodPost, "https://dayorder.example/api/v1/agent/runs", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertAgentIntegrationError(t, response, http.StatusServiceUnavailable, "AGENT_NOT_AVAILABLE")
}

func TestAgentIntegrationPostRequiresTrustedOriginAndJSON(t *testing.T) {
	userID, deviceID := uuid.New(), uuid.New()
	sessions := authenticatedAgentSession(userID)
	handler := newAgentIntegrationStubRouter(t, sessions, activeAgentDevice(userID, deviceID))

	request := agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs", `{}`, "", deviceID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertAgentIntegrationError(t, response, http.StatusForbidden, "ORIGIN_REQUIRED")

	request = agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs", `{}`, "https://dayorder.example", deviceID)
	request.Header.Set("Content-Type", "text/plain")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertAgentIntegrationError(t, response, http.StatusUnsupportedMediaType, "JSON_REQUIRED")

	request = agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs", `{}`, "http://dayorder.example", deviceID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertAgentIntegrationError(t, response, http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
}

func TestAgentIntegrationToolRequiresOwnedActiveDevice(t *testing.T) {
	userID, submittedDeviceID := uuid.New(), uuid.New()
	handler := newAgentIntegrationStubRouter(t, authenticatedAgentSession(userID), activeAgentDevice(userID, uuid.New()))
	body := `{"callId":"calendar-1","input":{"start":"2026-09-05T00:00:00Z","end":"2026-09-06T00:00:00Z"}}`
	request := agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs/"+uuid.NewString()+"/tools/calendar-read", body, "https://dayorder.example", submittedDeviceID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertAgentIntegrationError(t, response, http.StatusPreconditionRequired, "DEVICE_REGISTRATION_REQUIRED")

	request = agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs/"+uuid.NewString()+"/tools/calendar-read", body, "https://dayorder.example", uuid.Nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertAgentIntegrationError(t, response, http.StatusPreconditionRequired, "DEVICE_ID_REQUIRED")
}

func TestAgentIntegrationRejectsInvalidIDsAndBoundedJSONBeforeServices(t *testing.T) {
	userID, deviceID := uuid.New(), uuid.New()
	handler := newAgentIntegrationStubRouter(t, authenticatedAgentSession(userID), activeAgentDevice(userID, deviceID))

	tests := []struct {
		name   string
		path   string
		body   string
		status int
		code   string
	}{
		{name: "invalid run UUID", path: "/api/v1/agent/runs/not-a-uuid/tools/calendar-read", body: `{}`, status: http.StatusBadRequest, code: "INVALID_RESOURCE_ID"},
		{name: "long call ID", path: "/api/v1/agent/runs/" + uuid.NewString() + "/tools/calendar-read", body: `{"callId":"` + strings.Repeat("x", 129) + `","input":{"start":"2026-09-05T00:00:00Z","end":"2026-09-06T00:00:00Z"}}`, status: http.StatusUnprocessableEntity, code: "VALIDATION_FAILED"},
		{name: "oversized", path: "/api/v1/agent/runs/" + uuid.NewString() + "/turns/turn-1/stream", body: `{"padding":"` + strings.Repeat("x", 256<<10) + `"}`, status: http.StatusRequestEntityTooLarge, code: "REQUEST_TOO_LARGE"},
		{name: "too deep", path: "/api/v1/agent/runs/" + uuid.NewString() + "/turns/turn-1/stream", body: strings.Repeat(`{"x":`, 17) + `null` + strings.Repeat(`}`, 17), status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "long turn ID", path: "/api/v1/agent/runs/" + uuid.NewString() + "/turns/" + strings.Repeat("t", 129) + "/stream", body: `{}`, status: http.StatusUnprocessableEntity, code: "VALIDATION_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := agentIntegrationRequest(http.MethodPost, test.path, test.body, "https://dayorder.example", deviceID)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertAgentIntegrationError(t, response, test.status, test.code)
		})
	}
}

func TestAgentIntegrationCancelRejectsOversizedBodyBeforeService(t *testing.T) {
	userID, deviceID := uuid.New(), uuid.New()
	handler := newAgentIntegrationStubRouter(t, authenticatedAgentSession(userID), activeAgentDevice(userID, deviceID))
	request := agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs/"+uuid.NewString()+"/cancel", strings.Repeat("x", maxAgentRequestBytes+1), "https://dayorder.example", deviceID)
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertAgentIntegrationError(t, response, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE")
}

func TestAgentIntegrationAssemblyKeepsLegacyAgentRoutesDisabled(t *testing.T) {
	handler, err := NewAgentIntegrationRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: &stubSessionApplication{}, Devices: &stubDeviceApplication{},
		Agents: &stubAgentApplication{}, AgentAvailable: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, AgentIntegrationOptions{Environment: config.Test, Runs: &service.AgentReadonlyService{}, Calendar: &service.AgentCalendarReadService{}, Gateway: &agentgateway.Gateway{}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://dayorder.example/api/v1/agent-runs", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertAgentIntegrationError(t, response, http.StatusServiceUnavailable, "AGENT_NOT_AVAILABLE")
}

func TestAgentIntegrationHTTPRealServices(t *testing.T) {
	fixture := agenttest.Open(t)
	services := agenttest.NewServices(t, fixture, config.DatabaseRoleAPI)
	calendarRead, err := service.NewAgentCalendarReadService(services.Runs, services.Calendar)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = services.Calendar.Create(ctx, agentIntegrationMutation(fixture.UserA, fixture.DeviceA), service.CalendarEventInput{
		Title: "Design review", StartAt: time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC),
		EndAt: time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC), Timezone: "UTC", Kind: "fixed",
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := agentIntegrationTools(t)
	completedGateway := newAgentIntegrationGateway(t, services.Runs, agentIntegrationSequenceAdapter{}, tools)
	handler := newAgentIntegrationRealRouter(t, fixture.UserA, fixture.DeviceA, services.Runs, calendarRead, completedGateway)
	server := httptest.NewServer(handler)
	defer server.Close()

	created := createAgentIntegrationRunHTTP(t, server.Client(), server.URL, fixture.UserA, fixture.DeviceA)
	runID := uuid.MustParse(string(created.RunID))

	otherHandler := newAgentIntegrationRealRouter(t, fixture.UserB, fixture.DeviceB, services.Runs, calendarRead, completedGateway)
	otherServer := httptest.NewServer(otherHandler)
	request := agentIntegrationNetworkRequest(t, http.MethodGet, otherServer.URL+"/api/v1/agent/runs/"+runID.String(), "", fixture.DeviceB)
	response, err := otherServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentIntegrationNetworkError(t, response, http.StatusNotFound, "RESOURCE_NOT_FOUND")

	toolBody := `{"callId":"calendar-1","input":{"start":"2026-09-05T00:00:00Z","end":"2026-09-06T00:00:00Z","limit":20}}`
	request = agentIntegrationNetworkRequest(t, http.MethodPost, otherServer.URL+"/api/v1/agent/runs/"+runID.String()+"/tools/calendar-read", toolBody, fixture.DeviceB)
	response, err = otherServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentIntegrationNetworkError(t, response, http.StatusNotFound, "RESOURCE_NOT_FOUND")
	otherServer.Close()

	request = agentIntegrationNetworkRequest(t, http.MethodPost, server.URL+"/api/v1/agent/runs/"+runID.String()+"/tools/calendar-read", toolBody, fixture.DeviceA)
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("calendar status=%d body=%s", response.StatusCode, body)
	}
	var toolResult agentprotocol.ToolResult
	if err = json.NewDecoder(response.Body).Decode(&toolResult); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !toolResult.Ok {
		t.Fatalf("calendar result=%#v", toolResult)
	}

	turnRequest := agentIntegrationTurnRequest(runID, "turn-prepare-failure", nil)
	request = agentIntegrationJSONNetworkRequest(t, server.URL+"/api/v1/agent/runs/"+runID.String()+"/turns/turn-prepare-failure/stream", turnRequest, fixture.DeviceA)
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if contentType := response.Header.Get("Content-Type"); response.StatusCode != http.StatusUnprocessableEntity || !strings.HasPrefix(contentType, "application/json") {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("prepare failure status=%d contentType=%q body=%s", response.StatusCode, contentType, body)
	}
	response.Body.Close()

	turnRequest = agentIntegrationTurnRequest(runID, "turn-complete", tools)
	request = agentIntegrationJSONNetworkRequest(t, server.URL+"/api/v1/agent/runs/"+runID.String()+"/turns/turn-complete/stream", turnRequest, fixture.DeviceA)
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	frames := readAgentIntegrationFrames(t, response)
	if len(frames) != 2 || frames[0].Sequence != 1 || frames[1].Sequence != 2 || frames[1].Event.Type != agentprotocol.ProviderEventTypeCompleted {
		t.Fatalf("SSE frames=%#v", frames)
	}

	current, err := services.Runs.Get(ctx, fixture.UserA, runID)
	if err != nil {
		t.Fatal(err)
	}
	finishBody := `{"phase":"completed","summary":"Reviewed calendar","steps":[]}`
	request = agentIntegrationNetworkRequest(t, http.MethodPost, server.URL+"/api/v1/agent/runs/"+runID.String()+"/finish", finishBody, fixture.DeviceA)
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", fmt.Sprintf(`"%d"`, current.Version))
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("finish status=%d body=%s", response.StatusCode, body)
	}
	response.Body.Close()

	gated := newAgentIntegrationGatedAdapter()
	gatedGateway := newAgentIntegrationGateway(t, services.Runs, gated, tools)
	gatedHandler := newAgentIntegrationRealRouter(t, fixture.UserA, fixture.DeviceA, services.Runs, calendarRead, gatedGateway)
	gatedServer := httptest.NewServer(gatedHandler)
	defer gatedServer.Close()
	gatedRun := createAgentIntegrationRunHTTP(t, gatedServer.Client(), gatedServer.URL, fixture.UserA, fixture.DeviceA)
	gatedRunID := uuid.MustParse(string(gatedRun.RunID))

	streamContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	firstRequest := agentIntegrationTurnRequest(gatedRunID, "turn-active", tools)
	request = agentIntegrationJSONNetworkRequest(t, gatedServer.URL+"/api/v1/agent/runs/"+gatedRunID.String()+"/turns/turn-active/stream", firstRequest, fixture.DeviceA)
	request = request.WithContext(streamContext)
	firstResponse, err := gatedServer.Client().Do(request)
	if err != nil {
		t.Fatal("first SSE frame was not flushed before the adapter completed:", err)
	}
	reader := bufio.NewReader(firstResponse.Body)
	firstFrame, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(firstFrame, "data: ") {
		firstResponse.Body.Close()
		t.Fatalf("first flushed frame=%q error=%v", firstFrame, err)
	}

	conflictRequest := agentIntegrationTurnRequest(gatedRunID, "turn-conflict", tools)
	request = agentIntegrationJSONNetworkRequest(t, gatedServer.URL+"/api/v1/agent/runs/"+gatedRunID.String()+"/turns/turn-conflict/stream", conflictRequest, fixture.DeviceA)
	response, err = gatedServer.Client().Do(request)
	if err != nil {
		firstResponse.Body.Close()
		t.Fatal(err)
	}
	assertAgentIntegrationNetworkError(t, response, http.StatusConflict, "ENTITY_VERSION_CONFLICT")

	firstResponse.Body.Close()
	select {
	case <-gated.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("closing the SSE response did not cancel the active provider turn")
	}
	waitAgentIntegrationRunStatus(t, services.Runs, fixture.UserA, gatedRunID, agentprotocol.ReadonlyRunViewStatusFailed)

	cancelAdapter := &agentIntegrationCancelOrderAdapter{
		runs: services.Runs, userID: fixture.UserA, observed: make(chan agentIntegrationCancelObservation, 1),
	}
	cancelGateway := newAgentIntegrationGateway(t, services.Runs, cancelAdapter, tools)
	cancelHandler := newAgentIntegrationRealRouter(t, fixture.UserA, fixture.DeviceA, services.Runs, calendarRead, cancelGateway)
	cancelServer := httptest.NewServer(cancelHandler)
	defer cancelServer.Close()
	cancelRun := createAgentIntegrationRunHTTP(t, cancelServer.Client(), cancelServer.URL, fixture.UserA, fixture.DeviceA)
	cancelRunID := uuid.MustParse(string(cancelRun.RunID))
	cancelAdapter.runID = cancelRunID

	activeRequest := agentIntegrationTurnRequest(cancelRunID, "turn-cancel", tools)
	request = agentIntegrationJSONNetworkRequest(t, cancelServer.URL+"/api/v1/agent/runs/"+cancelRunID.String()+"/turns/turn-cancel/stream", activeRequest, fixture.DeviceA)
	activeResponse, err := cancelServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	activeReader := bufio.NewReader(activeResponse.Body)
	if firstLine, readErr := activeReader.ReadString('\n'); readErr != nil || !strings.HasPrefix(firstLine, "data: ") {
		activeResponse.Body.Close()
		t.Fatalf("active turn first frame=%q error=%v", firstLine, readErr)
	}
	cancelCurrent, err := services.Runs.Get(context.Background(), fixture.UserA, cancelRunID)
	if err != nil {
		activeResponse.Body.Close()
		t.Fatal(err)
	}

	request = agentIntegrationNetworkRequest(t, http.MethodPost, cancelServer.URL+"/api/v1/agent/runs/"+cancelRunID.String()+"/cancel", `{}`, fixture.DeviceA)
	request.Header.Set("Idempotency-Key", uuid.NewString())
	request.Header.Set("If-Match", fmt.Sprintf(`"%d"`, cancelCurrent.Version))
	response, err = cancelServer.Client().Do(request)
	if err != nil {
		activeResponse.Body.Close()
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		activeResponse.Body.Close()
		t.Fatalf("cancel status=%d body=%s", response.StatusCode, body)
	}
	response.Body.Close()
	select {
	case observation := <-cancelAdapter.observed:
		if observation.err != nil || observation.status != agentprotocol.ReadonlyRunViewStatusStopped {
			t.Fatalf("provider observed cancellation before persistence: status=%s error=%v", observation.status, observation.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active provider did not observe explicit cancellation")
	}
	_, _ = io.ReadAll(activeReader)
	activeResponse.Body.Close()

	background, err := services.Runs.Create(context.Background(), agentIntegrationMutation(fixture.UserA, fixture.DeviceA), agentIntegrationRunStart(agentprotocol.ExecutionModeBackground))
	if err != nil {
		t.Fatal(err)
	}
	backgroundID := uuid.MustParse(string(background.RunID))
	wrongModeRequest := agentIntegrationTurnRequest(backgroundID, "turn-wrong-mode", tools)
	request = agentIntegrationJSONNetworkRequest(t, server.URL+"/api/v1/agent/runs/"+backgroundID.String()+"/turns/turn-wrong-mode/stream", wrongModeRequest, fixture.DeviceA)
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if contentType := response.Header.Get("Content-Type"); response.StatusCode != http.StatusConflict || !strings.HasPrefix(contentType, "application/json") {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("wrong mode status=%d contentType=%q body=%s", response.StatusCode, contentType, body)
	}
	response.Body.Close()
	if _, err = services.Runs.Cancel(context.Background(), agentIntegrationMutation(fixture.UserA, fixture.DeviceA), backgroundID, int64(background.Version)); err != nil {
		t.Fatal(err)
	}

	failingGateway := newAgentIntegrationGateway(t, services.Runs, agentIntegrationFailingAdapter{}, tools)
	failingHandler := newAgentIntegrationRealRouter(t, fixture.UserA, fixture.DeviceA, services.Runs, calendarRead, failingGateway)
	failingServer := httptest.NewServer(failingHandler)
	defer failingServer.Close()
	failingRun := createAgentIntegrationRunHTTP(t, failingServer.Client(), failingServer.URL, fixture.UserA, fixture.DeviceA)
	failingRunID := uuid.MustParse(string(failingRun.RunID))
	failingTurn := agentIntegrationTurnRequest(failingRunID, "turn-failing", tools)
	request = agentIntegrationJSONNetworkRequest(t, failingServer.URL+"/api/v1/agent/runs/"+failingRunID.String()+"/turns/turn-failing/stream", failingTurn, fixture.DeviceA)
	response, err = failingServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	frames = readAgentIntegrationFrames(t, response)
	if len(frames) != 2 || frames[0].Sequence != 1 || frames[1].Sequence != 2 || frames[1].Event.Type != agentprotocol.ProviderEventTypeError || frames[1].Event.Error == nil || frames[1].Event.Error.Code != agentprotocol.ErrorCodeProtocolIncompatible {
		t.Fatalf("failing SSE frames=%#v", frames)
	}
	encodedFrames, _ := json.Marshal(frames)
	if strings.Contains(string(encodedFrames), "secret provider body") {
		t.Fatalf("raw provider error leaked in SSE: %s", encodedFrames)
	}
	waitAgentIntegrationRunStatus(t, services.Runs, fixture.UserA, failingRunID, agentprotocol.ReadonlyRunViewStatusFailed)

	flushGateway := newAgentIntegrationGateway(t, services.Runs, agentIntegrationSequenceAdapter{}, tools)
	flushHandler := newAgentIntegrationRealRouter(t, fixture.UserA, fixture.DeviceA, services.Runs, calendarRead, flushGateway)
	flushRun, err := services.Runs.Create(context.Background(), agentIntegrationMutation(fixture.UserA, fixture.DeviceA), agentIntegrationRunStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	flushRunID := uuid.MustParse(string(flushRun.RunID))
	flushTurn := agentIntegrationTurnRequest(flushRunID, "turn-flush-failure", tools)
	rawFlushTurn, _ := json.Marshal(flushTurn)
	flushRequest := agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs/"+flushRunID.String()+"/turns/turn-flush-failure/stream", string(rawFlushTurn), "https://dayorder.example", fixture.DeviceA)
	flushWriter := &agentIntegrationSettledFlushFailureWriter{
		header: make(http.Header), runs: services.Runs, userID: fixture.UserA, runID: flushRunID,
	}

	flushHandler.ServeHTTP(flushWriter, flushRequest)

	if !flushWriter.observedSettled {
		t.Fatal("flush did not fail after Gateway usage settlement")
	}
	flushView, err := services.Runs.Get(context.Background(), fixture.UserA, flushRunID)
	if err != nil {
		t.Fatal(err)
	}
	if flushView.Status != agentprotocol.ReadonlyRunViewStatusFailed || flushView.Error == nil || flushView.Error.Code != agentprotocol.ErrorCodeInternalError {
		t.Fatalf("post-settlement flush failure left run=%#v", flushView)
	}

	unknownGateway := newAgentIntegrationGateway(t, services.Runs, &agentIntegrationUnknownRetryAdapter{}, tools)
	unknownHandler := newAgentIntegrationRealRouter(t, fixture.UserA, fixture.DeviceA, services.Runs, calendarRead, unknownGateway)
	unknownRun, err := services.Runs.Create(context.Background(), agentIntegrationMutation(fixture.UserA, fixture.DeviceA), agentIntegrationRunStart(agentprotocol.ExecutionModeForeground))
	if err != nil {
		t.Fatal(err)
	}
	unknownRunID := uuid.MustParse(string(unknownRun.RunID))
	unknownTurn := agentIntegrationTurnRequest(unknownRunID, "turn-unknown-retry-flush", tools)
	rawUnknownTurn, _ := json.Marshal(unknownTurn)
	unknownRequest := agentIntegrationRequest(http.MethodPost, "/api/v1/agent/runs/"+unknownRunID.String()+"/turns/turn-unknown-retry-flush/stream", string(rawUnknownTurn), "https://dayorder.example", fixture.DeviceA)
	unknownWriter := &agentIntegrationSettledFlushFailureWriter{
		header: make(http.Header), runs: services.Runs, userID: fixture.UserA, runID: unknownRunID,
	}

	unknownHandler.ServeHTTP(unknownWriter, unknownRequest)

	if !unknownWriter.observedSettled {
		t.Fatal("unknown-retry flush did not fail after final usage settlement")
	}
	unknownView, err := services.Runs.Get(context.Background(), fixture.UserA, unknownRunID)
	if err != nil {
		t.Fatal(err)
	}
	if unknownView.UsageComplete || unknownView.Status != agentprotocol.ReadonlyRunViewStatusFailed || unknownView.Error == nil || unknownView.Error.Code != agentprotocol.ErrorCodeInternalError {
		t.Fatalf("unknown-retry post-settlement flush failure left run=%#v", unknownView)
	}
}

func newAgentIntegrationStubRouter(t testing.TB, sessions SessionApplication, devices DeviceApplication) http.Handler {
	t.Helper()
	handler, err := NewAgentIntegrationRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: sessions, Devices: devices,
		AllowedOrigins: []string{"https://dayorder.example"},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, AgentIntegrationOptions{
		Environment: config.Test,
		Runs:        &service.AgentReadonlyService{},
		Calendar:    &service.AgentCalendarReadService{},
		Gateway:     &agentgateway.Gateway{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func assertAgentIntegrationError(t testing.TB, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code || envelope.Error.RequestID == "" {
		t.Fatalf("error = %#v", envelope.Error)
	}
}

func activeAgentDevice(userID, deviceID uuid.UUID) *stubDeviceApplication {
	return &stubDeviceApplication{registration: service.DeviceRegistration{Device: model.UserDevice{ID: deviceID, UserID: userID}}}
}

func authenticatedAgentSession(userID uuid.UUID) *stubSessionApplication {
	return &stubSessionApplication{authenticated: model.AuthenticatedSession{Account: model.Account{ID: userID, Status: model.AccountActive}}}
}

func agentIntegrationRequest(method, path, body, origin string, deviceID uuid.UUID) *http.Request {
	request := httptest.NewRequest(method, "https://dayorder.example"+path, bytes.NewBufferString(body))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session-token"})
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	if deviceID != uuid.Nil {
		request.Header.Set("X-Device-ID", deviceID.String())
	}
	return request
}

type agentIntegrationSequenceAdapter struct{}

func (agentIntegrationSequenceAdapter) Stream(_ context.Context, _ agentprotocol.ModelTurnRequest, _ agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		text := "first chunk"
		if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil) {
			return
		}
		reason := agentprotocol.ProviderEventStopReasonEndTurn
		yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: &reason, Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}, nil)
	}
}

type agentIntegrationFailingAdapter struct{}

func (agentIntegrationFailingAdapter) Stream(_ context.Context, _ agentprotocol.ModelTurnRequest, _ agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		text := "published before failure"
		if yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil) {
			yield(agentprotocol.ProviderEvent{}, errors.New("secret provider body"))
		}
	}
}

type agentIntegrationUnknownRetryAdapter struct {
	mu    sync.Mutex
	count int
}

func (adapter *agentIntegrationUnknownRetryAdapter) Stream(_ context.Context, _ agentprotocol.ModelTurnRequest, _ agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	adapter.mu.Lock()
	adapter.count++
	attempt := adapter.count
	adapter.mu.Unlock()
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		if attempt == 1 {
			yield(agentprotocol.ProviderEvent{}, &agentprovider.ProviderError{Code: agentprotocol.ErrorCodeProviderUnavailable, Retryable: true})
			return
		}
		text := "retry completed with prior unknown usage"
		if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil) {
			return
		}
		reason := agentprotocol.ProviderEventStopReasonEndTurn
		yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeCompleted, StopReason: &reason, Usage: &agentprotocol.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}, nil)
	}
}

type agentIntegrationGatedAdapter struct {
	cancelled chan struct{}
	once      sync.Once
}

type agentIntegrationCancelObservation struct {
	status agentprotocol.ReadonlyRunViewStatus
	err    error
}

type agentIntegrationCancelOrderAdapter struct {
	runs     *service.AgentReadonlyService
	userID   uuid.UUID
	runID    uuid.UUID
	observed chan agentIntegrationCancelObservation
}

type agentIntegrationSettledFlushFailureWriter struct {
	header          http.Header
	runs            *service.AgentReadonlyService
	userID          uuid.UUID
	runID           uuid.UUID
	status          int
	observedSettled bool
}

func (writer *agentIntegrationSettledFlushFailureWriter) Header() http.Header { return writer.header }
func (writer *agentIntegrationSettledFlushFailureWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}
func (writer *agentIntegrationSettledFlushFailureWriter) Write(payload []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return len(payload), nil
}
func (writer *agentIntegrationSettledFlushFailureWriter) FlushError() error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err := writer.runs.Get(context.Background(), writer.userID, writer.runID)
		if err == nil && view.Usage.TotalTokens == 3 {
			writer.observedSettled = true
			// Gateway settles and authorizes success before relaying completed to
			// HTTP. Keep Flush blocked long enough for that release to complete.
			time.Sleep(100 * time.Millisecond)
			return errors.New("sentinel post-settlement flush failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("gateway did not settle before flush timeout")
}

func (adapter *agentIntegrationCancelOrderAdapter) Stream(ctx context.Context, _ agentprotocol.ModelTurnRequest, _ agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		text := "waiting for explicit cancellation"
		if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil) {
			return
		}
		<-ctx.Done()
		view, err := adapter.runs.Get(context.Background(), adapter.userID, adapter.runID)
		adapter.observed <- agentIntegrationCancelObservation{status: view.Status, err: err}
		yield(agentprotocol.ProviderEvent{}, ctx.Err())
	}
}

func newAgentIntegrationGatedAdapter() *agentIntegrationGatedAdapter {
	return &agentIntegrationGatedAdapter{cancelled: make(chan struct{})}
}

func (adapter *agentIntegrationGatedAdapter) Stream(ctx context.Context, _ agentprotocol.ModelTurnRequest, _ agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		text := "flushed before terminal"
		if !yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil) {
			return
		}
		<-ctx.Done()
		adapter.once.Do(func() { close(adapter.cancelled) })
		yield(agentprotocol.ProviderEvent{}, ctx.Err())
	}
}

func agentIntegrationTools(t testing.TB) []agentprotocol.ToolSpec {
	t.Helper()
	meta := agentskill.MetaBindings(nil, agentprotocol.CapabilitySnapshot{}, nil, agenttool.Policy{})
	calendar, err := agentassets.CalendarReadSpec()
	if err != nil {
		t.Fatal(err)
	}
	return []agentprotocol.ToolSpec{meta[0].Spec(), meta[1].Spec(), calendar}
}

func newAgentIntegrationGateway(t testing.TB, runs *service.AgentReadonlyService, adapter agentprovider.Adapter, tools []agentprotocol.ToolSpec) *agentgateway.Gateway {
	t.Helper()
	gateway, err := agentgateway.New(agentgateway.Config{
		Runs: runs, Profiles: []agentgateway.Profile{{ID: "readonly-default", Model: "fixture-model", Adapter: adapter}}, Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func newAgentIntegrationRealRouter(t testing.TB, userID, deviceID uuid.UUID, runs *service.AgentReadonlyService, calendar *service.AgentCalendarReadService, gateway *agentgateway.Gateway) http.Handler {
	t.Helper()
	handler, err := NewAgentIntegrationRouter(RouterOptions{
		Accounts: &stubAccountApplication{}, Sessions: authenticatedAgentSession(userID), Devices: activeAgentDevice(userID, deviceID),
		AllowedOrigins: []string{"https://dayorder.example"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, AgentIntegrationOptions{Environment: config.Test, Runs: runs, Calendar: calendar, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func agentIntegrationMutation(userID, deviceID uuid.UUID) service.MutationContext {
	return service.MutationContext{UserID: userID, DeviceID: deviceID, MutationID: uuid.New(), RequestID: uuid.New()}
}

func createAgentIntegrationRunHTTP(t testing.TB, client *http.Client, baseURL string, _ uuid.UUID, deviceID uuid.UUID) agentprotocol.ReadonlyRunView {
	t.Helper()
	input := agentIntegrationRunStart(agentprotocol.ExecutionModeForeground)
	raw, _ := json.Marshal(input)
	request := agentIntegrationNetworkRequest(t, http.MethodPost, baseURL+"/api/v1/agent/runs", string(raw), deviceID)
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create run status=%d body=%s", response.StatusCode, body)
	}
	var view agentprotocol.ReadonlyRunView
	if err = json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func agentIntegrationRunStart(mode agentprotocol.ExecutionMode) agentprotocol.ReadonlyRunStart {
	from, to := "2026-09-05T00:00:00Z", "2026-09-06T00:00:00Z"
	return agentprotocol.ReadonlyRunStart{
		ExecutionMode: mode, Intent: "Review my calendar", ModelProfile: "readonly-default", Timezone: "UTC",
		Scope: agentprotocol.AgentScope{Domains: []string{"calendar"}, From: &from, To: &to},
	}
}

func agentIntegrationNetworkRequest(t testing.TB, method, target, body string, deviceID uuid.UUID) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session-token"})
	request.Header.Set("Origin", "https://dayorder.example")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	if deviceID != uuid.Nil {
		request.Header.Set("X-Device-ID", deviceID.String())
	}
	return request
}

func agentIntegrationJSONNetworkRequest(t testing.TB, target string, value any, deviceID uuid.UUID) *http.Request {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return agentIntegrationNetworkRequest(t, http.MethodPost, target, string(raw), deviceID)
}

func agentIntegrationTurnRequest(runID uuid.UUID, turnID string, tools []agentprotocol.ToolSpec) agentprotocol.ModelTurnRequest {
	text := "Show my calendar."
	return agentprotocol.ModelTurnRequest{
		ProtocolVersion: "2.0", RunID: runID.String(), TurnID: turnID, ModelProfile: "readonly-default",
		Messages: []agentprotocol.Message{{Role: agentprotocol.MessageRoleUser, Content: []agentprotocol.ContentBlock{{Type: agentprotocol.ContentBlockTypeText, Text: &text}}}},
		Tools:    tools,
	}
}

func readAgentIntegrationFrames(t testing.TB, response *http.Response) []agentprotocol.ProviderEnvelope {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || response.Header.Get("X-Accel-Buffering") != "no" {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE status=%d contentType=%q buffering=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), response.Header.Get("X-Accel-Buffering"), body)
	}
	var frames []agentprotocol.ProviderEnvelope
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var envelope agentprotocol.ProviderEnvelope
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, envelope)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return frames
}

func assertAgentIntegrationNetworkError(t testing.TB, response *http.Response, status int, code string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, status, body)
	}
	var envelope apiErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("error=%#v", envelope.Error)
	}
}

func waitAgentIntegrationRunStatus(t testing.TB, runs *service.AgentReadonlyService, userID, runID uuid.UUID, status agentprotocol.ReadonlyRunViewStatus) {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		view, err := runs.Get(context.Background(), userID, runID)
		if err == nil && view.Status == status {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach status %s", runID, status)
}

func TestAgentIntegrationProtocolErrorsAreSanitized(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "inactive device", err: model.ErrDeviceNotActive, status: http.StatusPreconditionRequired, code: "DEVICE_REGISTRATION_REQUIRED"},
		{name: "protocol permission", err: &agentexecution.Error{Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodePermissionDenied, Message: "secret authorization detail"}}, status: http.StatusForbidden, code: "PERMISSION_DENIED"},
		{name: "provider unavailable", err: &agentexecution.Error{Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProviderUnavailable, Message: "secret provider detail"}}, status: http.StatusServiceUnavailable, code: "AGENT_DEPENDENCY_UNAVAILABLE"},
		{name: "unknown", err: errors.New("secret provider response with private prompt"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			router := &agentIntegrationRouter{Router: &Router{logger: slog.New(slog.NewTextHandler(&logs, nil))}}
			request := httptest.NewRequest(http.MethodPost, "https://dayorder.example/api/v1/agent/runs", nil)
			request = request.WithContext(context.WithValue(request.Context(), requestIDKey{}, uuid.NewString()))
			response := httptest.NewRecorder()
			router.handleAgentIntegrationError(response, request, test.err)
			assertAgentIntegrationError(t, response, test.status, test.code)
			for _, output := range []string{response.Body.String(), logs.String()} {
				if strings.Contains(output, "secret") {
					t.Fatalf("raw internal failure leaked: %s", output)
				}
			}
		})
	}
}

type agentIntegrationFlushErrorWriter struct {
	header http.Header
	err    error
}

func (writer *agentIntegrationFlushErrorWriter) Header() http.Header        { return writer.header }
func (*agentIntegrationFlushErrorWriter) Write(payload []byte) (int, error) { return len(payload), nil }
func (*agentIntegrationFlushErrorWriter) WriteHeader(int)                   {}
func (writer *agentIntegrationFlushErrorWriter) FlushError() error          { return writer.err }

func TestAgentIntegrationResponseControllerPropagatesWrappedFlushError(t *testing.T) {
	want := errors.New("sentinel flush failure")
	underlying := &agentIntegrationFlushErrorWriter{header: make(http.Header), err: want}
	wrapped := &responseStatusWriter{ResponseWriter: underlying}

	err := http.NewResponseController(wrapped).Flush()

	if !errors.Is(err, want) {
		t.Fatalf("flush error=%v, want sentinel", err)
	}
	if wrapped.status != http.StatusOK {
		t.Fatalf("status=%d, want %d", wrapped.status, http.StatusOK)
	}
}

func TestAgentIntegrationResponseControllerDelegatesWriteDeadlineThroughLiveMiddleware(t *testing.T) {
	router := &Router{allowedOrigins: map[string]struct{}{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	handler := router.middleware(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if err := http.NewResponseController(response).SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			router.writeError(response, request, http.StatusInternalServerError, "DEADLINE_FAILED", "deadline failed", false, nil)
			return
		}
		_, _ = io.WriteString(response, "deadline delegated")
	}))
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "deadline delegated" {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
}

func TestAgentIntegrationToolDeadlineUsesEarliestAuthority(t *testing.T) {
	now := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		runDeadline time.Time
		want        time.Time
	}{
		{name: "tool timeout", runDeadline: now.Add(time.Minute), want: now.Add(10 * time.Second)},
		{name: "run deadline", runDeadline: now.Add(3 * time.Second), want: now.Add(3 * time.Second)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := agentIntegrationToolDeadline(now, test.runDeadline, 10*time.Second); !got.Equal(test.want) {
				t.Fatalf("deadline=%s want=%s", got, test.want)
			}
		})
	}
}

func TestAgentIntegrationHeartbeatIsOnlyAnSSEComment(t *testing.T) {
	response := httptest.NewRecorder()
	if err := writeAgentSSEHeartbeat(response, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := response.Body.String(); got != ": heartbeat\n\n" || strings.Contains(got, "data:") {
		t.Fatalf("heartbeat=%q", got)
	}
}

func TestAgentIntegrationSSELimitReservesOneErrorTerminal(t *testing.T) {
	request := agentprotocol.ModelTurnRequest{RunID: uuid.NewString(), TurnID: strings.Repeat("t", maxAgentOperationID)}
	response := httptest.NewRecorder()
	runDeadline := time.Now().Add(time.Minute)
	sequence, totalBytes := 1, 0

	maxSizedTextEvent := func(sequence int) agentprotocol.ProviderEvent {
		text := strings.Repeat("x", maxAgentSSEDataBytes)
		event := agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}
		envelope := agentprotocol.ProviderEnvelope{
			ProtocolVersion: "2.0", RunID: request.RunID, TurnID: request.TurnID, Sequence: sequence, Event: event,
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		overhead := len(payload) - len(text)
		if overhead >= maxAgentSSEDataBytes {
			t.Fatalf("envelope overhead=%d", overhead)
		}
		text = strings.Repeat("x", maxAgentSSEDataBytes-overhead)
		event.Text = &text
		envelope.Event = event
		payload, err = json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if len(payload) != maxAgentSSEDataBytes {
			t.Fatalf("max-sized envelope=%d, want %d", len(payload), maxAgentSSEDataBytes)
		}
		return event
	}

	for sequence <= 32 {
		written, err := writeAgentSSEEnvelope(response, request, sequence, maxSizedTextEvent(sequence), runDeadline, totalBytes)
		if errors.Is(err, errAgentSSELimit) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		totalBytes += written
		sequence++
	}
	if sequence > 32 {
		t.Fatal("nonterminal stream did not reach the cumulative limit")
	}

	written, err := writeAgentSSEEnvelope(response, request, sequence, controlledAgentStreamError(&agentexecution.Error{
		Agent: agentprotocol.AgentError{Code: agentprotocol.ErrorCodeProtocolIncompatible},
	}), runDeadline, totalBytes)
	if err != nil {
		t.Fatalf("reserved error terminal was not writable: %v", err)
	}
	totalBytes += written

	var frames []agentprotocol.ProviderEnvelope
	emittedBytes := 0
	for _, block := range bytes.Split(response.Body.Bytes(), []byte("\n\n")) {
		if len(block) == 0 {
			continue
		}
		if !bytes.HasPrefix(block, []byte("data: ")) {
			t.Fatalf("non-data SSE block=%q", block)
		}
		payload := bytes.TrimPrefix(block, []byte("data: "))
		emittedBytes += len(payload)
		var envelope agentprotocol.ProviderEnvelope
		if err = json.Unmarshal(payload, &envelope); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, envelope)
	}
	if emittedBytes != totalBytes || emittedBytes > maxAgentSSETurnBytes {
		t.Fatalf("emitted bytes=%d returned bytes=%d cap=%d", emittedBytes, totalBytes, maxAgentSSETurnBytes)
	}
	if len(frames) != sequence {
		t.Fatalf("frames=%d, want %d", len(frames), sequence)
	}
	errorFrames := 0
	for index, frame := range frames {
		if frame.Sequence != index+1 {
			t.Fatalf("frame %d sequence=%d", index, frame.Sequence)
		}
		if frame.Event.Type == agentprotocol.ProviderEventTypeError {
			errorFrames++
		}
	}
	terminal := frames[len(frames)-1].Event
	if errorFrames != 1 || terminal.Type != agentprotocol.ProviderEventTypeError || terminal.Error == nil || terminal.Error.Code != agentprotocol.ErrorCodeProtocolIncompatible {
		t.Fatalf("error frames=%d terminal=%#v", errorFrames, terminal)
	}
}
