package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"dayorder.local/api/internal/agentexecution"
	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/database"
	dbmigrations "dayorder.local/api/internal/migrations"
	"dayorder.local/api/internal/model"
	postgresstore "dayorder.local/api/internal/postgres"
	"dayorder.local/api/internal/testdb"
	schema "dayorder.local/api/migrations"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ agentexecution.Store = postgresstore.NewAgentExecutionRepository()

func TestReadonlyExecutionTablesUseRLS(t *testing.T) {
	f := agenttest.Open(t)
	ctx := context.Background()
	for _, name := range []string{"agent_run_executions", "agent_run_operations"} {
		var enabled bool
		err := f.Migrator.QueryRow(ctx,
			`SELECT relrowsecurity FROM pg_class WHERE oid=to_regclass($1)`, "dayorder."+name).Scan(&enabled)
		if err != nil || !enabled {
			t.Fatalf("%s RLS: %v, %v", name, enabled, err)
		}
	}
}

func TestReadonlyExecutionRolePrivilegesAndIsolation(t *testing.T) {
	f := agenttest.Open(t)
	ctx := context.Background()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"api", f.API}, {"worker", f.Worker}} {
		t.Run(role.name, func(t *testing.T) {
			runID := uuid.New()
			token := uuid.New()
			operationID := uuid.NewString()
			insertRunAsMigrator(t, ctx, f, f.UserA, runID)

			withCommittedUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, executionInsertSQL, f.UserA, runID, token); err != nil {
					t.Fatalf("insert own execution: %v", err)
				}
				if _, err := tx.Exec(ctx, operationInsertSQL, f.UserA, runID, operationID); err != nil {
					t.Fatalf("insert own operation: %v", err)
				}
			})

			withCommittedUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
				var executions, operations int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM dayorder.agent_run_executions WHERE user_id=$1 AND run_id=$2`, f.UserA, runID).Scan(&executions); err != nil {
					t.Fatal(err)
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2`, f.UserA, runID).Scan(&operations); err != nil {
					t.Fatal(err)
				}
				if executions != 1 || operations != 1 {
					t.Fatalf("own rows = executions %d operations %d, want 1 and 1", executions, operations)
				}

				if _, err := tx.Exec(ctx, `UPDATE dayorder.agent_run_executions SET known_usage='{"inputTokens":2,"outputTokens":3,"totalTokens":5}', reserved_tokens=5, usage_complete=true, result_origin='server_runtime' WHERE user_id=$1 AND run_id=$2`, f.UserA, runID); err != nil {
					t.Fatalf("update mutable execution controls: %v", err)
				}
				if _, err := tx.Exec(ctx, `UPDATE dayorder.agent_run_operations SET state='completed', attempts=1, reserved_tokens=0, usage='{"inputTokens":2,"outputTokens":3,"totalTokens":5}', usage_complete=true, error_code='', finished_at=now() WHERE user_id=$1 AND run_id=$2 AND kind='provider_turn' AND operation_id=$3`, f.UserA, runID, operationID); err != nil {
					t.Fatalf("update mutable operation controls: %v", err)
				}
			})

			withUserTransaction(t, ctx, role.pool, f.UserB, func(tx pgx.Tx) {
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM dayorder.agent_run_executions WHERE user_id=$1 AND run_id=$2`, f.UserA, runID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("cross-user execution read = %d, %v; want zero rows", count, err)
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2`, f.UserA, runID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("cross-user operation read = %d, %v; want zero rows", count, err)
				}
			})
			withUserTransaction(t, ctx, role.pool, f.UserB, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, executionInsertSQL, f.UserA, runID, uuid.New()); sqlState(err) != "42501" {
					t.Fatalf("cross-user execution insert SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
			withUserTransaction(t, ctx, role.pool, f.UserB, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, operationInsertSQL, f.UserA, runID, "cross-owner-operation"); sqlState(err) != "42501" {
					t.Fatalf("cross-user operation insert SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
			withBarePGXRollbackTransaction(t, ctx, role.pool, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, executionInsertSQL, f.UserA, runID, uuid.New()); sqlState(err) != "42501" {
					t.Fatalf("missing-context execution insert SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
			withBarePGXRollbackTransaction(t, ctx, role.pool, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, operationInsertSQL, f.UserA, runID, "missing-context-operation"); sqlState(err) != "42501" {
					t.Fatalf("missing-context operation insert SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
			withUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, executionInsertSQL, f.UserA, uuid.New(), uuid.New()); sqlState(err) != "23503" {
					t.Fatalf("execution foreign-key SQLSTATE = %q, want 23503", sqlState(err))
				}
			})
			withUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, operationInsertSQL, f.UserA, uuid.New(), "missing-execution"); sqlState(err) != "23503" {
					t.Fatalf("operation foreign-key SQLSTATE = %q, want 23503", sqlState(err))
				}
			})

			for _, column := range []string{"execution_mode", "protocol_version", "runtime_version", "model_profile", "timezone", "capabilities", "budget", "deadline"} {
				withUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
					query := fmt.Sprintf("UPDATE dayorder.agent_run_executions SET %s=%s WHERE user_id=$1 AND run_id=$2", column, frozenExecutionValue(column))
					if _, err := tx.Exec(ctx, query, f.UserA, runID); sqlState(err) != "42501" {
						t.Fatalf("update frozen execution column %s SQLSTATE = %q, want 42501", column, sqlState(err))
					}
				})
			}
			withUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, `UPDATE dayorder.agent_runs SET scope='{"domains":[]}' WHERE user_id=$1 AND id=$2`, f.UserA, runID); sqlState(err) != "42501" {
					t.Fatalf("update frozen run scope SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
			for _, assignment := range []string{"kind='calendar_read'", "operation_id='replacement'", "operation_hash=decode(repeat('22',32),'hex')", "started_at=started_at-interval '1 hour'"} {
				withUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
					query := "UPDATE dayorder.agent_run_operations SET " + assignment + " WHERE user_id=$1 AND run_id=$2 AND kind='provider_turn' AND operation_id=$3"
					if _, err := tx.Exec(ctx, query, f.UserA, runID, operationID); sqlState(err) != "42501" {
						t.Fatalf("update frozen operation key/hash with %q SQLSTATE = %q, want 42501", assignment, sqlState(err))
					}
				})
			}

			withUserTransaction(t, ctx, role.pool, f.UserA, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, `DELETE FROM dayorder.agent_run_operations WHERE user_id=$1 AND run_id=$2`, f.UserA, runID); sqlState(err) != "42501" {
					t.Fatalf("delete operation SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
		})
	}
}

