package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"gophprofile/internal/metrics"
)

func TestMetricsCountsMatchedRoute(t *testing.T) {
	metrics.HTTPRequestsTotal.Reset()

	r := chi.NewRouter()
	r.Use(Metrics)
	r.Get("/api/v1/avatars/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/abc-123", nil)
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1.0, testutil.ToFloat64(
		metrics.HTTPRequestsTotal.WithLabelValues("GET", "/api/v1/avatars/{id}", "200"),
	))
}

func TestMetricsLabelsUnknownAsNotFound(t *testing.T) {
	metrics.HTTPRequestsTotal.Reset()

	r := chi.NewRouter()
	r.Use(Metrics)
	// chi only wires its middleware chain when at least one route is registered,
	// so a stub matched route is required even though we test the unmatched path.
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Equal(t, 1.0, testutil.ToFloat64(
		metrics.HTTPRequestsTotal.WithLabelValues("GET", "not_found", "404"),
	))
}
