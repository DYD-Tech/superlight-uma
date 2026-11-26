package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DYD-Tech/superlight-uma/internal/config"
	"github.com/DYD-Tech/superlight-uma/internal/store"
	"github.com/DYD-Tech/superlight-uma/internal/store/sqlite"
)

const adminKey = "test-admin-key-123456"

type fixture struct {
	srv *Server
	h   http.Handler
	now time.Time
}

func newFixture(t *testing.T, mod func(*config.Config)) *fixture {
	t.Helper()
	st, err := sqlite.Open(filepath.Join(t.TempDir(), "uma.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		AllowedApps:   []string{"dyd-browser", "other"},
		AdminKey:      adminKey,
		RetentionDays: 31,
	}
	if mod != nil {
		mod(&cfg)
	}
	f := &fixture{now: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
	f.srv = New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.srv.Now = func() time.Time { return f.now }
	f.h = f.srv.Handler()
	return f
}

func (f *fixture) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) beat(t *testing.T, dev string) {
	t.Helper()
	rec := f.do("POST", "/v1/heartbeat", `{"app_id":"dyd-browser","device_id":"`+dev+`","version":"1.0.0","platform":"Windows"}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("heartbeat %s: %d %s", dev, rec.Code, rec.Body)
	}
}

var auth = map[string]string{"Authorization": "Bearer " + adminKey}

const (
	dev1 = "3f1c2b4a-1d2e-4f60-8a9b-0c1d2e3f4a5b"
	dev2 = "9e8d7c6b-5a4f-4e3d-9c2b-1a0f9e8d7c6b"
	dev3 = "00000000-0000-4000-8000-000000000003"
)

func TestHeartbeatValidation(t *testing.T) {
	f := newFixture(t, nil)
	cases := []struct {
		name, body string
		code       int
	}{
		{"ok", `{"app_id":"dyd-browser","device_id":"` + dev1 + `"}`, 204},
		{"uppercase uuid ok", `{"app_id":"dyd-browser","device_id":"` + strings.ToUpper(dev1) + `"}`, 204},
		{"unknown app", `{"app_id":"evil","device_id":"` + dev1 + `"}`, 403},
		{"missing app", `{"device_id":"` + dev1 + `"}`, 403},
		{"bad uuid", `{"app_id":"dyd-browser","device_id":"not-a-uuid"}`, 400},
		{"braced uuid", `{"app_id":"dyd-browser","device_id":"{` + dev1 + `}"}`, 400},
		{"bad json", `{`, 400},
		{"long version", `{"app_id":"dyd-browser","device_id":"` + dev1 + `","version":"` + strings.Repeat("x", 65) + `"}`, 400},
		{"too large", `{"app_id":"dyd-browser","device_id":"` + dev1 + `","pad":"` + strings.Repeat("x", 5000) + `"}`, 413},
	}
	for _, c := range cases {
		if rec := f.do("POST", "/v1/heartbeat", c.body, nil); rec.Code != c.code {
			t.Errorf("%s: got %d want %d (%s)", c.name, rec.Code, c.code, rec.Body)
		}
	}
	if rec := f.do("GET", "/v1/heartbeat", "", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET heartbeat: %d", rec.Code)
	}
}

func TestStatsFlow(t *testing.T) {
	f := newFixture(t, nil)
	start := f.now
	// 10 days ago: dev3. 3 days ago: dev2. Today: dev1 several times + dev2.
	f.now = start.AddDate(0, 0, -10)
	f.beat(t, dev3)
	f.now = start.AddDate(0, 0, -3)
	f.beat(t, dev2)
	f.now = start
	for i := 0; i < 5; i++ {
		f.beat(t, dev1)
	}
	f.beat(t, dev2)

	rec := f.do("GET", "/v1/stats?app_id=dyd-browser", "", auth)
	if rec.Code != 200 {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body)
	}
	var st store.Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	want := store.Stats{AppID: "dyd-browser", Day: "2026-11-25", DAU: 2, WAU: 2, MAU: 3, Total: 3}
	if st != want {
		t.Fatalf("got %+v want %+v", st, want)
	}

	// Other app is isolated.
	rec = f.do("GET", "/v1/stats?app_id=other", "", auth)
	if !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("other app: %s", rec.Body)
	}
}

func TestAdminAuth(t *testing.T) {
	f := newFixture(t, nil)
	for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong"}, {"Authorization": adminKey}} {
		if rec := f.do("GET", "/v1/stats?app_id=dyd-browser", "", hdr); rec.Code != 401 {
			t.Errorf("hdr %v: got %d", hdr, rec.Code)
		}
	}
	if rec := f.do("GET", "/v1/stats/history?app_id=dyd-browser", "", nil); rec.Code != 401 {
		t.Errorf("history without auth: %d", rec.Code)
	}
	if rec := f.do("GET", "/v1/stats", "", auth); rec.Code != 400 {
		t.Errorf("missing app_id: %d", rec.Code)
	}
	if rec := f.do("GET", "/v1/stats?app_id=nope", "", auth); rec.Code != 404 {
		t.Errorf("unknown app: %d", rec.Code)
	}
}

func TestMaintenanceHistoryAndPrune(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	start := f.now
	// Simulate 40 days of traffic with an hourly-ish maintenance pass each day.
	for i := 39; i >= 0; i-- {
		f.now = start.AddDate(0, 0, -i)
		f.beat(t, dev1)
		if i%2 == 0 {
			f.beat(t, dev2)
		}
		if err := f.srv.Maintain(ctx); err != nil {
			t.Fatal(err)
		}
	}

	rec := f.do("GET", "/v1/stats/history?app_id=dyd-browser&days=7", "", auth)
	if rec.Code != 200 {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Days []store.Stats `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Days) != 7 || resp.Days[6].Day != "2026-11-25" {
		t.Fatalf("history days: %+v", resp.Days)
	}
	last := resp.Days[6]
	if last.DAU != 2 || last.WAU != 2 || last.MAU != 2 || last.Total != 2 {
		t.Fatalf("today summary: %+v", last)
	}
	// Full history is kept beyond the activity retention window.
	rec = f.do("GET", "/v1/stats/history?app_id=dyd-browser&days=365", "", auth)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// 40 days of traffic plus the (empty) day before the first pass.
	if len(resp.Days) != 41 {
		t.Fatalf("want 41 summary rows, got %d", len(resp.Days))
	}
	if rec := f.do("GET", "/v1/stats/history?app_id=dyd-browser&days=0", "", auth); rec.Code != 400 {
		t.Errorf("days=0: %d", rec.Code)
	}

	// Activity older than retention was pruned: a 40-day-old-only device would
	// no longer appear in MAU, but stats for today are unaffected.
	st, err := f.srv.store.Stats(ctx, "dyd-browser", "2026-11-25")
	if err != nil || st.MAU != 2 {
		t.Fatalf("stats after prune: %+v %v", st, err)
	}
	old, _ := f.srv.store.Stats(ctx, "dyd-browser", store.Day(start.AddDate(0, 0, -35)))
	if old.DAU != 0 {
		t.Fatalf("expected activity 35 days ago pruned, got %+v", old)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.RateLimit = 0.001; c.RateBurst = 2 })
	body := `{"app_id":"dyd-browser","device_id":"` + dev1 + `"}`
	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, f.do("POST", "/v1/heartbeat", body, nil).Code)
	}
	if codes[0] != 204 || codes[1] != 204 || codes[2] != 429 {
		t.Fatalf("codes: %v", codes)
	}
}

