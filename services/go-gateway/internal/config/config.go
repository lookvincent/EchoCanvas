package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Config holds runtime configuration for the Go control plane and AIGC gateway.
type Config struct {
	Port              string
	DatabaseURL       string
	RedisAddr         string
	InternalAuthToken string
	LogLevel          string
	GinMode           string
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3UseSSL          bool
}

// Load reads service settings from environment variables.
func Load() (Config, error) {
	cfg := Config{
		Port:              getEnvOrDefault("GO_GATEWAY_PORT", "8080"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RedisAddr:         getEnvOrDefault("REDIS_ADDR", "localhost:6379"),
		InternalAuthToken: strings.TrimSpace(os.Getenv("INTERNAL_AUTH_TOKEN")),
		LogLevel:          getEnvOrDefault("LOG_LEVEL", "info"),
		GinMode:           getEnvOrDefault("GIN_MODE", "release"),
		S3Endpoint:        getEnvOrDefault("S3_ENDPOINT", "localhost:9000"),
		S3Region:          getEnvOrDefault("S3_REGION", "us-east-1"),
		S3Bucket:          getEnvOrDefault("S3_BUCKET", "echocanvas-media"),
		S3UseSSL:          strings.EqualFold(strings.TrimSpace(os.Getenv("S3_USE_SSL")), "true"),
	}

	if cfg.DatabaseURL == "" {
		pgHost := getEnvOrDefault("POSTGRES_HOST", "localhost")
		pgPort := getEnvOrDefault("POSTGRES_PORT", "5432")
		pgUser := strings.TrimSpace(os.Getenv("POSTGRES_USER"))
		pgPass := strings.TrimSpace(os.Getenv("POSTGRES_PASSWORD"))
		pgDB := getEnvOrDefault("POSTGRES_DB", "echocanvas")
		if pgUser != "" && pgPass != "" {
			cfg.DatabaseURL = fmt.Sprintf(
				"postgres://%s:%s@%s:%s/%s?sslmode=disable",
				pgUser, pgPass, pgHost, pgPort, pgDB,
			)
		}
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL (or POSTGRES_USER and POSTGRES_PASSWORD) is required")
	}
	return cfg, nil
}

// ParseLogLevel converts a string log level into slog.Level.
func ParseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func getEnvOrDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(""+key)); val != "" {
		return val
	}
	return fallback
}
