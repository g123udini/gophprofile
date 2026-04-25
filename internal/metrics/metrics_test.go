package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandler_ExposesRegisteredMetricNames(t *testing.T) {
	// Prime each collector once so it appears in the scrape output. Counters
	// without observations don't render any sample lines.
	HTTPRequestsTotal.WithLabelValues("GET", "/x", "200").Inc()
	HTTPRequestDuration.WithLabelValues("GET", "/x").Observe(0.1)
	AvatarUploadsTotal.WithLabelValues(UploadStatusSuccess).Inc()
	AvatarUploadDuration.WithLabelValues(UploadStatusSuccess).Observe(0.05)
	AvatarStorageBytes.Set(123)
	AvatarProcessingStatusTotal.WithLabelValues(ProcessingStatusCompleted).Inc()
	RabbitConsumerPrefetch.Set(1)

	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	out := string(body)
	for _, want := range []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"avatars_uploads_total",
		"avatars_upload_duration_seconds",
		"avatars_storage_bytes",
		"avatar_processing_status_total",
		"rabbit_consumer_prefetch",
	} {
		require.Truef(t, strings.Contains(out, want), "metric %q missing from /metrics output", want)
	}
}
