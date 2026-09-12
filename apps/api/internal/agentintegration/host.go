package agentintegration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"dayorder.local/api/internal/agentassets"
	"dayorder.local/api/internal/agentgateway"
	"dayorder.local/api/internal/agenthost"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agentprovider"
	"dayorder.local/api/internal/agentskill"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/agenttool"
	"dayorder.local/api/internal/config"
	"dayorder.local/api/internal/httpapi"
	"dayorder.local/api/internal/observability"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/service"
	"dayorder.local/api/internal/testdb"
	"dayorder.local/api/internal/worker"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	deepSeekEndpoint     = "https://api.deepseek.com/chat/completions"
	defaultDeepSeekModel = "deepseek-v4-flash"
	workerPollInterval   = 25 * time.Millisecond
	databaseAuditTimeout = 5 * time.Second
	databaseSourceEnv    = "DAYORDER_AGENT_TEST_DB_SOURCE"
)

var (
	errProductionHost  = errors.New("production agent integration host is forbidden")
	integrationHMACKey = []byte("agent-integration-hmac-key-32-bytes")
)

type Config struct {
	Environment    config.Environment
	RealProvider   bool
	ProviderKey    string
	ProviderModel  string
	AllowedOrigins []string
}

type Host struct {
	apiURL     string
	server     *http.Server
	cancel     context.CancelFunc
	database   *agenttest.Database
	fixture    *testdb.Postgres
	serveDone  chan error
	workerDone chan error
	traces     *runtimeTraceRecorder
	logger     *slog.Logger
	dbSource   string
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErr   error
}