func TestReadonlyExecutionConstraintsRejectMalformedLedgerRows(t *testing.T) {
	f := agenttest.Open(t)
	ctx := context.Background()
	runID := uuid.New()
	insertRunAsMigrator(t, ctx, f, f.UserA, runID)
	if _, err := f.Migrator.Exec(ctx, executionInsertSQL, f.UserA, runID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Migrator.Exec(ctx, operationInsertSQL, f.UserA, runID, "turn-constraints"); err != nil {
		t.Fatal(err)
	}

	for _, assignment := range []string{
		"capabilities='[]'::jsonb",
		"budget='[]'::jsonb",
		"known_usage='[]'::jsonb",
		"reserved_tokens=-1",
	} {
		expectCheckViolation(t, ctx, f.Migrator,
			"UPDATE dayorder.agent_run_executions SET "+assignment+" WHERE user_id=$1 AND run_id=$2", f.UserA, runID)
	}
	for _, assignment := range []string{
		"kind='unexpected'",
		"state='unexpected'",
		"operation_hash=decode('11','hex')",
		"attempts=-1",
		"reserved_tokens=-1",
		"usage='[]'::jsonb",
	} {
		expectCheckViolation(t, ctx, f.Migrator,
			"UPDATE dayorder.agent_run_operations SET "+assignment+" WHERE user_id=$1 AND run_id=$2", f.UserA, runID)
	}
}

func TestReadonlyExecutionRepositoryPersistsRecordsOperationsAndCAS(t *testing.T) {
	f := agenttest.Open(t)
	ctx := context.Background()
	repository := postgresstore.NewAgentExecutionRepository()
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := readonlyRecord(f.UserA, uuid.New(), uuid.New(), now)

	withStoreTransaction(t, ctx, f.API, f.UserA, func(tx database.Tx) {
		if err := repository.LockAccount(ctx, tx, f.UserA); err != nil {
			t.Fatalf("lock account: %v", err)
		}
		if err := repository.Create(ctx, tx, record); err != nil {
			t.Fatalf("create record: %v", err)
		}
	})

	withStoreTransaction(t, ctx, f.API, f.UserA, func(tx database.Tx) {
		stored, err := repository.Get(ctx, tx, f.UserA, record.Run.ID, false)
		if err != nil {
			t.Fatalf("get record: %v", err)
		}
		if stored.Run.Version != 1 || stored.Execution.Token != record.Execution.Token || stored.Execution.Capabilities.RuntimeVersion != "2.0.0" || stored.Execution.Budget.MaxTokens != 1000 {
			t.Fatalf("stored record = %#v", stored)
		}
		active, err := repository.Active(ctx, tx, f.UserA)
		if err != nil || len(active) != 1 || active[0].Run.ID != record.Run.ID {
			t.Fatalf("active records = %#v, %v", active, err)
		}
		count, err := repository.CountCreatedSince(ctx, tx, f.UserA, now.Add(-time.Minute))
		if err != nil || count != 1 {
			t.Fatalf("created count = %d, %v", count, err)
		}
	})

	updated := record
	updated.Run.Status = "completed"
	updated.Run.Version = 2
	updated.Run.StartedAt = ptrTime(now)
	updated.Run.FinishedAt = ptrTime(now.Add(time.Second))
	updated.Run.Summary = ptrString("finished")
	updated.Execution.KnownUsage = agentprotocol.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
	updated.Execution.ReservedTokens = 0
	updated.Execution.UsageComplete = true
	updated.Execution.ResultOrigin = "server_runtime"
	updated.Execution.ModelProfile = "must-remain-readonly-default"
	updated.Execution.Deadline = now.Add(24 * time.Hour)

	withStoreTransaction(t, ctx, f.Worker, f.UserA, func(tx database.Tx) {
		if err := repository.Save(ctx, tx, updated, 1, record.Execution.Token); err != nil {
			t.Fatalf("save record: %v", err)
		}
		if err := repository.Save(ctx, tx, updated, 1, record.Execution.Token); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("stale version save error = %v", err)
		}
		updated.Run.Status = "failed"
		updated.Run.Summary = ptrString("must not replace completed state")
		if err := repository.Save(ctx, tx, updated, 2, uuid.New()); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("stale token save error = %v", err)
		}
	})

	operation := agentexecution.Operation{
		UserID: f.UserA, RunID: record.Run.ID, Kind: "provider_turn", ID: "turn-1", State: "running",
		Hash: [32]byte{1, 2, 3}, ReservedTokens: 1000, Usage: agentprotocol.Usage{}, StartedAt: now,
	}
	withStoreTransaction(t, ctx, f.Worker, f.UserA, func(tx database.Tx) {
		if err := repository.PutOperation(ctx, tx, operation, ""); err != nil {
			t.Fatalf("insert operation: %v", err)
		}
	})
	withStoreRollbackTransaction(t, ctx, f.Worker, f.UserA, func(tx database.Tx) {
		if err := repository.PutOperation(ctx, tx, operation, ""); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("duplicate operation error = %v", err)
		}
	})
	withStoreTransaction(t, ctx, f.Worker, f.UserA, func(tx database.Tx) {
		operation.State = "completed"
		operation.Attempts = 1
		operation.ReservedTokens = 0
		operation.Usage = agentprotocol.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
		operation.UsageComplete = true
		operation.FinishedAt = now.Add(time.Second)
		if err := repository.PutOperation(ctx, tx, operation, "running"); err != nil {
			t.Fatalf("complete operation: %v", err)
		}
		if err := repository.PutOperation(ctx, tx, operation, "running"); !errors.Is(err, model.ErrConflict) {
			t.Fatalf("stale operation error = %v", err)
		}
		operations, err := repository.Operations(ctx, tx, f.UserA, record.Run.ID)
		if err != nil || len(operations) != 1 || operations[0].State != "completed" || operations[0].Hash != operation.Hash {
			t.Fatalf("operations = %#v, %v", operations, err)
		}
	})

	refs := []model.AgentSourceRefDraft{{EntityType: "note", EntityID: uuid.New(), EntityVersion: 3, LabelSnapshot: "Source"}}
	steps := []model.AgentStepDraft{{Title: "Read source", Detail: "Done", Metadata: []byte(`{"kind":"read"}`)}}
	withStoreTransaction(t, ctx, f.Worker, f.UserA, func(tx database.Tx) {
		if err := repository.AddRefs(ctx, tx, f.UserA, record.Run.ID, refs); err != nil {
			t.Fatalf("add refs: %v", err)
		}
		if err := repository.AddRefs(ctx, tx, f.UserA, record.Run.ID, refs); err != nil {
			t.Fatalf("add duplicate refs: %v", err)
		}
		if err := repository.AddSteps(ctx, tx, f.UserA, record.Run.ID, steps); err != nil {
			t.Fatalf("add steps: %v", err)
		}
	})

	withStoreTransaction(t, ctx, f.API, f.UserA, func(tx database.Tx) {
		stored, err := repository.Get(ctx, tx, f.UserA, record.Run.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Run.Version != 2 || stored.Run.Status != "completed" || stored.Execution.ModelProfile != "readonly-default" || !stored.Execution.Deadline.Equal(record.Execution.Deadline) {
			t.Fatalf("saved record = %#v", stored)
		}
		if len(stored.Run.SourceRefs) != 1 || len(stored.Run.Steps) != 1 || stored.Run.Steps[0].SequenceNo != 1 {
			t.Fatalf("hydrated refs/steps = %#v / %#v", stored.Run.SourceRefs, stored.Run.Steps)
		}
	})
}

