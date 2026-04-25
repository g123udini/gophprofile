// Package metrics is a thin wrapper over prometheus/client_golang. All
// application metrics are declared here as package-level vars and registered
// against the default registry via init(); call sites just do .Inc()/.Observe().
//
// Cardinality note: the sprint spec sketched user_id as a label on uploads and
// storage; we drop it here — one series per user blows up the registry. Track
// per-user breakdowns via Loki queries instead.
package metrics

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTPRequestsTotal is incremented per finished HTTP request. The route
	// label is a chi route pattern (not the raw URL) so cardinality stays bounded.
	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests processed, labeled by method, route pattern and status code.",
	}, []string{"method", "route", "status"})

	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency, labeled by method and route pattern.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	// AvatarUploadsTotal counts business-level upload outcomes. Status values:
	// "success", "invalid_content_type", "internal_error".
	AvatarUploadsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "avatars_uploads_total",
		Help: "Avatar upload outcomes by status.",
	}, []string{"status"})

	AvatarUploadDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "avatars_upload_duration_seconds",
		Help:    "Avatar upload duration in seconds, labeled by outcome status.",
		Buckets: prometheus.DefBuckets,
	}, []string{"status"})

	// AvatarStorageBytes tracks the total bytes occupied by undeleted originals.
	// Add() on successful upload, Sub() on soft-delete. Resets to 0 on restart —
	// purely an in-process gauge, not a backed-by-S3 source of truth.
	AvatarStorageBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "avatars_storage_bytes",
		Help: "Total bytes of avatar originals tracked by this process since start.",
	})

	// AvatarProcessingStatusTotal counts worker outcomes per delivery: "completed",
	// "failed", "skipped" (idempotency hit).
	AvatarProcessingStatusTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "avatar_processing_status_total",
		Help: "Avatar processing outcomes from the worker by status.",
	}, []string{"status"})

	// RabbitConsumerPrefetch is a static gauge of the QoS prefetch the worker
	// consumes with. Useful in dashboards as an annotation, not a moving signal.
	RabbitConsumerPrefetch = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rabbit_consumer_prefetch",
		Help: "RabbitMQ consumer QoS prefetch count configured at startup.",
	})
)

// Upload status label constants — keep label values centralized to avoid typos.
const (
	UploadStatusSuccess       = "success"
	UploadStatusBadRequest    = "bad_request"
	UploadStatusInternalError = "internal_error"
	ProcessingStatusCompleted = "completed"
	ProcessingStatusFailed    = "failed"
	ProcessingStatusSkipped   = "skipped"
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		AvatarUploadsTotal,
		AvatarUploadDuration,
		AvatarStorageBytes,
		AvatarProcessingStatusTotal,
		RabbitConsumerPrefetch,
	)
}

// RegisterPgxPool wires gauges that read from pool.Stat() on each scrape.
// Idempotency: this MustRegisters; calling twice will panic — call once per
// process from main.
func RegisterPgxPool(pool *pgxpool.Pool) {
	prometheus.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "db_connections_acquired",
			Help: "Number of postgres connections currently checked out from the pool.",
		}, func() float64 { return float64(pool.Stat().AcquiredConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "db_connections_idle",
			Help: "Number of idle postgres connections in the pool.",
		}, func() float64 { return float64(pool.Stat().IdleConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "db_connections_total",
			Help: "Total open postgres connections (acquired + idle).",
		}, func() float64 { return float64(pool.Stat().TotalConns()) }),
	)
}

// Handler returns the http.Handler that serves /metrics in Prometheus format.
func Handler() http.Handler {
	return promhttp.Handler()
}
