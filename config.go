package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is read from the environment; contact sites come from a JSON file
// because each one carries a recipient list and allowed origins.
type Config struct {
	ListenAddr         string
	SMTPHost           string
	SMTPPort           int
	DefaultFrom        string
	AllowedFromDomains []string
	TestRecipient      string
	DBPath             string
	CheckInterval      time.Duration
	APIKeys            map[string]string // key -> client name
	Sites              map[string]Site
}

// Site is a public contact form. The recipient is fixed here so the public
// endpoint can never be used to send mail to an arbitrary address.
type Site struct {
	To       []string `json:"to"`
	From     string   `json:"from"`
	FromName string   `json:"fromName"`
	Subject  string   `json:"subject"`
	Origins  []string `json:"origins"`
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func LoadConfig() (*Config, error) {
	port, err := strconv.Atoi(env("SMTP_PORT", "25"))
	if err != nil {
		return nil, fmt.Errorf("SMTP_PORT: %w", err)
	}
	interval, err := time.ParseDuration(env("CHECK_INTERVAL", "15m"))
	if err != nil || interval <= 0 {
		return nil, fmt.Errorf("CHECK_INTERVAL: must be a duration like 15m")
	}
	cfg := &Config{
		ListenAddr:    env("LISTEN_ADDR", "127.0.0.1:5300"),
		SMTPHost:      env("SMTP_HOST", "127.0.0.1"),
		SMTPPort:      port,
		DefaultFrom:   env("DEFAULT_FROM", "noreply@wilhelm.my"),
		TestRecipient: env("TEST_RECIPIENT", "test@wilhelm.my"),
		DBPath:        env("DB_PATH", "/var/lib/mail-api/mail-api.db"),
		CheckInterval: interval,
		APIKeys:       map[string]string{},
		Sites:         map[string]Site{},
	}
	for _, d := range strings.Split(env("ALLOWED_FROM_DOMAINS", "wilhelm.my"), ",") {
		if d = strings.TrimSpace(strings.ToLower(d)); d != "" {
			cfg.AllowedFromDomains = append(cfg.AllowedFromDomains, d)
		}
	}

	// API_KEYS="helptype:abc123,other:def456"
	for _, pair := range strings.Split(os.Getenv("API_KEYS"), ",") {
		name, key, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok || name == "" || len(key) < 16 {
			if pair != "" {
				return nil, fmt.Errorf("API_KEYS: entry %q must be name:key with a key of 16+ chars", name)
			}
			continue
		}
		cfg.APIKeys[key] = name
	}

	if path := os.Getenv("SITES_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("SITES_FILE: %w", err)
		}
		if err := json.Unmarshal(raw, &cfg.Sites); err != nil {
			return nil, fmt.Errorf("SITES_FILE: %w", err)
		}
		for name, s := range cfg.Sites {
			if len(s.To) == 0 || s.Subject == "" {
				return nil, fmt.Errorf("site %q needs to and subject", name)
			}
		}
	}
	return cfg, nil
}