func TestCORS(t *testing.T) {
	f := newFixture(t, func(c *config.Config) {
		c.CORSOrigins = []string{"https://*.pages.dev", "http://localhost:8000", "https://stats.example.com/"}
	})
	preflight := func(origin, path string) *httptest.ResponseRecorder {
		return f.do("OPTIONS", path, "", map[string]string{
			"Origin":                         origin,
			"Access-Control-Request-Method":  "GET",
			"Access-Control-Request-Headers": "authorization",
		})
	}
	for _, origin := range []string{"https://uma-dash.pages.dev", "https://abc123.uma-dash.pages.dev", "http://localhost:8000", "https://stats.example.com"} {
		for _, path := range []string{"/v1/stats", "/v1/stats/history", "/v1/apps", "/v1/heartbeat"} {
			rec := preflight(origin, path)
			if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != origin ||
				!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
				t.Errorf("preflight %s %s: %d %v", origin, path, rec.Code, rec.Header())
			}
		}
	}
	for _, origin := range []string{"https://pages.dev", "http://x.pages.dev", "https://evil.com", "https://x.pages.dev.evil.com",
		"https://evil.com/.pages.dev", "https://x.pages.dev:8443", "http://localhost:9000", "null"} {
		rec := preflight(origin, "/v1/stats")
		if rec.Code != 403 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("origin %q should be rejected: %d %v", origin, rec.Code, rec.Header())
		}
	}
	// Actual request from an allowed origin carries the CORS header and auth still applies.
	hdr := map[string]string{"Origin": "https://uma-dash.pages.dev", "Authorization": "Bearer " + adminKey}
	rec := f.do("GET", "/v1/stats?app_id=dyd-browser", "", hdr)
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://uma-dash.pages.dev" {
		t.Fatalf("GET with origin: %d %v", rec.Code, rec.Header())
	}
	rec = f.do("GET", "/v1/stats?app_id=dyd-browser", "", map[string]string{"Origin": "https://uma-dash.pages.dev"})
	if rec.Code != 401 || rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("401 must still carry CORS header so the browser can read it: %d %v", rec.Code, rec.Header())
	}
	rec = f.do("GET", "/v1/stats?app_id=dyd-browser", "", map[string]string{"Origin": "https://evil.com", "Authorization": "Bearer " + adminKey})
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin got CORS header")
	}
}

