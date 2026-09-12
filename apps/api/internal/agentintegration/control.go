package agentintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/database"
	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/worker"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var allowedFaults = map[string]struct{}{
	"tool_timeout": {}, "run_timeout": {}, "provider_429": {}, "provider_disconnect": {},
	"complete_once": {}, "commit_once": {}, "interrupted": {},
}

type faultControl struct {
	mu                 sync.RWMutex
	fault              string
	armed              map[uuid.UUID]string
	runs               map[uuid.UUID]string
	used               map[string]bool
	using              map[string]bool
	interrupted        map[uuid.UUID]bool
	activeDependencies map[uuid.UUID]int
	owners             map[uuid.UUID]uuid.UUID
	reservations       map[uuid.UUID]faultReservation
	runGenerations     map[uuid.UUID]uint64
	nextGeneration     uint64
	migrator           *pgxpool.Pool
}

type faultReservation struct {
	userID     uuid.UUID
	runID      uuid.UUID
	fault      string
	generation uint64
}

func (control *faultControl) Set(fault string) error {
	if _, ok := allowedFaults[fault]; !ok {
		return errors.New("unsupported integration fault")
	}
	control.mu.Lock()
	control.fault = fault
	control.mu.Unlock()
	return nil
}

func (control *faultControl) Arm(userID uuid.UUID, fault string) error {
	if userID == uuid.Nil {
		return errors.New("integration fault account is required")
	}
	if _, ok := allowedFaults[fault]; !ok {
		return errors.New("unsupported integration fault")
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if _, exists := control.reservations[userID]; exists {
		return errors.New("integration fault account has an unresolved transaction")
	}
	if control.armed == nil {
		control.armed = make(map[uuid.UUID]string)
	}
	control.fault = fault
	control.armed[userID] = fault
	return nil
}

func (control *faultControl) Get() string {
	if control == nil {
		return ""
	}
	control.mu.RLock()
	defer control.mu.RUnlock()
	return control.fault
}

func (control *faultControl) armedFor(userID uuid.UUID, fault string) bool {
	control.mu.RLock()
	defer control.mu.RUnlock()
	return control.armed[userID] == fault
}

func (control *faultControl) reserve(userID, runID uuid.UUID) (*faultReservation, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	if existing, exists := control.reservations[userID]; exists {
		return nil, fmt.Errorf("integration fault account has unresolved Run %s", existing.runID)
	}
	fault := control.armed[userID]
	if fault == "" {
		return nil, nil
	}
	control.nextGeneration++
	reservation := faultReservation{
		userID: userID, runID: runID, fault: fault, generation: control.nextGeneration,
	}
	delete(control.armed, userID)
	if control.reservations == nil {
		control.reservations = make(map[uuid.UUID]faultReservation)
	}
	control.reservations[userID] = reservation
	return &reservation, nil
}

func (control *faultControl) publish(reservation faultReservation) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	current, exists := control.reservations[reservation.userID]
	if !exists || current.generation != reservation.generation || current.runID != reservation.runID {
		return errors.New("integration fault transaction reservation is no longer owned")
	}
	if generation, exists := control.runGenerations[reservation.runID]; exists && generation != reservation.generation {
		return errors.New("integration fault Run already has another reservation")
	}
	if control.runs == nil {
		control.runs = make(map[uuid.UUID]string)
	}
	if control.owners == nil {
		control.owners = make(map[uuid.UUID]uuid.UUID)
	}
	if control.runGenerations == nil {
		control.runGenerations = make(map[uuid.UUID]uint64)
	}
	control.runs[reservation.runID] = reservation.fault
	control.owners[reservation.runID] = reservation.userID
	control.runGenerations[reservation.runID] = reservation.generation
	return nil
}

func (control *faultControl) commit(reservation faultReservation) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	current, exists := control.reservations[reservation.userID]
	if !exists || current.generation != reservation.generation || current.runID != reservation.runID {
		return errors.New("integration fault transaction reservation is no longer owned")
	}
	delete(control.reservations, reservation.userID)
	return nil
}

