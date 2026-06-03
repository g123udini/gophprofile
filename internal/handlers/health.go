package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type dbPinger interface {
	Ping(ctx context.Context) error
}

type s3Pinger interface {
	Ping(ctx context.Context) error
}

type brokerChecker interface {
	Healthy() bool
}

type HealthHandler struct {
	db     dbPinger
	s3     s3Pinger
	broker brokerChecker
}

func NewHealthHandler(db dbPinger, s3 s3Pinger, broker brokerChecker) *HealthHandler {
	return &HealthHandler{db: db, s3: s3, broker: broker}
}

func (h *HealthHandler) Liveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "alive"})
}

func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	pgStatus := "ok"
	if err := h.db.Ping(ctx); err != nil {
		pgStatus = "down"
	}

	s3Status := "ok"
	if err := h.s3.Ping(ctx); err != nil {
		s3Status = "down"
	}

	rabbitStatus := "ok"
	if !h.broker.Healthy() {
		rabbitStatus = "down"
	}

	overall := "ok"
	statusCode := http.StatusOK
	if pgStatus != "ok" || s3Status != "ok" || rabbitStatus != "ok" {
		overall = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": overall,
		"components": map[string]string{
			"postgres": pgStatus,
			"s3":       s3Status,
			"rabbit":   rabbitStatus,
		},
	})
}