func TestReadonlyExecutionSaveRotatesTokenWithCASForBothRoles(t *testing.T) {
	f := agenttest.Open(t)
	ctx := context.Background()
	repository := postgresstore.NewAgentExecutionRepository()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"api", f.API}, {"worker", f.Worker}} {
		t.Run(role.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			record := readonlyRecord(f.UserA, uuid.New(), uuid.New(), now)
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.Create(ctx, tx, record); err != nil {
					t.Fatal(err)
				}
			})

			oldToken := record.Execution.Token
			newToken := uuid.New()
			rotated := record
			rotated.Run.Status = "reading"
			rotated.Execution.Token = newToken
			rotated.Execution.KnownUsage = agentprotocol.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
			rotated.Execution.ReservedTokens = 995
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.Save(ctx, tx, rotated, 1, oldToken); err != nil {
					t.Fatalf("rotate execution token: %v", err)
				}
			})

			conflicting := rotated
			conflicting.Run.Status = "failed"
			conflicting.Run.Summary = ptrString("must not persist")
			conflicting.Execution.Token = uuid.New()
			conflicting.Execution.KnownUsage = agentprotocol.Usage{InputTokens: 90, OutputTokens: 9, TotalTokens: 99}
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.Save(ctx, tx, conflicting, 2, oldToken); !errors.Is(err, model.ErrConflict) {
					t.Fatalf("old-token save error = %v, want conflict", err)
				}
				if err := repository.Save(ctx, tx, conflicting, 1, newToken); !errors.Is(err, model.ErrConflict) {
					t.Fatalf("wrong-version save error = %v, want conflict", err)
				}
			})

			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				stored, err := repository.Get(ctx, tx, f.UserA, record.Run.ID, false)
				if err != nil {
					t.Fatal(err)
				}
				if stored.Run.Version != 2 || stored.Run.Status != "reading" || stored.Run.Summary != nil {
					t.Fatalf("run after rejected saves = %#v", stored.Run)
				}
				if stored.Execution.Token != newToken || stored.Execution.KnownUsage.TotalTokens != 5 || stored.Execution.ReservedTokens != 995 {
					t.Fatalf("execution after rejected saves = %#v", stored.Execution)
				}
			})
		})
	}
}

