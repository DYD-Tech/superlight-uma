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
	f.beatWith(t, dev, "Windows")
}

func (f *fixture) beatWith(t *testing.T, dev, platform string) {
	t.Helper()
	f.beatBody(t, dev, `{"app_id":"dyd-browser","device_id":"`+dev+`","version":"1.0.0","platform":"`+platform+`"}`)
}

func (f *fixture) beatNoPlatform(t *testing.T, dev string) {
	t.Helper()
	f.beatBody(t, dev, `{"app_id":"dyd-browser","device_id":"`+dev+`","version":"1.0.0"}`)
}

func (f *fixture) beatBody(t *testing.T, dev, body string) {
	t.Helper()
	rec := f.do("POST", "/v1/heartbeat", body, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("heartbeat %s: %d %s", dev, rec.Code, rec.Body)
	}
}

var auth = map[string]string{"Authorization": "Bearer " + adminKey}

const (
	dev1 = "3f1c2b4a-1d2e-4f60-8a9b-0c1d2e3f4a5b"
	dev2 = "9e8d7c6b-5a4f-4e3d-9c2b-1a0f9e8d7c6b"
	dev3 = "00000000-0000-4000-8000-000000000003"
	dev4 = "00000000-0000-4000-8000-000000000004"
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

func TestPlatformStatsAndHistory(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	start := f.now
	// 10 days ago: dev3 android (MAU only). 3 days ago: dev2 windows (WAU).
	// Today: dev1 windows, dev2 windows again, dev4 with no platform.
	f.now = start.AddDate(0, 0, -10)
	f.beatWith(t, dev3, "Android")
	f.now = start.AddDate(0, 0, -3)
	f.beatWith(t, dev2, "Windows")
	f.now = start
	f.beatWith(t, dev1, "Windows")
	f.beatWith(t, dev2, "Windows")
	f.beatNoPlatform(t, dev4)

	rec := f.do("GET", "/v1/stats?app_id=dyd-browser", "", auth)
	if rec.Code != 200 {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body)
	}
	var st statsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.AppID != "dyd-browser" || st.Day != "2026-11-25" || st.DAU != 3 || st.WAU != 3 || st.MAU != 4 || st.Total != 4 {
		t.Fatalf("overall stats: %+v", st.Stats)
	}
	if len(st.Platforms) != 3 {
		t.Fatalf("platforms: %+v", st.Platforms)
	}
	wantPlats := []store.PlatformStat{
		{Platform: "windows", DAU: 2, WAU: 2, MAU: 2, Total: 2},
		{Platform: "", DAU: 1, WAU: 1, MAU: 1, Total: 1},
		{Platform: "android", DAU: 0, WAU: 0, MAU: 1, Total: 1},
	}
	for i, want := range wantPlats {
		if st.Platforms[i] != want {
			t.Fatalf("platforms[%d]: got %+v want %+v\nall: %+v", i, st.Platforms[i], want, st.Platforms)
		}
	}

	// Filter is case-insensitive and does not shrink the platforms array.
	rec = f.do("GET", "/v1/stats?app_id=dyd-browser&platform=Windows", "", auth)
	if rec.Code != 200 {
		t.Fatalf("filtered stats: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.DAU != 2 || st.WAU != 2 || st.MAU != 2 || st.Total != 2 {
		t.Fatalf("windows stats: %+v", st.Stats)
	}
	if len(st.Platforms) != 3 || st.Platforms[0].Platform != "windows" {
		t.Fatalf("filtered response should still include full breakdown: %+v", st.Platforms)
	}

	// ?platform= selects devices that reported no platform.
	rec = f.do("GET", "/v1/stats?app_id=dyd-browser&platform=", "", auth)
	if rec.Code != 200 {
		t.Fatalf("empty platform: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.DAU != 1 || st.WAU != 1 || st.MAU != 1 || st.Total != 1 {
		t.Fatalf("empty platform stats: %+v", st.Stats)
	}

	rec = f.do("GET", "/v1/stats?app_id=dyd-browser&platform=linux", "", auth)
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.DAU != 0 || st.Total != 0 {
		t.Fatalf("unknown platform stats: %+v", st.Stats)
	}

	long := strings.Repeat("x", 65)
	if rec := f.do("GET", "/v1/stats?app_id=dyd-browser&platform="+long, "", auth); rec.Code != 400 {
		t.Errorf("overlong platform: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("GET", "/v1/stats/history?app_id=dyd-browser&platform="+long, "", auth); rec.Code != 400 {
		t.Errorf("overlong platform history: %d %s", rec.Code, rec.Body)
	}

	// History without a rollup is empty; after Maintain it is filled (including today).
	rec = f.do("GET", "/v1/stats/history?app_id=dyd-browser&days=30&platform=windows", "", auth)
	if rec.Code != 200 {
		t.Fatalf("history before rollup: %d %s", rec.Code, rec.Body)
	}
	var hist struct {
		Days []store.Stats `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.Days) != 0 {
		t.Fatalf("expected no per-platform history before rollup, got %+v", hist.Days)
	}

	if err := f.srv.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	rec = f.do("GET", "/v1/stats/history?app_id=dyd-browser&days=15&platform=windows", "", auth)
	if rec.Code != 200 {
		t.Fatalf("windows history: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	// Maintain rolls up yesterday (empty of windows? yesterday has no activity in this test)
	// and today. Yesterday is 2026-11-24. start is 2026-11-25.
	// On 2026-11-24: no heartbeats that day. windows: DAU 0, WAU 1 (dev2 on -3 days = 11-22), MAU 2 (dev2 + dev1? dev1 is today only so not in yesterday).
	// Careful: Maintain uses s.Now() which is `start` (today). It rolls up yesterday and today.
	// Yesterday 2026-11-24:
	//   windows DAU: 0 (no activity that day)
	//   windows WAU: last 7 days of yesterday = 11-18 to 11-24: dev2 on 11-22. DAU 0, WAU 1, MAU 1 (dev3 is android). Total 1 (only dev2 first_day 11-22; dev1 first_day is today)
	//   android: DAU 0, WAU 0, MAU 1 (dev3 on 11-15), Total 1
	//   empty: all 0, Total 0 — not in breakdown because no devices with first_day <= yesterday and platform ""
	// Today 2026-11-25:
	//   windows: DAU 2, WAU 2, MAU 2, Total 2
	if len(hist.Days) != 2 {
		t.Fatalf("windows history days: %+v", hist.Days)
	}
	if hist.Days[0].Day != "2026-11-24" || hist.Days[0].DAU != 0 || hist.Days[0].WAU != 1 || hist.Days[0].MAU != 1 || hist.Days[0].Total != 1 {
		t.Fatalf("windows yesterday: %+v", hist.Days[0])
	}
	if hist.Days[1].Day != "2026-11-25" || hist.Days[1].DAU != 2 || hist.Days[1].WAU != 2 || hist.Days[1].MAU != 2 || hist.Days[1].Total != 2 {
		t.Fatalf("windows today: %+v", hist.Days[1])
	}

	rec = f.do("GET", "/v1/stats/history?app_id=dyd-browser&days=15&platform=", "", auth)
	if rec.Code != 200 {
		t.Fatalf("empty history: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	// empty platform only exists today (first_day today), so yesterday has no row.
	if len(hist.Days) != 1 || hist.Days[0].Day != "2026-11-25" || hist.Days[0].DAU != 1 || hist.Days[0].Total != 1 {
		t.Fatalf("empty platform history: %+v", hist.Days)
	}

	// Unfiltered history still works and includes yesterday + today.
	rec = f.do("GET", "/v1/stats/history?app_id=dyd-browser&days=15", "", auth)
	if rec.Code != 200 {
		t.Fatalf("overall history: %d %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.Days) != 2 || hist.Days[1].DAU != 3 || hist.Days[1].Total != 4 {
		t.Fatalf("overall history after rollup: %+v", hist.Days)
	}
}
