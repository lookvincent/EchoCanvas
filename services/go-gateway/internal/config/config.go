package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// ProviderEndpoint configures an upstream LLM or Vision model endpoint.
type ProviderEndpoint struct {
	Name    string
	BaseURL string
	Model   string
	APIKey  string
}

// Config holds runtime configuration for the Go control plane and AIGC gateway.
type Config struct {
	Port                      string
	PySidecarURL              string
	CORSAllowOrigins          string
	DatabaseURL               string
	RedisAddr                 string
	InternalAuthToken         string
	LogLevel                  string
	GinMode                   string
	S3Endpoint                string
	S3Region                  string
	S3Bucket                  string
	S3UseSSL                  bool
	LLMPrimary                ProviderEndpoint
	LLMFallback               ProviderEndpoint
	VisionPrimary             ProviderEndpoint
	VisionFallback            ProviderEndpoint
	VLMJudgeModel             string
	GatewayRPM                int
	CircuitBreakerThreshold   int
	CircuitBreakerCooldownSec int
	MaxConcurrentVision       int
}

// Load reads service settings from environment variables.
func Load() (Config, error) {
	cfg := Config{
		Port:              getEnvOrDefault("GO_GATEWAY_PORT", "8080"),
		PySidecarURL:      getEnvOrDefault("PY_SIDECAR_URL", "http://localhost:8001"),
		CORSAllowOrigins:  getEnvOrDefault("CORS_ALLOW_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RedisAddr:         getEnvOrDefault("REDIS_ADDR", "localhost:6379"),
		InternalAuthToken: strings.TrimSpace(os.Getenv("INTERNAL_AUTH_TOKEN")),
		LogLevel:          getEnvOrDefault("LOG_LEVEL", "info"),
		GinMode:           getEnvOrDefault("GIN_MODE", "release"),
		S3Endpoint:        getEnvOrDefault("S3_ENDPOINT", "localhost:9000"),
		S3Region:          getEnvOrDefault("S3_REGION", "us-east-1"),
		S3Bucket:          getEnvOrDefault("S3_BUCKET", "echocanvas-media"),
		S3UseSSL:          strings.EqualFold(strings.TrimSpace(os.Getenv("S3_USE_SSL")), "true"),
		LLMPrimary: ProviderEndpoint{
			Name:    getEnvOrDefault("LLM_PRIMARY_PROVIDER", "deepseek"),
			BaseURL: getEnvOrDefault("LLM_PRIMARY_BASE_URL", "https://api.deepseek.com/v1"),
			Model:   getEnvOrDefault("LLM_PRIMARY_MODEL", "deepseek-chat"),
			APIKey:  strings.TrimSpace(os.Getenv("LLM_PRIMARY_API_KEY")),
		},
		LLMFallback: ProviderEndpoint{
			Name:    getEnvOrDefault("LLM_FALLBACK_PROVIDER", "openai_compat"),
			BaseURL: getEnvOrDefault("LLM_FALLBACK_BASE_URL", "https://api.openai.com/v1"),
			Model:   getEnvOrDefault("LLM_FALLBACK_MODEL", "gpt-4o-mini"),
			APIKey:  strings.TrimSpace(os.Getenv("LLM_FALLBACK_API_KEY")),
		},
		VisionPrimary: ProviderEndpoint{
			Name:    getEnvOrDefault("VISION_PRIMARY_PROVIDER", "siliconflow"),
			BaseURL: getEnvOrDefault("VISION_PRIMARY_BASE_URL", "https://api.siliconflow.cn/v1"),
			Model:   getEnvOrDefault("VISION_PRIMARY_MODEL", "black-forest-labs/FLUX.1-dev"),
			APIKey:  strings.TrimSpace(os.Getenv("VISION_PRIMARY_API_KEY")),
		},
		VisionFallback: ProviderEndpoint{
			Name:    getEnvOrDefault("VISION_FALLBACK_PROVIDER", "fal"),
			BaseURL: getEnvOrDefault("VISION_FALLBACK_BASE_URL", "https://fal.run"),
			Model:   getEnvOrDefault("VISION_FALLBACK_MODEL", "fal-ai/flux/dev"),
			APIKey:  strings.TrimSpace(os.Getenv("VISION_FALLBACK_API_KEY")),
		},
		VLMJudgeModel:             getEnvOrDefault("VLM_JUDGE_MODEL", "gpt-4o-mini"),
		GatewayRPM:                getEnvInt("GATEWAY_RPM", 60),
		CircuitBreakerThreshold:   getEnvInt("CIRCUIT_BREAKER_THRESHOLD", 5),
		CircuitBreakerCooldownSec: getEnvInt("CIRCUIT_BREAKER_COOLDOWN_SEC", 30),
		MaxConcurrentVision:       getEnvInt("MAX_CONCURRENT_VISION", 3),
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
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		return v
	}
	return fallback
}
