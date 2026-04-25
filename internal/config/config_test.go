package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// unsetEnv clears key for the duration of the test, restoring the original
// value (or unset state) at cleanup. testing.T has no Unsetenv helper, hence
// the manual dance.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestLoad_DefaultsWhenEnvUnset(t *testing.T) {
	for _, k := range []string{
		"HTTP_PORT", "HTTP_READ_TIMEOUT", "POSTGRES_DSN",
		"S3_ENDPOINT", "S3_USE_SSL", "RABBIT_URL",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_INSECURE",
	} {
		unsetEnv(t, k)
	}

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, "8080", cfg.HTTP.Port)
	require.Equal(t, 10*time.Second, cfg.HTTP.ReadTimeout)
	require.Equal(t, "minioadmin", cfg.S3.AccessKey)
	require.False(t, cfg.S3.UseSSL)
	require.Empty(t, cfg.OTel.Endpoint)
	require.True(t, cfg.OTel.Insecure)
	require.Equal(t, ":8080", cfg.Addr())
}

func TestLoad_OverridesFromEnv(t *testing.T) {
	t.Setenv("HTTP_PORT", "9999")
	t.Setenv("HTTP_READ_TIMEOUT", "42s")
	t.Setenv("S3_USE_SSL", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "false")

	cfg, err := Load()
	require.NoError(t, err)

	require.Equal(t, "9999", cfg.HTTP.Port)
	require.Equal(t, 42*time.Second, cfg.HTTP.ReadTimeout)
	require.True(t, cfg.S3.UseSSL)
	require.Equal(t, "otel:4317", cfg.OTel.Endpoint)
	require.False(t, cfg.OTel.Insecure)
}

func TestLoad_InvalidDurationFallsBackToDefault(t *testing.T) {
	t.Setenv("HTTP_READ_TIMEOUT", "not-a-duration")
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 10*time.Second, cfg.HTTP.ReadTimeout)
}

func TestLoad_InvalidBoolFallsBackToDefault(t *testing.T) {
	unsetEnv(t, "S3_USE_SSL")
	t.Setenv("S3_USE_SSL", "definitely")
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.S3.UseSSL)
}
