package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"gophprofile/internal/logging"
)

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.RequestIDFromContext(r.Context())
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rr, req)

	require.NotEmpty(t, seen)
	_, err := uuid.Parse(seen)
	require.NoError(t, err)
	require.Equal(t, seen, rr.Header().Get(HeaderRequestID))
}

func TestRequestIDReusesInbound(t *testing.T) {
	const id = "incoming-correlation-id"

	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.RequestIDFromContext(r.Context())
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, id)
	h.ServeHTTP(rr, req)

	require.Equal(t, id, seen)
	require.Equal(t, id, rr.Header().Get(HeaderRequestID))
}