func TestReadonlyExecutionOperationCASPreservesStartForBothRoles(t *testing.T) {
	f := agenttest.Open(t)
	ctx := context.Background()
	repository := postgresstore.NewAgentExecutionRepository()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"api", f.API}, {"worker", f.Worker}} {
		t.Run(role.name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Microsecond)
			record := readonlyRecord(f.UserA, uuid.New(), uuid.New(), now)
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.Create(ctx, tx, record); err != nil {
					t.Fatal(err)
				}
			})
			operation := agentexecution.Operation{
				UserID: f.UserA, RunID: record.Run.ID, Kind: "provider_turn", ID: role.name + "-turn",
				State: "running", Hash: [32]byte{4, 5, 6}, ReservedTokens: 1000, StartedAt: now,
			}
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.PutOperation(ctx, tx, operation, ""); err != nil {
					t.Fatal(err)
				}
			})
			withStoreRollbackTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.PutOperation(ctx, tx, operation, ""); !errors.Is(err, model.ErrConflict) {
					t.Fatalf("duplicate operation error = %v, want conflict", err)
				}
			})

			originalStartedAt := operation.StartedAt
			operation.State = "completed"
			operation.Attempts = 1
			operation.ReservedTokens = 0
			operation.Usage = agentprotocol.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
			operation.UsageComplete = true
			operation.StartedAt = originalStartedAt.Add(500 * time.Millisecond)
			operation.FinishedAt = originalStartedAt.Add(time.Second)
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.PutOperation(ctx, tx, operation, "running"); err != nil {
					t.Fatalf("complete operation: %v", err)
				}
				if err := repository.PutOperation(ctx, tx, operation, "running"); !errors.Is(err, model.ErrConflict) {
					t.Fatalf("stale operation error = %v, want conflict", err)
				}
			})
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				operations, err := repository.Operations(ctx, tx, f.UserA, record.Run.ID)
				if err != nil || len(operations) != 1 {
					t.Fatalf("operations = %#v, %v", operations, err)
				}
				if operations[0].State != "completed" || !operations[0].StartedAt.Equal(originalStartedAt) || !operations[0].FinishedAt.Equal(originalStartedAt.Add(time.Second)) {
					t.Fatalf("completed operation = %#v", operations[0])
				}
			})
		})
	}
}

