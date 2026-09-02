package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"dayorder.local/api/internal/model"
	"dayorder.local/api/internal/service"

	"github.com/google/uuid"
)

type AccountApplication interface {
	Register(context.Context, service.RegisterInput) (service.RegistrationResult, error)
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

type GoalApplication interface {
	Create(context.Context, service.MutationContext, service.CreateGoalInput) (model.Goal, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (model.Goal, error)
	List(context.Context, uuid.UUID, string, int) (service.GoalPage, error)
	Update(context.Context, service.MutationContext, uuid.UUID, int64, service.UpdateGoalInput) (model.Goal, error)
	Delete(context.Context, service.MutationContext, uuid.UUID, int64) error
	CreateMilestone(context.Context, service.MutationContext, uuid.UUID, service.CreateMilestoneInput) (model.GoalMilestone, error)
	GetMilestone(context.Context, uuid.UUID, uuid.UUID) (model.GoalMilestone, error)
	ListMilestones(context.Context, uuid.UUID, uuid.UUID) ([]model.GoalMilestone, error)
	UpdateMilestone(context.Context, service.MutationContext, uuid.UUID, int64, service.UpdateMilestoneInput) (model.GoalMilestone, error)
	DeleteMilestone(context.Context, service.MutationContext, uuid.UUID, int64) error
}

type TaskApplication interface {
	Create(context.Context, service.MutationContext, service.TaskInput) (model.Task, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (model.Task, error)
	List(context.Context, uuid.UUID, string, string, int) (service.TaskPage, error)
	Update(context.Context, service.MutationContext, uuid.UUID, int64, service.TaskInput) (model.Task, error)
	Delete(context.Context, service.MutationContext, uuid.UUID, int64) error
}

type CalendarApplication interface {
	Create(context.Context, service.MutationContext, service.CalendarEventInput) (service.CalendarEventResult, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (service.CalendarEventResult, error)
	List(context.Context, uuid.UUID, *time.Time, *time.Time, string, int) (service.CalendarPage, error)
	Update(context.Context, service.MutationContext, uuid.UUID, int64, service.CalendarEventInput) (service.CalendarEventResult, error)
	Delete(context.Context, service.MutationContext, uuid.UUID, int64) error
}

type ContentApplication interface {
	CreateRecord(context.Context, service.MutationContext, service.RecordInput) (model.Record, error)
	GetRecord(context.Context, uuid.UUID, uuid.UUID) (model.Record, error)
	ListRecords(context.Context, uuid.UUID, string, int) (service.RecordPage, error)
	UpdateRecord(context.Context, service.MutationContext, uuid.UUID, int64, service.RecordInput) (model.Record, error)
	DeleteRecord(context.Context, service.MutationContext, uuid.UUID, int64) error
	CreateNote(context.Context, service.MutationContext, service.NoteInput) (model.Note, error)
	GetNote(context.Context, uuid.UUID, uuid.UUID) (model.Note, error)
	ListNotes(context.Context, uuid.UUID, string, string, int) (service.NotePage, error)
	UpdateNote(context.Context, service.MutationContext, uuid.UUID, int64, service.NoteInput) (model.Note, error)
	DeleteNote(context.Context, service.MutationContext, uuid.UUID, int64) error
	CreateReview(context.Context, service.MutationContext, service.ReviewInput) (model.DailyReview, error)
	GetReview(context.Context, uuid.UUID, uuid.UUID) (model.DailyReview, error)
	ListReviews(context.Context, uuid.UUID, string, int) (service.ReviewPage, error)
	UpdateReview(context.Context, service.MutationContext, uuid.UUID, int64, service.ReviewInput) (model.DailyReview, error)
	DeleteReview(context.Context, service.MutationContext, uuid.UUID, int64) error
	ListTags(context.Context, uuid.UUID) ([]model.Tag, error)
}

type SettingsApplication interface {
	Get(context.Context, uuid.UUID) (model.UserSettings, error)
	Patch(context.Context, service.MutationContext, int64, json.RawMessage) (model.UserSettings, error)
}

type DeviceApplication interface {
	Register(context.Context, uuid.UUID, uuid.UUID, service.RegisterDeviceInput) (service.DeviceRegistration, error)
	List(context.Context, uuid.UUID) ([]model.UserDevice, error)
}

type SyncApplication interface {
	Bootstrap(context.Context, uuid.UUID, uuid.UUID) (service.SyncBootstrap, error)
	DeviceChanges(context.Context, uuid.UUID, uuid.UUID, string, int) (service.SyncPage, error)
}

type AgentApplication interface {
	Create(context.Context, service.MutationContext, service.StartAgentInput) (model.AgentRun, error)
	Get(context.Context, uuid.UUID, uuid.UUID) (model.AgentRun, error)
	List(context.Context, uuid.UUID, string, int) (service.AgentRunPage, error)
	Accept(context.Context, service.MutationContext, uuid.UUID, int64) (model.AgentApplyResult, error)
	Reject(context.Context, service.MutationContext, uuid.UUID, int64) (model.AgentApplyResult, error)
	Stop(context.Context, service.MutationContext, uuid.UUID, int64) (model.AgentRun, error)
}

type AuditApplication interface {
	Get(context.Context, uuid.UUID, uuid.UUID) (model.AuditEvent, error)
	List(context.Context, uuid.UUID, string, int) (service.AuditPage, error)
}

type UndoApplication interface {
	Undo(context.Context, service.MutationContext, uuid.UUID, int64) (model.UndoResult, error)
}

type RouterOptions struct {
	Accounts       AccountApplication
	Sessions       SessionApplication
	Goals          GoalApplication
	Tasks          TaskApplication
	Calendar       CalendarApplication
	Content        ContentApplication
	Settings       SettingsApplication
	Devices        DeviceApplication
	Sync           SyncApplication
	Agents         AgentApplication
	AgentAvailable bool
	Audits         AuditApplication
	Undos          UndoApplication
	AllowedOrigins []string
	Logger         *slog.Logger
	Metrics        RouterMetrics
	Ready          func(context.Context) error
}

type RouterMetrics interface {
	ObserveHTTPRequest(string, string, int, time.Duration)
	ObserveLoginRateLimited()
	ObserveSyncCursorReset()
	ObserveSyncMutation(string)
}

type Router struct {
	accounts       AccountApplication
	sessions       SessionApplication
	goals          GoalApplication
	tasks          TaskApplication
	calendar       CalendarApplication
	content        ContentApplication
	settings       SettingsApplication
	devices        DeviceApplication
	sync           SyncApplication
	agents         AgentApplication
	agentAvailable bool
	audits         AuditApplication
	undos          UndoApplication
	allowedOrigins map[string]struct{}
	logger         *slog.Logger
	metrics        RouterMetrics
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
		goals: options.Goals, tasks: options.Tasks, calendar: options.Calendar, content: options.Content, settings: options.Settings, devices: options.Devices, sync: options.Sync, agents: options.Agents, agentAvailable: options.AgentAvailable, audits: options.Audits, undos: options.Undos,
		allowedOrigins: make(map[string]struct{}), logger: logger, metrics: options.Metrics, ready: options.Ready,
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
	if router.devices != nil {
		mux.HandleFunc("GET /api/v1/users/me/devices", router.listDevices)
		mux.HandleFunc("PUT /api/v1/users/me/devices/{deviceId}", router.registerDevice)
	}
	if router.sync != nil {
		mux.HandleFunc("GET /api/v1/sync/bootstrap", router.syncBootstrap)
		mux.HandleFunc("GET /api/v1/sync/changes", router.syncChanges)
		mux.HandleFunc("POST /api/v1/sync/mutations", router.syncMutations)
	}
	if router.goals != nil {
		mux.HandleFunc("GET /api/v1/goals", router.listGoals)
		mux.HandleFunc("POST /api/v1/goals", router.createGoal)
		mux.HandleFunc("GET /api/v1/goals/{goalId}", router.getGoal)
		mux.HandleFunc("PATCH /api/v1/goals/{goalId}", router.updateGoal)
		mux.HandleFunc("DELETE /api/v1/goals/{goalId}", router.deleteGoal)
		mux.HandleFunc("GET /api/v1/goals/{goalId}/milestones", router.listMilestones)
		mux.HandleFunc("POST /api/v1/goals/{goalId}/milestones", router.createMilestone)
		mux.HandleFunc("PATCH /api/v1/milestones/{milestoneId}", router.updateMilestone)
		mux.HandleFunc("DELETE /api/v1/milestones/{milestoneId}", router.deleteMilestone)
	}
	if router.tasks != nil {
		mux.HandleFunc("GET /api/v1/tasks", router.listTasks)
		mux.HandleFunc("POST /api/v1/tasks", router.createTask)
		mux.HandleFunc("GET /api/v1/tasks/{taskId}", router.getTask)
		mux.HandleFunc("PATCH /api/v1/tasks/{taskId}", router.updateTask)
		mux.HandleFunc("DELETE /api/v1/tasks/{taskId}", router.deleteTask)
	}
	if router.calendar != nil {
		mux.HandleFunc("GET /api/v1/calendar-events", router.listCalendarEvents)
		mux.HandleFunc("POST /api/v1/calendar-events", router.createCalendarEvent)
		mux.HandleFunc("GET /api/v1/calendar-events/{eventId}", router.getCalendarEvent)
		mux.HandleFunc("PATCH /api/v1/calendar-events/{eventId}", router.updateCalendarEvent)
		mux.HandleFunc("DELETE /api/v1/calendar-events/{eventId}", router.deleteCalendarEvent)
	}
	if router.content != nil {
		mux.HandleFunc("GET /api/v1/records", router.listRecords)
		mux.HandleFunc("POST /api/v1/records", router.createRecord)
		mux.HandleFunc("GET /api/v1/records/{recordId}", router.getRecord)
		mux.HandleFunc("PATCH /api/v1/records/{recordId}", router.updateRecord)
		mux.HandleFunc("DELETE /api/v1/records/{recordId}", router.deleteRecord)
		mux.HandleFunc("GET /api/v1/notes", router.listNotes)
		mux.HandleFunc("POST /api/v1/notes", router.createNote)
		mux.HandleFunc("GET /api/v1/notes/{noteId}", router.getNote)
		mux.HandleFunc("PATCH /api/v1/notes/{noteId}", router.updateNote)
		mux.HandleFunc("DELETE /api/v1/notes/{noteId}", router.deleteNote)
		mux.HandleFunc("GET /api/v1/daily-reviews", router.listReviews)
		mux.HandleFunc("POST /api/v1/daily-reviews", router.createReview)
		mux.HandleFunc("GET /api/v1/daily-reviews/{reviewId}", router.getReview)
		mux.HandleFunc("PATCH /api/v1/daily-reviews/{reviewId}", router.updateReview)
		mux.HandleFunc("DELETE /api/v1/daily-reviews/{reviewId}", router.deleteReview)
		mux.HandleFunc("GET /api/v1/tags", router.listTags)
	}
	if router.settings != nil {
		mux.HandleFunc("GET /api/v1/users/me/settings", router.getSettings)
		mux.HandleFunc("PATCH /api/v1/users/me/settings", router.updateSettings)
	}
	if router.agentAvailable && router.agents != nil {
		mux.HandleFunc("GET /api/v1/agent-runs", router.listAgentRuns)
		mux.HandleFunc("POST /api/v1/agent-runs", router.createAgentRun)
		mux.HandleFunc("GET /api/v1/agent-runs/{runId}", router.getAgentRun)
		mux.HandleFunc("POST /api/v1/agent-runs/{runId}/stop", router.stopAgentRun)
		mux.HandleFunc("POST /api/v1/agent-changes/{changeId}/accept", router.acceptAgentChange)
		mux.HandleFunc("POST /api/v1/agent-changes/{changeId}/reject", router.rejectAgentChange)
	} else {
		mux.HandleFunc("GET /api/v1/agent-runs", router.agentUnavailable)
		mux.HandleFunc("POST /api/v1/agent-runs", router.agentUnavailable)
		mux.HandleFunc("GET /api/v1/agent-runs/{runId}", router.agentUnavailable)
		mux.HandleFunc("POST /api/v1/agent-runs/{runId}/stop", router.agentUnavailable)
		mux.HandleFunc("POST /api/v1/agent-changes/{changeId}/accept", router.agentUnavailable)
		mux.HandleFunc("POST /api/v1/agent-changes/{changeId}/reject", router.agentUnavailable)
	}
	if router.audits != nil {
		mux.HandleFunc("GET /api/v1/audit-events", router.listAuditEvents)
		mux.HandleFunc("GET /api/v1/audit-events/{auditEventId}", router.getAuditEvent)
	}
	if router.undos != nil {
		mux.HandleFunc("POST /api/v1/audit-events/{auditEventId}/undo", router.undoAuditEvent)
	}
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
