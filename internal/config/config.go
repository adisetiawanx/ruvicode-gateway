package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Config holds all runtime configuration for the gateway.
// Values are read from environment variables, with an optional local .env
// file for development. Provider identifiers are deliberately generic
// ("provider") so the upstream identity stays masked in public-facing code.
type Config struct {
	// Server
	Port string `env:"PORT" envDefault:"8080"`
	Env  string `env:"ENV" envDefault:"development"`

	// Database
	DatabaseURL string `env:"DATABASE_URL" envDefault:"postgresql://ruvicode:CHANGE_ME@localhost:5432/ruvicode"`

	// Redis
	RedisURL string `env:"REDIS_URL" envDefault:"redis://localhost:6379"`

	// Providers (comma-separated list of active provider names)
	ActiveProviders []string `env:"ACTIVE_PROVIDERS" envDefault:"provider"`

	// Provider connection (generic naming, upstream identity masked)
	ProviderBaseURL string   `env:"PROVIDER_BASE_URL"`
	ProviderAPIKeys []string `env:"PROVIDER_API_KEYS"`

	// Pricing engine
	PricingCronInterval string `env:"PRICING_CRON_INTERVAL" envDefault:"2m"`
	PricingSpreadPP     int    `env:"PRICING_SPREAD_PP" envDefault:"20"`

	// Rate limiting
	GlobalRPM int `env:"GLOBAL_RPM" envDefault:"60"`

	// Logging
	LogLevel string `env:"LOG_LEVEL" envDefault:"info"`
}

// Load reads configuration from the environment. It loads a local .env file
// if present (best effort, ignored in production where values come from the
// container environment), parses env vars into the Config struct, then
// validates required fields.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Validate required provider settings.
	if len(cfg.ProviderAPIKeys) == 0 || cfg.ProviderAPIKeys[0] == "" {
		return nil, fmt.Errorf("PROVIDER_API_KEYS is required (comma-separated provider keys)")
	}
	if cfg.ProviderBaseURL == "" {
		return nil, fmt.Errorf("PROVIDER_BASE_URL is required")
	}

	return cfg, nil
}
