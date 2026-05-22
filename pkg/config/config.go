// Package config loads runtime configuration from environment variables.
//
// Each bounded context calls Load() once at startup. Adding a new env var
// means adding a field to the struct and a default in Load().
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	// Database
	DatabaseURL string

	// Server
	HTTPPort int

	// JWT
	JWTSecret     string
	JWTIssuer     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration

	// Logging
	LogLevel  string
	LogFormat string
}

// Load reads configuration from environment variables, falling back to
// values from a .env file in the current or parent directory if present.
// The httpPortEnv argument names the service's port env var
// (e.g. "IDENTITY_SVC_PORT") so each binary picks the right port.
func Load(httpPortEnv string) (*Config, error) {
	// .env is optional in production; required-style defaults still apply.
	_ = godotenv.Load(".env", "../.env", "../../.env")

	cfg := &Config{
		DatabaseURL:   getString("DATABASE_URL", ""),
		HTTPPort:      getInt(httpPortEnv, 8080),
		JWTSecret:     getString("JWT_SECRET", ""),
		JWTIssuer:     getString("JWT_ISSUER", "ion-core"),
		JWTAccessTTL:  getDuration("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL: getDuration("JWT_REFRESH_TTL", 720*time.Hour),
		LogLevel:      getString("LOG_LEVEL", "info"),
		LogFormat:     getString("LOG_FORMAT", "text"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	if len(cfg.JWTSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}

	return cfg, nil
}

func getString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