func TestCORSDisabledByDefault(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do("OPTIONS", "/v1/stats", "", map[string]string{"Origin": "https://x.pages.dev", "Access-Control-Request-Method": "GET"})
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("CORS should be off: %v", rec.Header())
	}
	if rec := f.do("GET", "/healthz", "", nil); rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
}

func TestAdminOpenWithoutKey(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.AdminKey = "" })
	f.beat(t, dev1)
	for _, path := range []string{"/v1/stats?app_id=dyd-browser", "/v1/stats/history?app_id=dyd-browser", "/v1/apps"} {
		if rec := f.do("GET", path, "", nil); rec.Code != 200 {
			t.Errorf("%s without key: %d %s", path, rec.Code, rec.Body)
		}
		// Any Authorization header is ignored when no key is configured.
		if rec := f.do("GET", path, "", map[string]string{"Authorization": "Bearer whatever"}); rec.Code != 200 {
			t.Errorf("%s with arbitrary key: %d", path, rec.Code)
		}
	}
	rec := f.do("GET", "/v1/stats?app_id=dyd-browser", "", nil)
	if !strings.Contains(rec.Body.String(), `"dau":1`) {
		t.Fatalf("stats body: %s", rec.Body)
	}
	// Validation still applies.
	if rec := f.do("GET", "/v1/stats?app_id=nope", "", nil); rec.Code != 404 {
		t.Errorf("unknown app: %d", rec.Code)
	}
}

func TestAdminKeyRequiredWhenSet(t *testing.T) {
	f := newFixture(t, nil)
	for _, path := range []string{"/v1/stats?app_id=dyd-browser", "/v1/stats/history?app_id=dyd-browser", "/v1/apps"} {
		if rec := f.do("GET", path, "", nil); rec.Code != 401 {
			t.Errorf("%s without key: %d", path, rec.Code)
		}
		if rec := f.do("GET", path, "", auth); rec.Code != 200 {
			t.Errorf("%s with key: %d", path, rec.Code)
		}
	}
}

func TestApps(t *testing.T) {
	f := newFixture(t, nil)
	if rec := f.do("GET", "/v1/apps", "", nil); rec.Code != 401 {
		t.Fatalf("apps without auth: %d", rec.Code)
	}
	rec := f.do("GET", "/v1/apps", "", auth)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"apps":["dyd-browser","other"]}` {
		t.Fatalf("apps: %d %s", rec.Code, rec.Body)
	}
}

func TestNormalizeUUID(t *testing.T) {
	if got, ok := normalizeUUID("3F1C2B4A-1D2E-4F60-8A9B-0C1D2E3F4A5B"); !ok || got != dev1 {
		t.Fatalf("normalize: %q %v", got, ok)
	}
	for _, bad := range []string{"", dev1 + "0", "3f1c2b4a_1d2e-4f60-8a9b-0c1d2e3f4a5b", "gf1c2b4a-1d2e-4f60-8a9b-0c1d2e3f4a5b"} {
		if _, ok := normalizeUUID(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
