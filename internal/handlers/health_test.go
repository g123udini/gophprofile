package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"gophprofile/internal/handlers"
)

type fakeDB struct{ err error }

func (f *fakeDB) Ping(_ context.Context) error { return f.err }

type fakeS3 struct{ err error }

func (f *fakeS3) Ping(_ context.Context) error { return f.err }

type fakeBroker struct{ healthy bool }

func (f *fakeBroker) Healthy() bool { return f.healthy }

func TestHealthHandler_Liveness_AlwaysOK(t *testing.T) {
	h := handlers.NewHealthHandler(&fakeDB{}, &fakeS3{}, &fakeBroker{})
	rec := httptest.NewRecorder()
	h.Liveness(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	require.Equal(t, "alive", body["status"])
}

func TestHealthHandler_Readiness_AllHealthy(t *testing.T) {
	h := handlers.NewHealthHandler(
		&fakeDB{},
		&fakeS3{},
		&fakeBroker{healthy: true},
	)
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	require.Equal(t, "ok", body["status"])
}

func TestHealthHandler_Readiness_DBDown(t *testing.T) {
	h := handlers.NewHealthHandler(
		&fakeDB{err: errors.New("connection refused")},
		&fakeS3{},
		&fakeBroker{healthy: true},
	)
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	require.Equal(t, "degraded", body["status"])
	comps := body["components"].(map[string]any)
	require.Equal(t, "down", comps["postgres"])
	require.Equal(t, "ok", comps["s3"])
	require.Equal(t, "ok", comps["rabbit"])
}

func TestHealthHandler_Readiness_S3Down(t *testing.T) {
	h := handlers.NewHealthHandler(
		&fakeDB{},
		&fakeS3{err: errors.New("s3 unreachable")},
		&fakeBroker{healthy: true},
	)
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	comps := body["components"].(map[string]any)
	require.Equal(t, "ok", comps["postgres"])
	require.Equal(t, "down", comps["s3"])
}

func TestHealthHandler_Readiness_BrokerDown(t *testing.T) {
	h := handlers.NewHealthHandler(
		&fakeDB{},
		&fakeS3{},
		&fakeBroker{healthy: false},
	)
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	comps := body["components"].(map[string]any)
	require.Equal(t, "down", comps["rabbit"])
}

func TestHealthHandler_Readiness_AllDown(t *testing.T) {
	h := handlers.NewHealthHandler(
		&fakeDB{err: errors.New("db down")},
		&fakeS3{err: errors.New("s3 down")},
		&fakeBroker{healthy: false},
	)
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	require.Equal(t, "degraded", body["status"])
	comps := body["components"].(map[string]any)
	require.Equal(t, "down", comps["postgres"])
	require.Equal(t, "down", comps["s3"])
	require.Equal(t, "down", comps["rabbit"])
}
