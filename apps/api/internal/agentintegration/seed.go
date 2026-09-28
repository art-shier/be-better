package agentintegration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"dayorder.local/api/internal/agenttest"
	"dayorder.local/api/internal/auth"

	"github.com/google/uuid"
)

const fixturePassword = "Agent-Fixture-Password-2026"

var (
	fixtureWindowStart       = time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	fixtureWindowEnd         = time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	fixturePagingWindowStart = time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	fixturePagingWindowEnd   = time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
)

type fixtureAccount struct {
	Email    string    `json:"email"`
	Password string    `json:"password"`
	DeviceID uuid.UUID `json:"deviceId"`
	userID   uuid.UUID
}

type fixtureConfig struct {
	Accounts     []fixtureAccount `json:"accounts"`
	Profile      string           `json:"profile"`
	Window       fixtureWindow    `json:"window"`
	PagingWindow fixtureWindow    `json:"pagingWindow"`
}

type fixtureWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type pagingFixtureEvent struct {
	UserID   uuid.UUID `json:"-"`
	ID       uuid.UUID `json:"id"`
	Title    string    `json:"title"`
	Start    time.Time `json:"startAt"`
	End      time.Time `json:"endAt"`
	Timezone string    `json:"timezone"`
	Kind     string    `json:"kind"`
	Version  int64     `json:"version"`
}

func pagingFixtureEvents(userID uuid.UUID) []pagingFixtureEvent {
	events := make([]pagingFixtureEvent, 50)
	for index := range events {
		start := fixturePagingWindowStart.Add(time.Duration(index) * 20 * time.Minute)
		events[index] = pagingFixtureEvent{
			UserID: userID, ID: uuid.NewSHA1(userID, []byte(fmt.Sprintf("agent-paging-%02d", index))),
			Title: strings.Repeat("<", 240), Start: start, End: start.Add(10 * time.Minute),
			Timezone: "UTC", Kind: "fixed", Version: 1,
		}
	}
	return events
}

func seedDatabase(ctx context.Context, database *agenttest.Database, profile string) (fixtureConfig, error) {
	if database == nil || database.Migrator == nil {
		return fixtureConfig{}, errors.New("integration seed database is required")
	}
	passwordHash, err := auth.HashPassword(fixturePassword)
	if err != nil {
		return fixtureConfig{}, errors.New("hash integration account password")
	}
	accounts := []struct {
		userID, deviceID uuid.UUID
		email, title     string
	}{
		{database.UserA, database.DeviceA, "agent-a@example.test", "Architecture review A"},
		{database.UserB, database.DeviceB, "agent-b@example.test", "Private planning B"},
	}
	result := fixtureConfig{
		Accounts: make([]fixtureAccount, 0, len(accounts)), Profile: profile,
		Window:       fixtureWindow{Start: fixtureWindowStart.Format(time.RFC3339), End: fixtureWindowEnd.Format(time.RFC3339)},
		PagingWindow: fixtureWindow{Start: fixturePagingWindowStart.Format(time.RFC3339), End: fixturePagingWindowEnd.Format(time.RFC3339)},
	}
	for index, account := range accounts {
		if _, err = database.Migrator.Exec(ctx, `
UPDATE dayorder.users
SET email = $2, normalized_email = $2, password_hash = $3, updated_at = now()
WHERE id = $1
`, account.userID, account.email, passwordHash); err != nil {
			return fixtureConfig{}, fmt.Errorf("prepare integration account %d", index+1)
		}
		eventID := uuid.New()
		start := fixtureWindowStart.Add(time.Duration(9+index*2) * time.Hour)
		if _, err = database.Migrator.Exec(ctx, `
INSERT INTO dayorder.calendar_events (id, user_id, title, start_at, end_at, timezone, kind)
VALUES ($1, $2, $3, $4, $5, 'UTC', 'fixed')
`, eventID, account.userID, account.title, start, start.Add(time.Hour)); err != nil {
			return fixtureConfig{}, fmt.Errorf("prepare integration calendar %d", index+1)
		}
		result.Accounts = append(result.Accounts, fixtureAccount{
			Email: account.email, Password: fixturePassword, DeviceID: account.deviceID, userID: account.userID,
		})
	}
	for index, event := range pagingFixtureEvents(database.UserB) {
		if _, err = database.Migrator.Exec(ctx, `
INSERT INTO dayorder.calendar_events (id, user_id, title, start_at, end_at, timezone, kind)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`, event.ID, event.UserID, event.Title, event.Start, event.End, event.Timezone, event.Kind); err != nil {
			return fixtureConfig{}, fmt.Errorf("prepare integration paging calendar %d", index+1)
		}
	}
	return result, nil
}
