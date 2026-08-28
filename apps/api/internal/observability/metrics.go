package observability

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type poolStater interface {
	Stat() *pgxpool.Stat
}

type outboxQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Metrics struct {
	service          string
	registry         *prometheus.Registry
	httpRequests     *prometheus.CounterVec
	httpDuration     *prometheus.HistogramVec
	syncMutations    *prometheus.CounterVec
	syncCursorResets prometheus.Counter
	loginRateLimited prometheus.Counter
	passwordDuration *prometheus.HistogramVec
}

func NewMetrics(service string, pool poolStater, outbox outboxQueryer) *Metrics {
	registry := prometheus.NewRegistry()
	metrics := &Metrics{
		service:  service,
		registry: registry,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "dayorder", Name: "http_requests_total", Help: "HTTP requests handled by DayOrder.",
		}, []string{"service", "route", "method", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "dayorder", Name: "http_request_duration_seconds", Help: "DayOrder HTTP request latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"service", "route", "method"}),
		syncMutations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "dayorder", Name: "sync_mutations_total", Help: "Resource synchronization mutation outcomes.",
		}, []string{"status"}),
		syncCursorResets: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "dayorder", Name: "sync_cursor_resets_total", Help: "Expired cursors requiring a local cache rebuild.",
		}),
		loginRateLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "dayorder", Name: "login_rate_limited_total", Help: "Login requests rejected by throttling.",
		}),
		passwordDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "dayorder", Name: "password_operation_duration_seconds", Help: "Argon2id hash and verification latency.",
			Buckets: []float64{0.025, 0.05, 0.075, 0.1, 0.15, 0.25, 0.5, 1},
		}, []string{"operation"}),
	}
	registry.MustRegister(
		metrics.httpRequests, metrics.httpDuration, metrics.syncMutations,
		metrics.syncCursorResets, metrics.loginRateLimited, metrics.passwordDuration,
	)
	if pool != nil {
		registry.MustRegister(newPoolCollector(service, pool))
	}
	if outbox != nil {
		registry.MustRegister(newOutboxCollector(outbox))
	}
	return metrics
}

