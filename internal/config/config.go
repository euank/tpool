package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Socket   string     `toml:"socket"`
	LogLevel string     `toml:"log_level"`
	Web      *WebConfig `toml:"web"`
}

type WebConfig struct {
	Enabled bool         `toml:"enabled"`
	Address string       `toml:"address"`
	Ngrok   *NgrokConfig `toml:"ngrok"`
}

type NgrokConfig struct {
	Authtoken          string       `toml:"authtoken"`
	URL                string       `toml:"url"`
	ServerAddr         string       `toml:"server_addr"`
	InsecureSkipVerify bool         `toml:"insecure_skip_verify"`
	OAuth              *OAuthConfig `toml:"oauth"`
}

type OAuthConfig struct {
	Provider     string   `toml:"provider"`
	AllowedUsers []string `toml:"allowed_users"`
}

func DefaultConfig() *Config {
	return &Config{
		Socket:   defaultSocketPath(),
		LogLevel: "info",
		Web:      nil,
	}
}

func defaultSocketPath() string {
	if path := os.Getenv("TPOOL_SOCKET"); path != "" {
		return path
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = "/tmp"
	}
	return filepath.Join(runtimeDir, fmt.Sprintf("tpool-%d.sock", os.Getuid()))
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Apply defaults for web if enabled but address not set
	if cfg.Web != nil && cfg.Web.Enabled && cfg.Web.Address == "" {
		cfg.Web.Address = ":8080"
	}

	// Normalize log level
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	return cfg, nil
}

func (c *Config) SlogLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