func TestReadonlyExecutionRepositoryRejectsMissingContextCrossOwnerAndBadForeignKey(t *testing.T) {
	f := agenttest.Open(t)
	ctx := context.Background()
	repository := postgresstore.NewAgentExecutionRepository()

	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{"api", f.API}, {"worker", f.Worker}} {
		t.Run(role.name, func(t *testing.T) {
			record := readonlyRecord(f.UserA, uuid.New(), uuid.New(), time.Now().UTC())
			withStoreTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				if err := repository.Create(ctx, tx, record); err != nil {
					t.Fatal(err)
				}
			})

			withBareRollbackTransaction(t, ctx, role.pool, func(tx database.Tx) {
				missingContext := readonlyRecord(f.UserA, uuid.New(), uuid.New(), time.Now().UTC())
				if err := repository.Create(ctx, tx, missingContext); sqlState(err) != "42501" {
					t.Fatalf("missing-context create SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
			withStoreRollbackTransaction(t, ctx, role.pool, f.UserB, func(tx database.Tx) {
				crossOwner := readonlyRecord(f.UserA, uuid.New(), uuid.New(), time.Now().UTC())
				if err := repository.Create(ctx, tx, crossOwner); sqlState(err) != "42501" {
					t.Fatalf("cross-owner create SQLSTATE = %q, want 42501", sqlState(err))
				}
			})
			withStoreRollbackTransaction(t, ctx, role.pool, f.UserA, func(tx database.Tx) {
				badForeignKey := readonlyRecord(f.UserA, uuid.New(), uuid.New(), time.Now().UTC())
				badForeignKey.Execution.RunID = uuid.New()
				if err := repository.Create(ctx, tx, badForeignKey); !errors.Is(err, model.ErrNotFound) {
					t.Fatalf("bad execution foreign key error = %v, want not found", err)
				}
			})
			withStoreTransaction(t, ctx, role.pool, f.UserB, func(tx database.Tx) {
				if _, err := repository.Get(ctx, tx, f.UserA, record.Run.ID, false); !errors.Is(err, model.ErrNotFound) {
					t.Fatalf("cross-owner get error = %v, want not found", err)
				}
			})
		})
	}
}

func TestReadonlyExecutionUpgradeFrom000008PreservesExistingRun(t *testing.T) {
	fixture := testdb.StartIsolatedForTest(t)
	logFixtureCleanup(t, fixture)

	source, err := iofs.New(schema.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.NewWithSourceInstance("iofs", source, fixture.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Steps(8); err != nil {
		_, _ = runner.Close()
		t.Fatalf("apply migrations through 000008: %v", err)
	}
	if sourceErr, databaseErr := runner.Close(); sourceErr != nil || databaseErr != nil {
		t.Fatalf("close 000008 migrator: source=%v database=%v", sourceErr, databaseErr)
	}

	ctx := context.Background()
	pool := openTestPool(t, ctx, fixture.MigrationURL, 1)
	var serverVersion string
	if err = pool.QueryRow(ctx, "SHOW server_version").Scan(&serverVersion); err != nil {
		pool.Close()
		t.Fatal("read isolated PostgreSQL server version")
	}
	t.Logf("created isolated PostgreSQL database %s on PostgreSQL %s", fixture.DatabaseName(), serverVersion)
	userID, runID := uuid.New(), uuid.New()
	email := "agent-upgrade-" + userID.String() + "@example.com"
	if _, err = pool.Exec(ctx, `
INSERT INTO dayorder.users (id,email,normalized_email,display_name,password_hash,status,email_verified_at)
VALUES ($1,$2,$2,'Upgrade User','hash','active',now());
`, userID, email); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO dayorder.agent_runs (id,user_id,intent,status,action_mode,scope) VALUES ($1,$2,'legacy readonly run','ready','read','{}')`, runID, userID); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()

	if err = dbmigrations.Up(fixture.MigrationURL); err != nil {
		t.Fatalf("upgrade 000008 fixture to 000009: %v", err)
	}
	pool = openTestPool(t, ctx, fixture.MigrationURL, 1)
	defer pool.Close()
	var intent string
	var rls bool
	if err = pool.QueryRow(ctx, `SELECT intent FROM dayorder.agent_runs WHERE user_id=$1 AND id=$2`, userID, runID).Scan(&intent); err != nil || intent != "legacy readonly run" {
		t.Fatalf("legacy run after upgrade = %q, %v", intent, err)
	}
	if err = pool.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE oid='dayorder.agent_run_executions'::regclass`).Scan(&rls); err != nil || !rls {
		t.Fatalf("000009 execution RLS = %t, %v", rls, err)
	}
}

const executionInsertSQL = `
INSERT INTO dayorder.agent_run_executions (
    user_id, run_id, token, execution_mode, protocol_version, runtime_version,
    model_profile, timezone, capabilities, budget, deadline, known_usage,
    reserved_tokens, usage_complete, result_origin
) VALUES ($1,$2,$3,'background','2.0','2.0.0','readonly-default','UTC',
    '{"executionMode":"background","runtimeVersion":"2.0.0","scope":{"domains":[]},"skills":[],"toolIds":[]}',
    '{"maxConcurrency":1,"maxDurationMs":60000,"maxRepeatedToolCalls":2,"maxSteps":8,"maxTokens":1000,"maxWorkers":0}',
    now()+interval '1 minute','{"inputTokens":0,"outputTokens":0,"totalTokens":0}',1000,false,'client_reported')`

const operationInsertSQL = `
INSERT INTO dayorder.agent_run_operations (
    user_id, run_id, kind, operation_id, state, operation_hash, attempts,
    reserved_tokens, usage, usage_complete, error_code, started_at, finished_at
) VALUES ($1,$2,'provider_turn',$3,'running',decode(repeat('11',32),'hex'),0,1000,
    '{"inputTokens":0,"outputTokens":0,"totalTokens":0}',false,'',now(),NULL)`

func insertRunAsMigrator(t testing.TB, ctx context.Context, f *agenttest.Database, userID, runID uuid.UUID) {
	t.Helper()
	if _, err := f.Migrator.Exec(ctx, `
INSERT INTO dayorder.agent_runs (id,user_id,intent,status,action_mode,scope)
VALUES ($1,$2,'readonly fixture','ready','read','{}')`, runID, userID); err != nil {
		t.Fatal(err)
	}
}

func withUserTransaction(t testing.TB, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, operation func(pgx.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT dayorder.set_user_context($1)`, userID); err != nil {
		t.Fatal(err)
	}
	operation(tx)
}

func withCommittedUserTransaction(t testing.TB, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, operation func(pgx.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT dayorder.set_user_context($1)`, userID); err != nil {
		t.Fatal(err)
	}
	operation(tx)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func withBarePGXRollbackTransaction(t testing.TB, ctx context.Context, pool *pgxpool.Pool, operation func(pgx.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation(tx)
}

func sqlState(err error) string {
	var postgresError *pgconn.PgError
	if err != nil && errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return ""
}

func expectCheckViolation(t testing.TB, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, query, args...); sqlState(err) != "23514" {
		t.Fatalf("constraint SQLSTATE = %q, want 23514", sqlState(err))
	}
}

func frozenExecutionValue(column string) string {
	switch column {
	case "capabilities", "budget":
		return "'{}'::jsonb"
	case "deadline":
		return "now()+interval '2 minutes'"
	default:
		return "'replacement'"
	}
}

func readonlyRecord(userID, runID, token uuid.UUID, now time.Time) agentexecution.Record {
	return agentexecution.Record{
		Run: model.AgentRun{
			ID: runID, Intent: "read the calendar", Status: "ready", ActionMode: "read",
			Scope: []byte(`{"domains":["calendar"]}`),
		},
		Execution: agentexecution.Execution{
			UserID: userID, RunID: runID, Token: token, Mode: agentprotocol.ExecutionModeBackground,
			ProtocolVersion: "2.0", RuntimeVersion: "2.0.0", ModelProfile: "readonly-default", Timezone: "UTC",
			ResultOrigin: "client_reported",
			Capabilities: agentprotocol.CapabilitySnapshot{
				ExecutionMode: agentprotocol.ExecutionModeBackground, RuntimeVersion: "2.0.0",
				Scope: agentprotocol.AgentScope{Domains: []string{"calendar"}}, Skills: []agentprotocol.SkillRef{}, ToolIds: []string{"device.calendar.read"},
			},
			Budget: agentprotocol.Budget{
				MaxConcurrency: 1, MaxDurationMs: 60000, MaxRepeatedToolCalls: 2,
				MaxSteps: 8, MaxTokens: 1000, MaxWorkers: 0,
			},
			Deadline: now.Add(time.Minute), KnownUsage: agentprotocol.Usage{}, ReservedTokens: 1000,
		},
	}
}

func withStoreTransaction(t testing.TB, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, operation func(database.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT dayorder.set_user_context($1)`, userID); err != nil {
		t.Fatal(err)
	}
	operation(tx)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func withStoreRollbackTransaction(t testing.TB, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, operation func(database.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT dayorder.set_user_context($1)`, userID); err != nil {
		t.Fatal(err)
	}
	operation(tx)
}

func withBareRollbackTransaction(t testing.TB, ctx context.Context, pool *pgxpool.Pool, operation func(database.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	operation(tx)
}

func openTestPool(t testing.TB, ctx context.Context, databaseURL string, maxConnections int32) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("parse isolated PostgreSQL pool configuration")
	}
	config.MaxConns = maxConnections
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("open isolated PostgreSQL pool")
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal("connect isolated PostgreSQL pool")
	}
	return pool
}

func logFixtureCleanup(t testing.TB, fixture *testdb.Postgres) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := fixture.Close(ctx); err != nil {
			t.Errorf("close isolated PostgreSQL fixture %s", fixture.DatabaseName())
			return
		}
		t.Logf("deleted isolated PostgreSQL database %s", fixture.DatabaseName())
	})
}

func ptrTime(value time.Time) *time.Time { return &value }
func ptrString(value string) *string     { return &value }