func Start(ctx context.Context, configuration Config) (_ *Host, resultErr error) {
	if configuration.Environment == config.Production {
		return nil, errProductionHost
	}
	if configuration.Environment != config.Development && configuration.Environment != config.Test {
		return nil, errors.New("agent integration host requires development or test environment")
	}
	allowed, err := allowedLoopbackOrigins(configuration.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	logger := observability.NewLogger(os.Stderr, "agent-integration", slog.LevelInfo)
	databaseSource := integrationDatabaseSource(os.Getenv(databaseSourceEnv))
	fixture, err := testdb.StartIsolated(ctx)
	config.ScrubConfigHubDatabaseEnvironment()
	if err != nil {
		return nil, fmt.Errorf("start owned integration database: %w", err)
	}
	logger.Info("owned PostgreSQL integration database created",
		"databaseName", fixture.DatabaseName(), "databaseSource", databaseSource, "outcome", "created",
	)
	var database *agenttest.Database
	defer func() {
		if resultErr == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if database != nil {
			resultErr = errors.Join(resultErr, database.Close(cleanupCtx))
		}
		resultErr = errors.Join(resultErr, closeOwnedIntegrationFixture(cleanupCtx, logger, fixture, databaseSource))
	}()

	database, err = agenttest.NewDatabase(ctx, fixture)
	if err != nil {
		return nil, fmt.Errorf("construct integration database: %w", err)
	}
	versionCtx, cancelVersion := context.WithTimeout(ctx, databaseAuditTimeout)
	serverVersion, versionErr := ownedIntegrationDatabaseVersion(versionCtx, database.Migrator)
	cancelVersion()
	if versionErr != nil {
		return nil, versionErr
	}
	logger.Info("owned PostgreSQL integration database version verified",
		"databaseName", fixture.DatabaseName(), "databaseSource", databaseSource,
		"serverVersion", serverVersion, "outcome", "verified",
	)
	profileID := "readonly-fake"
	if configuration.RealProvider {
		profileID = "readonly-deepseek"
		if strings.TrimSpace(configuration.ProviderKey) == "" {
			return nil, errors.New("real integration Provider key is required")
		}
	}
	seed, err := seedDatabase(ctx, database, profileID)
	if err != nil {
		return nil, err
	}

	metrics := observability.NewMetrics("agent-integration", nil, nil)
	faults := &faultControl{migrator: database.Migrator}
	apiBeginner := controlledBeginner{base: integrationPoolBeginner{pool: database.API}, control: faults}
	workerBeginner := controlledBeginner{base: integrationPoolBeginner{pool: database.Worker}, control: faults}
	calendarStore := controlledCalendarStore{
		CalendarStore: postgresstore.NewCalendarRepository(), control: faults,
	}
	apiServices, err := agenttest.BuildServices(database, config.DatabaseRoleAPI, agenttest.ServicesOptions{
		Profiles:      []string{profileID},
		Store:         controlledStore{Store: postgresstore.NewAgentExecutionRepository(), control: faults},
		CalendarStore: calendarStore,
		Beginner:      apiBeginner,
		Observer:      metrics, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct integration API services: %w", err)
	}
	workerServices, err := agenttest.BuildServices(database, config.DatabaseRoleWorker, agenttest.ServicesOptions{
		Profiles:      []string{profileID},
		Store:         controlledStore{Store: postgresstore.NewAgentExecutionRepository(), control: faults},
		CalendarStore: calendarStore,
		Beginner:      workerBeginner,
		Observer:      metrics, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct integration Worker services: %w", err)
	}

	apiCalendar, err := service.NewAgentCalendarReadService(apiServices.Runs, apiServices.Calendar)
	if err != nil {
		return nil, fmt.Errorf("construct integration API calendar reader: %w", err)
	}
	apiCalendar.SetObserver(metrics)
	workerCalendar, err := service.NewAgentCalendarReadService(workerServices.Runs, workerServices.Calendar)
	if err != nil {
		return nil, fmt.Errorf("construct integration Worker calendar reader: %w", err)
	}
	workerCalendar.SetObserver(metrics)

	adapter, err := integrationAdapter(configuration)
	if err != nil {
		return nil, err
	}
	controlledProvider := controlledAdapter{base: adapter, control: faults}
	tools, err := integrationTools()
	if err != nil {
		return nil, err
	}
	model := selectedModel(configuration)
	apiGateway, err := agentgateway.New(agentgateway.Config{
		Runs:     apiServices.Runs,
		Profiles: []agentgateway.Profile{{ID: profileID, Model: model, Adapter: controlledProvider}},
		Tools:    tools, Observer: metrics, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct integration API Provider gateway: %w", err)
	}
	timeoutBudget := agentprotocol.Budget{
		MaxSteps: 8, MaxTokens: 16000, MaxDurationMs: 1500,
		MaxWorkers: 1, MaxConcurrency: 1, MaxRepeatedToolCalls: 2,
	}
	timeoutServices, err := agenttest.BuildServices(database, config.DatabaseRoleAPI, agenttest.ServicesOptions{
		Profiles: []string{profileID}, Budget: timeoutBudget,
		Store:         controlledStore{Store: postgresstore.NewAgentExecutionRepository(), control: faults},
		CalendarStore: calendarStore,
		Beginner:      apiBeginner,
		Observer:      metrics, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct timeout integration API services: %w", err)
	}
	timeoutCalendar, err := service.NewAgentCalendarReadService(timeoutServices.Runs, timeoutServices.Calendar)
	if err != nil {
		return nil, fmt.Errorf("construct timeout integration calendar reader: %w", err)
	}
	timeoutCalendar.SetObserver(metrics)
	timeoutGateway, err := agentgateway.New(agentgateway.Config{
		Runs:     timeoutServices.Runs,
		Profiles: []agentgateway.Profile{{ID: profileID, Model: model, Adapter: controlledProvider}},
		Tools:    tools, Observer: metrics, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct timeout integration Provider gateway: %w", err)
	}
	workerGateway, err := agentgateway.New(agentgateway.Config{
		Runs:     workerServices.Runs,
		Profiles: []agentgateway.Profile{{ID: profileID, Model: model, Adapter: controlledProvider}},
		Tools:    tools, Observer: metrics, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct integration Worker Provider gateway: %w", err)
	}

	background, err := agenthost.NewBackground(agenthost.Config{
		Runs: workerServices.Runs, Calendar: workerCalendar, Gateway: workerGateway,
		Observer: metrics, Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("construct integration background Host: %w", err)
	}
	traces := newRuntimeTraceRecorder()
	readonlyHandler, err := worker.NewReadonlyAgentHandler(traceRecordingProcessor{base: background, traces: traces})
	if err != nil {
		return nil, err
	}
	outbox, err := postgresstore.NewOutboxRepository(database.Worker)
	if err != nil {
		return nil, fmt.Errorf("construct integration Outbox repository: %w", err)
	}
	runner, err := worker.NewRunnerWithOptions(controlledOutbox{
		base: outbox, faults: postgresOutboxFaultStore{pool: database.Migrator}, control: faults,
	}, map[string]worker.Handler{"agent.readonly.run.requested": runContextHandler{base: readonlyHandler}}, worker.RunnerOptions{BatchSize: 1})
	if err != nil {
		return nil, fmt.Errorf("construct integration Worker: %w", err)
	}

	accountsRepository, err := postgresstore.NewAccountRepository(database.API)
	if err != nil {
		return nil, err
	}
	accounts, err := service.NewAccountService(accountsRepository)
	if err != nil {
		return nil, err
	}
	sessions, err := service.NewSessionService(accountsRepository, accountsRepository, integrationHMACKey)
	if err != nil {
		return nil, err
	}
	devices, err := service.NewDeviceService(postgresstore.NewDeviceRepository(), apiServices.Transactor, apiServices.Audit)
	if err != nil {
		return nil, err
	}
	origins := make([]string, 0, len(allowed))
	for origin := range allowed {
		origins = append(origins, origin)
	}
	application, err := httpapi.NewAgentIntegrationRouter(httpapi.RouterOptions{
		Accounts: accounts, Sessions: sessions, Calendar: apiServices.Calendar, Devices: devices,
		AllowedOrigins: origins, Logger: logger, Metrics: metrics,
	}, httpapi.AgentIntegrationOptions{
		Environment: configuration.Environment, Runs: apiServices.Runs, Calendar: apiCalendar, Gateway: apiGateway,
	})
	if err != nil {
		return nil, fmt.Errorf("construct integration HTTP router: %w", err)
	}
	timeoutApplication, err := httpapi.NewAgentIntegrationRouter(httpapi.RouterOptions{
		Accounts: accounts, Sessions: sessions, Calendar: timeoutServices.Calendar, Devices: devices,
		AllowedOrigins: origins, Logger: logger, Metrics: metrics,
	}, httpapi.AgentIntegrationOptions{
		Environment: configuration.Environment, Runs: timeoutServices.Runs, Calendar: timeoutCalendar, Gateway: timeoutGateway,
	})
	if err != nil {
		return nil, fmt.Errorf("construct timeout integration HTTP router: %w", err)
	}
	handler := newControlHandler(application, timeoutApplication, sessions, apiServices, seed, faults, allowed, traces)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on integration loopback: %w", err)
	}

	hostCtx, cancel := context.WithCancel(ctx)
	host := &Host{
		apiURL: "http://" + listener.Addr().String(),
		cancel: cancel, database: database, fixture: fixture,
		serveDone: make(chan error, 1), workerDone: make(chan error, 1), traces: traces,
		logger: logger, dbSource: databaseSource, closeDone: make(chan struct{}),
	}
	host.server = &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context { return hostCtx },
	}
	go func() {
		serveErr := host.server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		host.serveDone <- serveErr
	}()
	go func() { host.workerDone <- runWorkerLoop(hostCtx, runner) }()
	return host, nil
}

func (host *Host) APIURL() string {
	if host == nil {
		return ""
	}
	return host.apiURL
}

func (host *Host) Close(ctx context.Context) error {
	if host == nil {
		return nil
	}
	host.closeOnce.Do(func() {
		defer close(host.closeDone)
		if ctx == nil {
			ctx = context.Background()
		}
		host.cancel()
		var shutdownErr error
		if host.server != nil {
			shutdownErr = host.server.Shutdown(ctx)
		}
		var serveErr, workerErr error
		select {
		case serveErr = <-host.serveDone:
		case <-ctx.Done():
			logOwnedIntegrationCleanupFailure(host.logger, host.fixture, host.dbSource)
			host.closeErr = errors.Join(shutdownErr, fmt.Errorf(
				"cleanup incomplete for temporary database %s: HTTP server did not exit", host.fixture.DatabaseName(),
			))
			return
		}
		select {
		case workerErr = <-host.workerDone:
		case <-ctx.Done():
			logOwnedIntegrationCleanupFailure(host.logger, host.fixture, host.dbSource)
			host.closeErr = errors.Join(shutdownErr, serveErr, fmt.Errorf(
				"cleanup incomplete for temporary database %s: integration Worker did not exit", host.fixture.DatabaseName(),
			))
			return
		}
		host.traces.Close()
		databaseErr := host.database.Close(ctx)
		fixtureErr := closeOwnedIntegrationFixture(ctx, host.logger, host.fixture, host.dbSource)
		host.closeErr = errors.Join(shutdownErr, serveErr, workerErr, databaseErr, fixtureErr)
	})
	<-host.closeDone
	return host.closeErr
}

type ownedIntegrationFixture interface {
	DatabaseName() string
	Close(context.Context) error
}

func integrationDatabaseSource(value string) string {
	if strings.TrimSpace(value) == "confighub" {
		return "confighub"
	}
	return "docker"
}

func ownedIntegrationDatabaseVersion(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	if pool == nil {
		return "", errors.New("inspect owned integration database server version: PostgreSQL operation failed")
	}
	var version string
	if err := pool.QueryRow(ctx, "SELECT pg_catalog.current_setting('server_version')").Scan(&version); err != nil || strings.TrimSpace(version) == "" {
		return "", errors.New("inspect owned integration database server version: PostgreSQL operation failed")
	}
	return strings.TrimSpace(version), nil
}

func closeOwnedIntegrationFixture(ctx context.Context, logger *slog.Logger, fixture ownedIntegrationFixture, databaseSource string) error {
	err := fixture.Close(ctx)
	if err != nil {
		logOwnedIntegrationCleanupFailure(logger, fixture, databaseSource)
		return err
	}
	if logger != nil {
		logger.Info("deleted isolated PostgreSQL database after guarded cleanup",
			"databaseName", fixture.DatabaseName(), "databaseSource", databaseSource, "outcome", "guarded_cleanup",
		)
	}
	return nil
}

func logOwnedIntegrationCleanupFailure(logger *slog.Logger, fixture ownedIntegrationFixture, databaseSource string) {
	if logger == nil || fixture == nil {
		return
	}
	logger.Error("isolated PostgreSQL database cleanup failed",
		"databaseName", fixture.DatabaseName(), "databaseSource", databaseSource, "outcome", "failed",
	)
}

func integrationAdapter(configuration Config) (agentprovider.Adapter, error) {
	if !configuration.RealProvider {
		return agentprovider.NewFake(agentprovider.FakeConfig{
			Window: agentprotocol.CalendarReadInput{
				Start: agentprotocol.DateTime(fixtureWindowStart.Format(time.RFC3339)),
				End:   agentprotocol.DateTime(fixtureWindowEnd.Format(time.RFC3339)), Limit: 20,
			}, RecoverToolTimeout: true,
		})
	}
	adapter, err := agentprovider.NewDeepSeek(agentprovider.DeepSeekConfig{
		Endpoint: deepSeekEndpoint, APIKey: configuration.ProviderKey,
	})
	if err != nil {
		return nil, fmt.Errorf("construct DeepSeek integration adapter: %w", err)
	}
	return adapter, nil
}

func selectedModel(configuration Config) string {
	if !configuration.RealProvider {
		return "fixture-calendar-overview"
	}
	if model := strings.TrimSpace(configuration.ProviderModel); model != "" {
		return model
	}
	return defaultDeepSeekModel
}

func integrationTools() ([]agentprotocol.ToolSpec, error) {
	calendar, err := agentassets.CalendarReadSpec()
	if err != nil {
		return nil, fmt.Errorf("load integration calendar Tool: %w", err)
	}
	profile, err := agentskill.ParseBundle(agentassets.CalendarOverviewBundle())
	if err != nil {
		return nil, fmt.Errorf("load integration calendar Skill: %w", err)
	}
	registry, err := agentskill.NewRegistry([]agentskill.SkillProfile{profile})
	if err != nil {
		return nil, err
	}
	bindings := agentskill.MetaBindings(registry, agentprotocol.CapabilitySnapshot{}, &agenttool.Registry{}, agenttool.Policy{})
	tools := []agentprotocol.ToolSpec{calendar}
	for _, binding := range bindings {
		tools = append(tools, binding.Spec())
	}
	return tools, nil
}

func runWorkerLoop(ctx context.Context, runner *worker.Runner) error {
	backoff := workerPollInterval
	for {
		if ctx.Err() != nil {
			return nil
		}
		_, err := runner.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			backoff = workerPollInterval
		} else if backoff < 500*time.Millisecond {
			backoff *= 2
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func allowedLoopbackOrigins(values []string) (map[string]struct{}, error) {
	allowed := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSuffix(strings.TrimSpace(value), "/")
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || !loopbackHost(parsed.Host) {
			return nil, errors.New("allowed origins must be explicit loopback HTTP origins")
		}
		allowed[value] = struct{}{}
	}
	return allowed, nil
}

func trustedDiagnosticRequest(request *http.Request, allowed map[string]struct{}) bool {
	if request == nil || !loopbackHost(request.Host) {
		return false
	}
	origin := strings.TrimSuffix(strings.TrimSpace(request.Header.Get("Origin")), "/")
	if request.Method == http.MethodPost && origin == "" {
		return false
	}
	if origin == "" {
		return request.Method == http.MethodGet
	}
	_, ok := allowed[origin]
	return ok
}

func loopbackHost(value string) bool {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}
