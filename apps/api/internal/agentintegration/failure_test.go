package agentintegration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dayorder.local/api/internal/agentprotocol"
	"dayorder.local/api/internal/model"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPagingFixturePreservesBaselineWindowAndExceedsTheRealResultLimit(t *testing.T) {
	userID := uuid.New()
	events := pagingFixtureEvents(userID)
	if len(events) != 50 {
		t.Fatalf("paging fixture events = %d, want 50", len(events))
	}
	for index, event := range events {
		if event.UserID != userID || utf8.RuneCountInString(event.Title) != 240 || !strings.Contains(event.Title, "<") {
			t.Fatalf("event %d fixture metadata invalid: owner=%t title_runes=%d escaped=%t", index, event.UserID == userID, utf8.RuneCountInString(event.Title), strings.Contains(event.Title, "<"))
		}
		if event.Start.Before(fixturePagingWindowStart) || !event.End.Before(fixturePagingWindowEnd) || !event.Start.Before(event.End) {
			t.Fatalf("event %d paging bounds invalid: start_in_window=%t end_in_window=%t ordered=%t", index, !event.Start.Before(fixturePagingWindowStart), event.End.Before(fixturePagingWindowEnd), event.Start.Before(event.End))
		}
		if !event.Start.After(fixtureWindowEnd) {
			t.Fatalf("event %d overlaps baseline window", index)
		}
		if index > 0 && !events[index-1].Start.Before(event.Start) {
			t.Fatalf("events %d and %d are not strictly ordered", index-1, index)
		}
	}
	data := agentprotocol.ToolResultData{"events": events}
	raw, err := json.Marshal(agentprotocol.ToolResult{Ok: true, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 65_536 {
		t.Fatalf("50-event ToolResult bytes = %d, want >65536", len(raw))
	}
}

type recordingSessionExpirer struct {
	query string
	args  []any
	tag   pgconn.CommandTag
	err   error
}

func (executor *recordingSessionExpirer) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	executor.query, executor.args = query, args
	return executor.tag, executor.err
}

func TestExpireFixtureSessionTargetsOnlyTheAuthenticatedSyntheticSession(t *testing.T) {
	userID, sessionID := uuid.New(), uuid.New()
	executor := &recordingSessionExpirer{tag: pgconn.NewCommandTag("UPDATE 1")}
	configuration := fixtureConfig{Accounts: []fixtureAccount{{Email: "agent-a@example.test", userID: userID}}}
	authenticated := model.AuthenticatedSession{
		Account: model.Account{ID: userID, Email: "agent-a@example.test"},
		Session: model.Session{ID: sessionID, UserID: userID},
	}

	if err := expireFixtureSession(t.Context(), executor, configuration, authenticated); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(executor.query, "WHERE user_id = $1 AND id = $2") || len(executor.args) != 4 ||
		executor.args[0] != userID || executor.args[1] != sessionID {
		t.Fatalf("expiry statement invalid: target_predicate=%t arg_count=%d user_match=%t session_match=%t", strings.Contains(executor.query, "WHERE user_id = $1 AND id = $2"), len(executor.args), len(executor.args) > 0 && executor.args[0] == userID, len(executor.args) > 1 && executor.args[1] == sessionID)
	}
	createdAt, createdOK := executor.args[2].(time.Time)
	expiresAt, expiresOK := executor.args[3].(time.Time)
	if !createdOK || !expiresOK || !createdAt.Before(expiresAt) || !expiresAt.Before(time.Now()) {
		t.Fatalf("expiry timestamps invalid: created_time=%t expires_time=%t ordered=%t already_expired=%t", createdOK, expiresOK, createdOK && expiresOK && createdAt.Before(expiresAt), expiresOK && expiresAt.Before(time.Now()))
	}
}

func TestExpireFixtureSessionRejectsNonSyntheticOrUnmatchedRows(t *testing.T) {
	userID := uuid.New()
	authenticated := model.AuthenticatedSession{
		Account: model.Account{ID: userID, Email: "person@example.com"},
		Session: model.Session{ID: uuid.New(), UserID: userID},
	}
	executor := &recordingSessionExpirer{tag: pgconn.NewCommandTag("UPDATE 1")}
	if err := expireFixtureSession(t.Context(), executor, fixtureConfig{}, authenticated); err == nil || executor.query != "" {
		t.Fatalf("non-fixture expiry err=%v query=%q", err, executor.query)
	}

	authenticated.Account.Email = "agent-a@example.test"
	executor = &recordingSessionExpirer{tag: pgconn.NewCommandTag("UPDATE 0")}
	configuration := fixtureConfig{Accounts: []fixtureAccount{{Email: authenticated.Account.Email, userID: userID}}}
	if err := expireFixtureSession(t.Context(), executor, configuration, authenticated); err == nil {
		t.Fatal("zero-row precise expiry was accepted")
	}
}

func TestActiveDependencyTrackerDeletesSettledRuns(t *testing.T) {
	runID := uuid.New()
	control := &faultControl{}
	control.beginDependency(runID)
	control.beginDependency(runID)
	if got := control.activeDependencyCount(runID); got != 2 {
		t.Fatalf("active dependency count = %d, want 2", got)
	}
	control.endDependency(runID)
	control.endDependency(runID)
	if got := control.activeDependencyCount(runID); got != 0 {
		t.Fatalf("settled dependency count = %d, want 0", got)
	}
	control.mu.RLock()
	_, retained := control.activeDependencies[runID]
	control.mu.RUnlock()
	if retained {
		t.Fatal("settled dependency retained a map entry")
	}
}
