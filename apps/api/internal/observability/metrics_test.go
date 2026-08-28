package observability

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type outboxRow struct {
	backlog int64
	age     float64
	dead    int64
	err     error
}

func (row outboxRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	*(destinations[0].(*int64)) = row.backlog
	*(destinations[1].(*float64)) = row.age
	*(destinations[2].(*int64)) = row.dead
	return nil
}

type stubOutboxQueryer struct{ row pgx.Row }

func (queryer stubOutboxQueryer) QueryRow(context.Context, string, ...any) pgx.Row {
	return queryer.row
}

func TestMetricsExposeApplicationAndOutboxSignals(t *testing.T) {
	metrics := NewMetrics("worker", nil, stubOutboxQueryer{row: outboxRow{backlog: 4, age: 37, dead: 2}})
	metrics.ObserveHTTPRequest("POST /api/v1/sync/mutations", "POST", 409, 125*time.Millisecond)
	metrics.ObserveSyncMutation("conflict")
	metrics.ObserveSyncCursorReset()
	metrics.ObserveLoginRateLimited()
	metrics.ObservePasswordOperation("verify", 80*time.Millisecond)

	if count := testutil.ToFloat64(metrics.syncMutations.WithLabelValues("conflict")); count != 1 {
		t.Fatalf("sync conflict count = %v", count)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "http://metrics.internal/metrics", nil)
	metrics.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("metrics status = %d", response.Code)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`dayorder_http_requests_total{method="POST",route="POST /api/v1/sync/mutations",service="worker",status="409"} 1`,
		`dayorder_outbox_backlog 4`,
		`dayorder_outbox_oldest_age_seconds 37`,
		`dayorder_outbox_dead_events 2`,
		`dayorder_sync_cursor_resets_total 1`,
		`dayorder_login_rate_limited_total 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, body)
		}
	}
}

func TestOutboxCollectorReportsScrapeErrorWithoutPublishingStaleValues(t *testing.T) {
	metrics := NewMetrics("worker", nil, stubOutboxQueryer{row: outboxRow{err: errors.New("database unavailable")}})
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))

	if !strings.Contains(response.Body.String(), `dayorder_outbox_scrape_success 0`) {
		t.Fatalf("missing failed scrape signal:\n%s", response.Body.String())
	}
}
