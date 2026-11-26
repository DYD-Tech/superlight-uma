package config

import (
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadEnvAndFlags(t *testing.T) {
	env := envOf(map[string]string{
		"UMA_ALLOWED_APPS": "dyd-browser, other-app",
		"UMA_ADMIN_KEY":    "0123456789abcdef",
		"UMA_ADDR":         ":9000",
	})
	c, err := Load([]string{"-db", "/tmp/x.db", "-rollup-every", "30m"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":9000" || c.DBPath != "/tmp/x.db" || len(c.AllowedApps) != 2 || c.AllowedApps[1] != "other-app" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.RollupEvery != 30*time.Minute || c.RetentionDays != 31 || c.RateLimit != 1 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := map[string]map[string]string{
		"no apps":   {"UMA_ADMIN_KEY": "0123456789abcdef"},
		"short key": {"UMA_ALLOWED_APPS": "a", "UMA_ADMIN_KEY": "short"},
		"bad app":   {"UMA_ALLOWED_APPS": "Bad App", "UMA_ADMIN_KEY": "0123456789abcdef"},
		"retention": {"UMA_ALLOWED_APPS": "a", "UMA_ADMIN_KEY": "0123456789abcdef", "UMA_RETENTION_DAYS": "7"},
	}
	for name, env := range cases {
		if _, err := Load(nil, envOf(env)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadEmptyAdminKeyAllowed(t *testing.T) {
	c, err := Load(nil, envOf(map[string]string{"UMA_ALLOWED_APPS": "a"}))
	if err != nil {
		t.Fatalf("empty admin key should be allowed: %v", err)
	}
	if c.AdminKey != "" {
		t.Fatalf("admin key: %q", c.AdminKey)
	}
}