func (control *faultControl) quarantine(reservation faultReservation) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	current, exists := control.reservations[reservation.userID]
	if !exists || current.generation != reservation.generation || current.runID != reservation.runID {
		return errors.New("integration fault transaction reservation is no longer owned")
	}
	return nil
}

func (control *faultControl) release(reservation faultReservation) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	current, exists := control.reservations[reservation.userID]
	if !exists {
		return nil
	}
	if current.generation != reservation.generation || current.runID != reservation.runID {
		return errors.New("integration fault transaction reservation is no longer owned")
	}
	delete(control.reservations, reservation.userID)
	if control.runGenerations[reservation.runID] == reservation.generation {
		delete(control.runGenerations, reservation.runID)
		delete(control.runs, reservation.runID)
		delete(control.owners, reservation.runID)
	}
	if control.armed[reservation.userID] != "" {
		return errors.New("integration fault account was rearmed during a transaction")
	}
	if control.armed == nil {
		control.armed = make(map[uuid.UUID]string)
	}
	control.armed[reservation.userID] = reservation.fault
	return nil
}

func (control *faultControl) forRun(runID uuid.UUID) string {
	control.mu.RLock()
	defer control.mu.RUnlock()
	return control.runs[runID]
}

func (control *faultControl) forRunOwner(runID, userID uuid.UUID) string {
	control.mu.RLock()
	defer control.mu.RUnlock()
	if control.owners[runID] != userID {
		return ""
	}
	return control.runs[runID]
}

func (control *faultControl) takeRun(runID uuid.UUID, stage string) bool {
	control.mu.Lock()
	defer control.mu.Unlock()
	key := runID.String() + ":" + stage
	if control.used == nil {
		control.used = make(map[string]bool)
	}
	if control.used[key] {
		return false
	}
	control.used[key] = true
	return true
}

func (control *faultControl) reserveRunOnce(runID uuid.UUID, stage string) (reserved, used bool) {
	control.mu.Lock()
	defer control.mu.Unlock()
	key := runID.String() + ":" + stage
	if control.used[key] {
		return false, true
	}
	if control.using == nil {
		control.using = make(map[string]bool)
	}
	if control.using[key] {
		return false, false
	}
	control.using[key] = true
	return true, false
}

func (control *faultControl) finishRunOnce(runID uuid.UUID, stage string, succeeded bool) {
	control.mu.Lock()
	defer control.mu.Unlock()
	key := runID.String() + ":" + stage
	delete(control.using, key)
	if succeeded {
		if control.used == nil {
			control.used = make(map[string]bool)
		}
		control.used[key] = true
	}
}

func (control *faultControl) releaseInterrupted(runID uuid.UUID) {
	control.mu.Lock()
	if control.interrupted == nil {
		control.interrupted = make(map[uuid.UUID]bool)
	}
	control.interrupted[runID] = true
	control.mu.Unlock()
}

func (control *faultControl) interruptionReleased(runID uuid.UUID) bool {
	control.mu.RLock()
	defer control.mu.RUnlock()
	return control.interrupted[runID]
}