func (metrics *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (metrics *Metrics) ObserveHTTPRequest(route, method string, status int, elapsed time.Duration) {
	metrics.httpRequests.WithLabelValues(metrics.service, route, method, strconv.Itoa(status)).Inc()
	metrics.httpDuration.WithLabelValues(metrics.service, route, method).Observe(elapsed.Seconds())
}

func (metrics *Metrics) ObserveSyncMutation(status string) {
	switch status {
	case "applied", "duplicate", "conflict", "rejected":
	default:
		status = "unknown"
	}
	metrics.syncMutations.WithLabelValues(status).Inc()
}

func (metrics *Metrics) ObserveSyncCursorReset()  { metrics.syncCursorResets.Inc() }
func (metrics *Metrics) ObserveLoginRateLimited() { metrics.loginRateLimited.Inc() }

func (metrics *Metrics) ObservePasswordOperation(operation string, elapsed time.Duration) {
	if operation != "hash" && operation != "verify" {
		operation = "unknown"
	}
	metrics.passwordDuration.WithLabelValues(operation).Observe(elapsed.Seconds())
}

type poolCollector struct {
	service      string
	pool         poolStater
	acquired     *prometheus.Desc
	idle         *prometheus.Desc
	total        *prometheus.Desc
	max          *prometheus.Desc
	waitCount    *prometheus.Desc
	waitDuration *prometheus.Desc
}

func newPoolCollector(service string, pool poolStater) *poolCollector {
	labels := []string{"service"}
	return &poolCollector{
		service: service, pool: pool,
		acquired:     prometheus.NewDesc("dayorder_pgxpool_acquired_connections", "Currently acquired PostgreSQL connections.", labels, nil),
		idle:         prometheus.NewDesc("dayorder_pgxpool_idle_connections", "Currently idle PostgreSQL connections.", labels, nil),
		total:        prometheus.NewDesc("dayorder_pgxpool_total_connections", "Current PostgreSQL pool connections.", labels, nil),
		max:          prometheus.NewDesc("dayorder_pgxpool_max_connections", "Configured maximum PostgreSQL pool connections.", labels, nil),
		waitCount:    prometheus.NewDesc("dayorder_pgxpool_wait_total", "Pool acquisitions that waited for a connection.", labels, nil),
		waitDuration: prometheus.NewDesc("dayorder_pgxpool_wait_duration_seconds_total", "Time spent waiting for PostgreSQL connections.", labels, nil),
	}
}

func (collector *poolCollector) Describe(output chan<- *prometheus.Desc) {
	for _, descriptor := range []*prometheus.Desc{collector.acquired, collector.idle, collector.total, collector.max, collector.waitCount, collector.waitDuration} {
		output <- descriptor
	}
}

func (collector *poolCollector) Collect(output chan<- prometheus.Metric) {
	stats := collector.pool.Stat()
	labels := []string{collector.service}
	output <- prometheus.MustNewConstMetric(collector.acquired, prometheus.GaugeValue, float64(stats.AcquiredConns()), labels...)
	output <- prometheus.MustNewConstMetric(collector.idle, prometheus.GaugeValue, float64(stats.IdleConns()), labels...)
	output <- prometheus.MustNewConstMetric(collector.total, prometheus.GaugeValue, float64(stats.TotalConns()), labels...)
	output <- prometheus.MustNewConstMetric(collector.max, prometheus.GaugeValue, float64(stats.MaxConns()), labels...)
	output <- prometheus.MustNewConstMetric(collector.waitCount, prometheus.CounterValue, float64(stats.EmptyAcquireCount()), labels...)
	output <- prometheus.MustNewConstMetric(collector.waitDuration, prometheus.CounterValue, stats.EmptyAcquireWaitTime().Seconds(), labels...)
}

type outboxCollector struct {
	queryer       outboxQueryer
	backlog       *prometheus.Desc
	oldestAge     *prometheus.Desc
	dead          *prometheus.Desc
	scrapeSuccess *prometheus.Desc
}

func newOutboxCollector(queryer outboxQueryer) *outboxCollector {
	return &outboxCollector{
		queryer:       queryer,
		backlog:       prometheus.NewDesc("dayorder_outbox_backlog", "Pending and processing Outbox events.", nil, nil),
		oldestAge:     prometheus.NewDesc("dayorder_outbox_oldest_age_seconds", "Age of the oldest pending Outbox event.", nil, nil),
		dead:          prometheus.NewDesc("dayorder_outbox_dead_events", "Outbox events in terminal failure state.", nil, nil),
		scrapeSuccess: prometheus.NewDesc("dayorder_outbox_scrape_success", "Whether the Outbox aggregate query succeeded.", nil, nil),
	}
}

func (collector *outboxCollector) Describe(output chan<- *prometheus.Desc) {
	output <- collector.backlog
	output <- collector.oldestAge
	output <- collector.dead
	output <- collector.scrapeSuccess
}

func (collector *outboxCollector) Collect(output chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var backlog, dead int64
	var oldestAge float64
	err := collector.queryer.QueryRow(ctx, `
SELECT backlog, oldest_age_seconds, dead_total
FROM dayorder.outbox_metrics()
`).Scan(&backlog, &oldestAge, &dead)
	if err != nil {
		output <- prometheus.MustNewConstMetric(collector.scrapeSuccess, prometheus.GaugeValue, 0)
		return
	}
	output <- prometheus.MustNewConstMetric(collector.backlog, prometheus.GaugeValue, float64(backlog))
	output <- prometheus.MustNewConstMetric(collector.oldestAge, prometheus.GaugeValue, oldestAge)
	output <- prometheus.MustNewConstMetric(collector.dead, prometheus.GaugeValue, float64(dead))
	output <- prometheus.MustNewConstMetric(collector.scrapeSuccess, prometheus.GaugeValue, 1)
}
