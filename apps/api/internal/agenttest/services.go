package agenttest

import (
	"errors"
	"log/slog"
	"testing"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/database"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"

	"github.com/jackc/pgx/v5/pgxpool"
)

var serviceTestHMACKey = []byte("agent-test-hmac-key-32-byte-value")

type Services struct {
	Runs       *service.AgentReadonlyService
	Calendar   *service.CalendarService
	Transactor *database.Transactor
	Commands   *service.CommandService
	Sync       *service.SyncService
	Audit      *service.AuditService
}

type ServicesOptions struct {
	Profiles      []string
	Budget        agentprotocol.Budget
	Store         agentexecution.Store
	CalendarStore service.CalendarStore
	Beginner      database.Beginner
	Observer      agentexecution.Observer
	Logger        *slog.Logger
}

func NewServices(t testing.TB, fixture *Database, role config.DatabaseRole) Services {
	t.Helper()
	services, err := BuildServices(fixture, role, ServicesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return *services
}

func BuildServices(fixture *Database, role config.DatabaseRole, options ServicesOptions) (*Services, error) {
	if fixture == nil {
		return nil, errors.New("agent service fixture is required")
	}
	var pool *pgxpool.Pool
	switch role {
	case config.DatabaseRoleAPI:
		pool = fixture.API
	case config.DatabaseRoleWorker:
		pool = fixture.Worker
	default:
		return nil, errors.New("agent service role must be API or Worker")
	}
	if pool == nil {
		return nil, errors.New("agent service role pool is required")
	}
	var transactor *database.Transactor
	if options.Beginner == nil {
		var err error
		transactor, err = database.NewPoolTransactor(pool)
		if err != nil {
			return nil, errors.New("create agent test transactor")
		}
	} else {
		transactor = database.NewTransactor(options.Beginner)
	}
	idempotency, err := service.NewIdempotencyService(postgresstore.NewIdempotencyRepository())
	if err != nil {
		return nil, errors.New("create agent test idempotency service")
	}
	syncWriter, err := service.NewSyncService(postgresstore.NewSyncRepository(), transactor, serviceTestHMACKey)
	if err != nil {
		return nil, errors.New("create agent test sync service")
	}
	auditWriter, err := service.NewAuditService(postgresstore.NewAuditRepository())
	if err != nil {
		return nil, errors.New("create agent test audit service")
	}
	commands, err := service.NewCommandService(transactor, idempotency, syncWriter, auditWriter, postgresstore.NewOutboxWriter())
	if err != nil {
		return nil, errors.New("create agent test command service")
	}
	cursors, err := service.NewResourceCursorCodec(serviceTestHMACKey)
	if err != nil {
		return nil, errors.New("create agent test cursor codec")
	}
	calendarStore := options.CalendarStore
	if calendarStore == nil {
		calendarStore = postgresstore.NewCalendarRepository()
	}
	calendar, err := service.NewCalendarService(calendarStore, transactor, commands, cursors)
	if err != nil {
		return nil, errors.New("create agent test calendar service")
	}
	tool, err := agentassets.CalendarReadSpec()
	if err != nil {
		return nil, errors.New("load builtin calendar tool")
	}
	profile, err := agentskill.ParseBundle(agentassets.CalendarOverviewBundle())
	if err != nil {
		return nil, errors.New("load builtin calendar skill")
	}
	skill := agentprotocol.SkillRef{
		Name: profile.Manifest.Name, Version: profile.Manifest.Version, Digest: profile.Descriptor.Digest,
	}
	capabilities := make(map[agentprotocol.ExecutionMode]agentprotocol.CapabilitySnapshot, 2)
	for _, mode := range []agentprotocol.ExecutionMode{agentprotocol.ExecutionModeForeground, agentprotocol.ExecutionModeBackground} {
		capabilities[mode] = agentprotocol.CapabilitySnapshot{
			RuntimeVersion: "2.0.0", ExecutionMode: mode,
			ToolIds: []string{"skill_list", "skill_load", tool.ID}, Skills: []agentprotocol.SkillRef{skill},
			Scope: agentprotocol.AgentScope{Domains: []string{"calendar"}},
		}
	}
	profiles := options.Profiles
	if len(profiles) == 0 {
		profiles = []string{"readonly-default"}
	}
	store := options.Store
	if store == nil {
		store = postgresstore.NewAgentExecutionRepository()
	}
	runs, err := service.NewAgentReadonlyService(service.AgentReadonlyConfig{
		Store: store, Transactor: transactor, Commands: commands,
		SyncWriter: syncWriter, AuditWriter: auditWriter, Capabilities: capabilities,
		Profiles: profiles, Budget: options.Budget, Observer: options.Observer, Logger: options.Logger,
	})
	if err != nil {
		return nil, errors.New("create agent test readonly run service")
	}
	return &Services{
		Runs: runs, Calendar: calendar, Transactor: transactor,
		Commands: commands, Sync: syncWriter, Audit: auditWriter,
	}, nil
}
