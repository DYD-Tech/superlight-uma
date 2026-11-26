// Package config loads service configuration from flags and environment variables.
// Flags take precedence over environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime settings.
type Config struct {
	Addr          string        // listen address
	DBPath        string        // SQLite file path
	AllowedApps   []string      // registered app ids
	AdminKey      string        // bearer token for admin endpoints; empty = admin endpoints are open
	RateLimit     float64       // heartbeats per second per client IP (0 disables)
	RateBurst     int           // burst size for the rate limiter
	TrustProxy    bool          // use X-Forwarded-For / X-Real-IP for client IP
	CORSOrigins   []string      // allowed CORS origins: exact, "https://*.example" wildcard, or "*"; empty disables CORS
	RollupEvery   time.Duration // how often to compute daily summaries and prune
	RetentionDays int           // days of per-device activity to keep
}

// Load parses args (without program name) using env as fallback.
func Load(args []string, getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	env := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}

	fs := flag.NewFlagSet("superlight-uma", flag.ContinueOnError)
	addr := fs.String("addr", env("UMA_ADDR", ":8080"), "listen address (env UMA_ADDR)")
	db := fs.String("db", env("UMA_DB_PATH", "uma.db"), "SQLite database path (env UMA_DB_PATH)")
	apps := fs.String("apps", env("UMA_ALLOWED_APPS", ""), "comma-separated registered app ids (env UMA_ALLOWED_APPS)")
	key := fs.String("admin-key", env("UMA_ADMIN_KEY", ""), "admin bearer key for /v1/stats*, /v1/apps; empty disables admin auth (env UMA_ADMIN_KEY)")
	rl := fs.String("rate-limit", env("UMA_RATE_LIMIT", "1"), "heartbeats/sec per client IP, 0 disables (env UMA_RATE_LIMIT)")
	burst := fs.String("rate-burst", env("UMA_RATE_BURST", "20"), "rate limiter burst (env UMA_RATE_BURST)")
	proxy := fs.String("trust-proxy", env("UMA_TRUST_PROXY", "false"), "trust X-Forwarded-For (env UMA_TRUST_PROXY)")
	cors := fs.String("cors-origins", env("UMA_CORS_ORIGINS", ""), "comma-separated CORS origins, e.g. https://*.pages.dev,http://localhost:8000; * for any (env UMA_CORS_ORIGINS)")
	every := fs.String("rollup-every", env("UMA_ROLLUP_EVERY", "1h"), "summary/prune interval (env UMA_ROLLUP_EVERY)")
	keep := fs.String("retention-days", env("UMA_RETENTION_DAYS", "31"), "days of activity rows to keep, min 31 (env UMA_RETENTION_DAYS)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	c := Config{
		Addr:        *addr,
		DBPath:      *db,
		AllowedApps: splitList(*apps),
		AdminKey:    *key,
		CORSOrigins: splitList(*cors),
	}
	var err error
	if c.RateLimit, err = strconv.ParseFloat(*rl, 64); err != nil || c.RateLimit < 0 {
		return c, fmt.Errorf("invalid rate-limit %q", *rl)
	}
	if c.RateBurst, err = strconv.Atoi(*burst); err != nil || c.RateBurst < 1 {
		return c, fmt.Errorf("invalid rate-burst %q", *burst)
	}
	if c.TrustProxy, err = strconv.ParseBool(*proxy); err != nil {
		return c, fmt.Errorf("invalid trust-proxy %q", *proxy)
	}
	if c.RollupEvery, err = time.ParseDuration(*every); err != nil || c.RollupEvery < time.Minute {
		return c, fmt.Errorf("invalid rollup-every %q (min 1m)", *every)
	}
	if c.RetentionDays, err = strconv.Atoi(*keep); err != nil || c.RetentionDays < 31 {
		return c, fmt.Errorf("invalid retention-days %q (min 31, needed for MAU)", *keep)
	}
	return c, c.Validate()
}

// Validate checks required settings.
func (c Config) Validate() error {
	if len(c.AllowedApps) == 0 {
		return errors.New("no allowed apps: set UMA_ALLOWED_APPS or -apps")
	}
	for _, a := range c.AllowedApps {
		if !ValidAppID(a) {
			return fmt.Errorf("invalid app id %q (use 1-64 chars of a-z, 0-9, '-', '_', '.')", a)
		}
	}
	// An empty key is allowed (admin endpoints become open); a set key must be strong.
	if c.AdminKey != "" && len(c.AdminKey) < 16 {
		return errors.New("admin key must be at least 16 characters (or empty to disable admin auth): UMA_ADMIN_KEY / -admin-key")
	}
	return nil
}

// ValidAppID reports whether s is an acceptable app id.
func ValidAppID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
