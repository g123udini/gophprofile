package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apimw "gophprofile/internal/api/middleware"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRateLimiter_AllowsUnderLimit(t *testing.T) {
	h := apimw.RateLimiter(100, 5, time.Minute)(okHandler())

	for range 5 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}
}

func TestRateLimiter_BlocksWhenBurstExceeded(t *testing.T) {
	// burst=2 means only 2 tokens initially
	h := apimw.RateLimiter(0.001, 2, time.Minute)(okHandler())

	results := make([]int, 4)
	for i := range 4 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		h.ServeHTTP(rec, req)
		results[i] = rec.Code
	}

	require.Equal(t, http.StatusOK, results[0])
	require.Equal(t, http.StatusOK, results[1])
	require.Equal(t, http.StatusTooManyRequests, results[2])
	require.Equal(t, http.StatusTooManyRequests, results[3])
}

func TestRateLimiter_IndependentPerIP(t *testing.T) {
	// burst=1 — second request from same IP is blocked, different IP goes through
	h := apimw.RateLimiter(0.001, 1, time.Minute)(okHandler())

	send := func(ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":1234"
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, send("10.0.0.1"))
	require.Equal(t, http.StatusTooManyRequests, send("10.0.0.1"))
	require.Equal(t, http.StatusOK, send("10.0.0.2"))
}

func TestRateLimiter_UsesXRealIP(t *testing.T) {
	h := apimw.RateLimiter(0.001, 1, time.Minute)(okHandler())

	send := func(header, ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(header, ip)
		req.RemoteAddr = "127.0.0.1:1234"
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, send("X-Real-IP", "5.5.5.5"))
	require.Equal(t, http.StatusTooManyRequests, send("X-Real-IP", "5.5.5.5"))
	require.Equal(t, http.StatusOK, send("X-Real-IP", "6.6.6.6"))
}