func (control *faultControl) beginDependency(runID uuid.UUID) {
	if control == nil || runID == uuid.Nil {
		return
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.activeDependencies == nil {
		control.activeDependencies = make(map[uuid.UUID]int)
	}
	control.activeDependencies[runID]++
}

func (control *faultControl) endDependency(runID uuid.UUID) {
	if control == nil || runID == uuid.Nil {
		return
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.activeDependencies[runID] <= 1 {
		delete(control.activeDependencies, runID)
		return
	}
	control.activeDependencies[runID]--
}

func (control *faultControl) activeDependencyCount(runID uuid.UUID) int {
	if control == nil || runID == uuid.Nil {
		return 0
	}
	control.mu.RLock()
	defer control.mu.RUnlock()
	return control.activeDependencies[runID]
}

type faultRunContextKey struct{}

func withFaultRun(ctx context.Context, runID uuid.UUID) context.Context {
	return context.WithValue(ctx, faultRunContextKey{}, runID)
}

func faultRunFromContext(ctx context.Context) (uuid.UUID, bool) {
	runID, ok := ctx.Value(faultRunContextKey{}).(uuid.UUID)
	return runID, ok && runID != uuid.Nil
}

type controlledCalendarStore struct {
	service.CalendarStore
	control *faultControl
}

func (store controlledCalendarStore) ListEvents(ctx context.Context, tx database.Tx, userID uuid.UUID, start, end *time.Time, after *model.ResourcePosition, limit int) ([]model.CalendarEvent, error) {
	runID, tagged := faultRunFromContext(ctx)
	if tagged && store.control != nil {
		store.control.beginDependency(runID)
		defer store.control.endDependency(runID)
	}
	if tagged && store.control != nil && store.control.forRunOwner(runID, userID) == "tool_timeout" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return store.CalendarStore.ListEvents(ctx, tx, userID, start, end, after, limit)
}

type controlledAdapter struct {
	base    agentprovider.Adapter
	control *faultControl
}

func (adapter controlledAdapter) Stream(ctx context.Context, request agentprotocol.ModelTurnRequest, options agentprovider.TurnOptions) iter.Seq2[agentprotocol.ProviderEvent, error] {
	return func(yield func(agentprotocol.ProviderEvent, error) bool) {
		runID, _ := uuid.Parse(request.RunID)
		adapter.control.beginDependency(runID)
		defer adapter.control.endDependency(runID)
		switch adapter.control.forRun(runID) {
		case "run_timeout":
			<-ctx.Done()
			yield(agentprotocol.ProviderEvent{}, &agentprovider.ProviderError{Code: agentprotocol.ErrorCodeTimeout})
			return
		case "provider_429":
			if adapter.control.takeRun(runID, "provider_429") {
				yield(agentprotocol.ProviderEvent{}, &agentprovider.ProviderError{
					Code: agentprotocol.ErrorCodeProviderRateLimited, Status: 429, Retryable: true,
				})
				return
			}
		case "provider_disconnect":
			text := "partial"
			if yield(agentprotocol.ProviderEvent{Type: agentprotocol.ProviderEventTypeTextDelta, Text: &text}, nil) {
				yield(agentprotocol.ProviderEvent{}, &agentprovider.ProviderError{Code: agentprotocol.ErrorCodeProviderUnavailable, Retryable: true})
			}
			return
		case "interrupted":
			yield(agentprotocol.ProviderEvent{}, errors.New("controlled integration executor interruption"))
			return
		}
		for event, err := range adapter.base.Stream(ctx, request, options) {
			if !yield(event, err) {
				return
			}
		}
	}
}

type controlledStore struct {
	agentexecution.Store
	control *faultControl
}

func (store controlledStore) Create(ctx context.Context, tx database.Tx, record agentexecution.Record) error {
	controlled, ok := tx.(interface {
		bindFault(uuid.UUID, uuid.UUID) error
		cancelFaultBinding() error
	})
	if !ok {
		return errors.New("controlled integration transaction is required")
	}
	if err := controlled.bindFault(record.Execution.UserID, record.Run.ID); err != nil {
		return err
	}
	if err := store.Store.Create(ctx, tx, record); err != nil {
		return errors.Join(err, controlled.cancelFaultBinding())
	}
	return nil
}

type controlledBeginner struct {
	base    database.Beginner
	control *faultControl
}

func (beginner controlledBeginner) Begin(ctx context.Context) (database.Tx, error) {
	if beginner.base == nil || beginner.control == nil {
		return nil, errors.New("controlled integration transaction beginner is required")
	}
	tx, err := beginner.base.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &controlledTx{Tx: tx, control: beginner.control}, nil
}

type integrationPoolBeginner struct {
	pool *pgxpool.Pool
}

func (beginner integrationPoolBeginner) Begin(ctx context.Context) (database.Tx, error) {
	if beginner.pool == nil {
		return nil, errors.New("integration transaction pool is required")
	}
	return beginner.pool.Begin(ctx)
}

type controlledTx struct {
	database.Tx
	control         *faultControl
	binding         *faultReservation
	commitAttempted bool
}

func (tx *controlledTx) bindFault(userID, runID uuid.UUID) error {
	if tx == nil || tx.control == nil {
		return errors.New("controlled integration transaction is required")
	}
	if tx.binding != nil {
		return errors.New("controlled integration transaction already has a fault reservation")
	}
	binding, err := tx.control.reserve(userID, runID)
	if err != nil {
		return err
	}
	tx.binding = binding
	return nil
}

func (tx *controlledTx) cancelFaultBinding() error {
	if tx == nil || tx.binding == nil {
		return nil
	}
	err := tx.control.release(*tx.binding)
	tx.binding = nil
	return err
}

func (tx *controlledTx) Commit(ctx context.Context) error {
	if tx == nil || tx.Tx == nil {
		return errors.New("controlled integration transaction is required")
	}
	if tx.binding == nil {
		tx.commitAttempted = true
		return tx.Tx.Commit(ctx)
	}
	binding := *tx.binding
	if err := tx.control.publish(binding); err != nil {
		return err
	}
	tx.commitAttempted = true
	err := tx.Tx.Commit(ctx)
	switch {
	case err == nil:
		return tx.control.commit(binding)
	case conclusivelyRolledBack(err):
		return errors.Join(err, tx.control.release(binding))
	default:
		return errors.Join(err, tx.control.quarantine(binding))
	}
}

func (tx *controlledTx) Rollback(ctx context.Context) error {
	if tx == nil || tx.Tx == nil {
		return errors.New("controlled integration transaction is required")
	}
	err := tx.Tx.Rollback(ctx)
	if tx.commitAttempted || tx.binding == nil {
		return err
	}
	return errors.Join(err, tx.cancelFaultBinding())
}

func conclusivelyRolledBack(err error) bool {
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return true
	}
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01")
}

func (store controlledStore) Save(ctx context.Context, tx database.Tx, record agentexecution.Record, version int64, token uuid.UUID) error {
	fault := store.control.forRun(record.Run.ID)
	if terminalRunStatus(record.Run.Status) {
		if fault == "commit_once" && store.control.takeRun(record.Run.ID, "commit_once") {
			return errors.New("controlled integration terminal commit failure")
		}
		if fault == "interrupted" && !store.control.interruptionReleased(record.Run.ID) {
			return fmt.Errorf("%w: controlled integration executor interruption", service.ErrValidation)
		}
	}
	return store.Store.Save(ctx, tx, record, version, token)
}

func terminalRunStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "stopped"
}

