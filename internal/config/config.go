package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTP     HTTPConfig
	Postgres PostgresConfig
	S3       S3Config
	Rabbit   RabbitConfig
	OTel     OTelConfig
}

type HTTPConfig struct {
	Port            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration
}

type PostgresConfig struct {
	DSN string
}

type S3Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

type RabbitConfig struct {
	URL      string
	Exchange string
}

// OTelConfig configures the OTLP trace exporter. Empty Endpoint disables
// tracing — Init returns a no-op shutdown.
type OTelConfig struct {
	Endpoint string
	Insecure bool
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTP: HTTPConfig{
			Port:            getEnv("HTTP_PORT", "8080"),
			ReadTimeout:     getEnvDuration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    getEnvDuration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			ShutdownTimeout: getEnvDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Postgres: PostgresConfig{
			DSN: getEnv("POSTGRES_DSN", "postgres://gophprofile:gophprofile@localhost:5432/gophprofile?sslmode=disable"),
		},
		S3: S3Config{
			Endpoint:  getEnv("S3_ENDPOINT", "localhost:9000"),
			AccessKey: getEnv("S3_ACCESS_KEY", "minioadmin"),
			SecretKey: getEnv("S3_SECRET_KEY", "minioadmin"),
			Bucket:    getEnv("S3_BUCKET", "avatars"),
			UseSSL:    getEnvBool("S3_USE_SSL", false),
		},
		Rabbit: RabbitConfig{
			URL:      getEnv("RABBIT_URL", "amqp://guest:guest@localhost:5672/"),
			Exchange: getEnv("RABBIT_EXCHANGE", "avatars.exchange"),
		},
		OTel: OTelConfig{
			Endpoint: getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			Insecure: getEnvBool("OTEL_EXPORTER_OTLP_INSECURE", true),
		},
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func (c *Config) Addr() string {
	return fmt.Sprintf(":%s", c.HTTP.Port)
}