type controlledOutbox struct {
	base    worker.OutboxRepository
	faults  outboxFaultStore
	control *faultControl
}

type outboxFaultStore interface {
	EventRun(context.Context, uuid.UUID) (uuid.UUID, error)
	ExpireLease(context.Context, uuid.UUID, uuid.UUID) (bool, error)
}

type postgresOutboxFaultStore struct {
	pool *pgxpool.Pool
}

func (store postgresOutboxFaultStore) EventRun(ctx context.Context, eventID uuid.UUID) (uuid.UUID, error) {
	if store.pool == nil {
		return uuid.Nil, errors.New("integration Outbox diagnostics are unavailable")
	}
	var runID uuid.UUID
	err := store.pool.QueryRow(ctx, `SELECT aggregate_id FROM dayorder.outbox_events WHERE id = $1`, eventID).Scan(&runID)
	return runID, err
}

func (store postgresOutboxFaultStore) ExpireLease(ctx context.Context, eventID, token uuid.UUID) (bool, error) {
	if store.pool == nil {
		return false, errors.New("integration Outbox diagnostics are unavailable")
	}
	tag, err := store.pool.Exec(ctx, `
UPDATE dayorder.outbox_events
SET locked_at = now() - interval '6 minutes'
WHERE id = $1 AND lock_token = $2 AND status = 'processing'
`, eventID, token)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (repository controlledOutbox) Claim(ctx context.Context, limit int, token uuid.UUID, stale time.Duration) ([]model.OutboxEvent, error) {
	return repository.base.Claim(ctx, limit, token, stale)
}

func (repository controlledOutbox) Complete(ctx context.Context, eventID, token uuid.UUID) error {
	if repository.faults == nil {
		return errors.New("controlled integration outbox lookup failed")
	}
	runID, err := repository.faults.EventRun(ctx, eventID)
	if err != nil {
		return errors.New("controlled integration outbox lookup failed")
	}
	if repository.control.forRun(runID) != "complete_once" {
		return repository.base.Complete(ctx, eventID, token)
	}
	reserved, used := repository.control.reserveRunOnce(runID, "complete_once")
	if used {
		return repository.base.Complete(ctx, eventID, token)
	}
	if !reserved {
		return errors.New("controlled integration outbox confirmation is already in progress")
	}
	succeeded := false
	defer func() { repository.control.finishRunOnce(runID, "complete_once", succeeded) }()
	expired, err := repository.faults.ExpireLease(ctx, eventID, token)
	if err != nil {
		return errors.New("controlled integration outbox lease update failed")
	}
	if !expired {
		return errors.New("controlled integration outbox lease was not held")
	}
	succeeded = true
	return errors.New("controlled integration outbox confirmation failure")
}

func (repository controlledOutbox) Retry(ctx context.Context, retry model.OutboxRetry) error {
	if repository.faults == nil {
		return errors.New("controlled integration outbox lookup failed")
	}
	runID, err := repository.faults.EventRun(ctx, retry.EventID)
	if err != nil {
		return errors.New("controlled integration outbox lookup failed")
	}
	if repository.control.forRun(runID) == "interrupted" {
		retry.AvailableAt = time.Now().UTC()
		if err = repository.base.Retry(ctx, retry); err != nil {
			return err
		}
		repository.control.releaseInterrupted(runID)
		return nil
	}
	return repository.base.Retry(ctx, retry)
}

type controlHandler struct {
	application        http.Handler
	timeoutApplication http.Handler
	sessions           *service.SessionService
	services           *agenttest.Services
	configuration      fixtureConfig
	faults             *faultControl
	allowed            map[string]struct{}
	traces             *runtimeTraceRecorder
}

func newControlHandler(application, timeoutApplication http.Handler, sessions *service.SessionService, services *agenttest.Services, configuration fixtureConfig, faults *faultControl, allowed map[string]struct{}, traceRecorders ...*runtimeTraceRecorder) http.Handler {
	handler := &controlHandler{
		application: application, timeoutApplication: timeoutApplication, sessions: sessions,
		services: services, configuration: configuration, faults: faults, allowed: allowed,
	}
	if len(traceRecorders) > 0 {
		handler.traces = traceRecorders[0]
	}
	return handler
}

func (handler *controlHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(request.URL.Path, "/__test/") {
		application := handler.application
		if request.Method == http.MethodPost && request.URL.Path == "/api/v1/agent/runs" && handler.timeoutApplication != nil {
			if authenticated, ok := handler.authenticate(request); ok && handler.faults.armedFor(authenticated.Account.ID, "run_timeout") {
				application = handler.timeoutApplication
			}
		}
		if runID, ok := calendarToolRunID(request); ok {
			request = request.WithContext(withFaultRun(request.Context(), runID))
		}
		application.ServeHTTP(response, request)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if !trustedDiagnosticRequest(request, handler.allowed) {
		writeControlError(response, http.StatusForbidden, "ORIGIN_NOT_ALLOWED")
		return
	}
	if origin := strings.TrimSuffix(strings.TrimSpace(request.Header.Get("Origin")), "/"); origin != "" {
		response.Header().Set("Access-Control-Allow-Origin", origin)
		response.Header().Set("Access-Control-Allow-Credentials", "true")
		response.Header().Set("Vary", "Origin")
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/__test/config":
		writeControlJSON(response, http.StatusOK, handler.configuration)
	case request.Method == http.MethodPost && request.URL.Path == "/__test/fault":
		handler.setFault(response, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__test/session/expire":
		handler.expireSession(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/__test/runs/"):
		handler.runState(response, request)
	default:
		writeControlError(response, http.StatusNotFound, "NOT_FOUND")
	}
}

func calendarToolRunID(request *http.Request) (uuid.UUID, bool) {
	if request == nil || request.Method != http.MethodPost {
		return uuid.Nil, false
	}
	const prefix = "/api/v1/agent/runs/"
	const suffix = "/tools/calendar-read"
	if !strings.HasPrefix(request.URL.Path, prefix) || !strings.HasSuffix(request.URL.Path, suffix) {
		return uuid.Nil, false
	}
	identifier := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, prefix), suffix)
	if strings.Contains(identifier, "/") {
		return uuid.Nil, false
	}
	runID, err := uuid.Parse(identifier)
	return runID, err == nil
}

type runContextHandler struct {
	base worker.Handler
}

func (handler runContextHandler) Handle(ctx context.Context, event model.OutboxEvent) error {
	return handler.base.Handle(withFaultRun(ctx, event.AggregateID), event)
}

func (handler *controlHandler) authenticate(request *http.Request) (model.AuthenticatedSession, bool) {
	cookie, err := request.Cookie("dayorder_session")
	if err != nil || cookie.Value == "" {
		return model.AuthenticatedSession{}, false
	}
	authenticated, err := handler.sessions.Authenticate(request.Context(), cookie.Value)
	return authenticated, err == nil
}

func (handler *controlHandler) setFault(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := handler.authenticate(request)
	if !ok {
		writeControlError(response, http.StatusUnauthorized, "AUTH_REQUIRED")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeControlError(response, http.StatusUnsupportedMediaType, "JSON_REQUIRED")
		return
	}
	var input struct {
		Fault string `json:"fault"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || handler.faults.Arm(authenticated.Account.ID, input.Fault) != nil {
		writeControlError(response, http.StatusUnprocessableEntity, "INVALID_FAULT")
		return
	}
	writeControlJSON(response, http.StatusOK, map[string]string{"fault": input.Fault})
}

type testRunState struct {
	Status             string              `json:"status"`
	ErrorCode          *string             `json:"errorCode"`
	Summary            string              `json:"summary"`
	Usage              agentprotocol.Usage `json:"usage"`
	UsageComplete      bool                `json:"usageComplete"`
	OutboxCount        int                 `json:"outboxCount"`
	OutboxStatus       *string             `json:"outboxStatus"`
	Deliveries         int                 `json:"deliveries"`
	ProviderCalls      int                 `json:"providerCalls"`
	CalendarCalls      int                 `json:"calendarCalls"`
	ActiveDependencies int                 `json:"activeDependencies"`
	Attempts           []int               `json:"attempts"`
	SourceRefs         []testSourceRef     `json:"sourceRefs"`
	Trace              testTraceDiagnostic `json:"trace"`
}

type testSourceRef struct {
	EntityID      string `json:"entityId"`
	EntityVersion int64  `json:"entityVersion"`
}

func (handler *controlHandler) runState(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := handler.authenticate(request)
	if !ok {
		writeControlError(response, http.StatusUnauthorized, "AUTH_REQUIRED")
		return
	}
	runID, err := uuid.Parse(strings.TrimPrefix(request.URL.Path, "/__test/runs/"))
	if err != nil {
		writeControlError(response, http.StatusBadRequest, "INVALID_RUN_ID")
		return
	}
	view, err := handler.services.Runs.Get(request.Context(), authenticated.Account.ID, runID)
	if err != nil {
		writeControlError(response, http.StatusNotFound, "NOT_FOUND")
		return
	}
	state := newTestRunState(view)
	state.ActiveDependencies = handler.faults.activeDependencyCount(runID)
	if view.Error != nil {
		code := string(view.Error.Code)
		state.ErrorCode = &code
	}
	if view.Summary != nil {
		state.Summary = *view.Summary
	}
	terminal := view.Status == agentprotocol.ReadonlyRunViewStatusCompleted ||
		view.Status == agentprotocol.ReadonlyRunViewStatusFailed ||
		view.Status == agentprotocol.ReadonlyRunViewStatusStopped
	state.Trace = handler.traces.Diagnostic(runID, terminal)
	if handler.faults == nil || handler.faults.migrator == nil {
		writeControlError(response, http.StatusInternalServerError, "DIAGNOSTIC_FAILED")
		return
	}
	err = loadRunState(request.Context(), handler.faults.migrator, authenticated.Account.ID, runID, &state)
	if err != nil {
		writeControlError(response, http.StatusInternalServerError, "DIAGNOSTIC_FAILED")
		return
	}
	writeControlJSON(response, http.StatusOK, state)
}

func newTestRunState(view agentprotocol.ReadonlyRunView) testRunState {
	return testRunState{
		Status: string(view.Status), Usage: view.Usage, UsageComplete: view.UsageComplete,
		Attempts: []int{}, SourceRefs: []testSourceRef{},
	}
}

type sessionExpirer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func expireFixtureSession(ctx context.Context, executor sessionExpirer, configuration fixtureConfig, authenticated model.AuthenticatedSession) error {
	if executor == nil || authenticated.Account.ID == uuid.Nil || authenticated.Session.ID == uuid.Nil ||
		authenticated.Session.UserID != authenticated.Account.ID {
		return errors.New("authenticated integration session is required")
	}
	known := false
	for _, account := range configuration.Accounts {
		if account.userID == authenticated.Account.ID && account.Email == authenticated.Account.Email {
			known = true
			break
		}
	}
	if !known {
		return errors.New("session does not belong to a synthetic integration account")
	}
	createdAt := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(24 * time.Hour)
	tag, err := executor.Exec(ctx, `
UPDATE dayorder.sessions
SET created_at = $3, expires_at = $4
WHERE user_id = $1 AND id = $2
`, authenticated.Account.ID, authenticated.Session.ID, createdAt, expiresAt)
	if err != nil || tag.RowsAffected() != 1 {
		return errors.New("expire synthetic integration session failed")
	}
	return nil
}

func (handler *controlHandler) expireSession(response http.ResponseWriter, request *http.Request) {
	authenticated, ok := handler.authenticate(request)
	if !ok {
		writeControlError(response, http.StatusUnauthorized, "AUTH_REQUIRED")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeControlError(response, http.StatusUnsupportedMediaType, "JSON_REQUIRED")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16))
	decoder.DisallowUnknownFields()
	var input struct{}
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeControlError(response, http.StatusUnprocessableEntity, "INVALID_SESSION_EXPIRY")
		return
	}
	if handler.faults == nil || expireFixtureSession(request.Context(), handler.faults.migrator, handler.configuration, authenticated) != nil {
		writeControlError(response, http.StatusInternalServerError, "SESSION_EXPIRY_FAILED")
		return
	}
	writeControlJSON(response, http.StatusOK, map[string]string{"status": "expired"})
}

func loadRunState(ctx context.Context, queries interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, userID, runID uuid.UUID, state *testRunState) error {
	if state == nil {
		return errors.New("integration run state is required")
	}
	var status *string
	if err := queries.QueryRow(ctx, `
SELECT count(*)::integer, max(status), coalesce(max(attempts), 0)::integer
FROM dayorder.outbox_events WHERE user_id = $1 AND aggregate_id = $2
`, userID, runID).Scan(&state.OutboxCount, &status, &state.Deliveries); err != nil {
		return err
	}
	state.OutboxStatus = status
	rows, err := queries.Query(ctx, `
SELECT kind, attempts FROM dayorder.agent_run_operations
WHERE user_id = $1 AND run_id = $2 ORDER BY started_at, kind, operation_id
`, userID, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var attempts int
		if err = rows.Scan(&kind, &attempts); err != nil {
			return err
		}
		switch kind {
		case "provider_turn":
			state.ProviderCalls += attempts
			state.Attempts = append(state.Attempts, attempts)
		case "calendar_read":
			state.CalendarCalls++
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	refRows, err := queries.Query(ctx, `
SELECT entity_id, entity_version FROM dayorder.agent_source_refs
WHERE user_id = $1 AND run_id = $2 ORDER BY entity_id, entity_version
`, userID, runID)
	if err != nil {
		return err
	}
	defer refRows.Close()
	for refRows.Next() {
		var ref testSourceRef
		if err = refRows.Scan(&ref.EntityID, &ref.EntityVersion); err != nil {
			return err
		}
		state.SourceRefs = append(state.SourceRefs, ref)
	}
	return refRows.Err()
}

func writeControlJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeControlError(response http.ResponseWriter, status int, code string) {
	writeControlJSON(response, status, map[string]any{"error": map[string]string{"code": code}})
}

var _ agentprovider.Adapter = controlledAdapter{}
var _ agentexecution.Store = controlledStore{}
var _ worker.OutboxRepository = controlledOutbox{}
