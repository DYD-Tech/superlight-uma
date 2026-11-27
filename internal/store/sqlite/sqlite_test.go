package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/DYD-Tech/superlight-uma/internal/store"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(store.DayLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return d.Add(12 * time.Hour)
}

func hb(t *testing.T, s *Store, app, dev, d string) {
	t.Helper()
	if err := s.RecordHeartbeat(context.Background(), store.Heartbeat{AppID: app, DeviceID: dev, At: day(t, d)}); err != nil {
		t.Fatal(err)
	}
}

func TestStatsWindows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// d1 active today (twice), d2 3 days ago, d3 20 days ago, d4 40 days ago.
	hb(t, s, "app", "d1", "2026-03-31")
	hb(t, s, "app", "d1", "2026-03-31")
	hb(t, s, "app", "d2", "2026-03-28")
	hb(t, s, "app", "d3", "2026-03-11")
	hb(t, s, "app", "d4", "2026-02-19")
	hb(t, s, "other", "d1", "2026-03-31")

	got, err := s.Stats(ctx, "app", "2026-03-31")
	if err != nil {
		t.Fatal(err)
	}
	want := store.Stats{AppID: "app", Day: "2026-03-31", DAU: 1, WAU: 2, MAU: 3, Total: 4}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}

	// Total as of an earlier day only counts devices first seen by then.
	got, _ = s.Stats(ctx, "app", "2026-03-01")
	if got.Total != 1 || got.DAU != 0 || got.MAU != 1 {
		t.Fatalf("historic stats: %+v", got)
	}
}

func TestRollupHistoryPrune(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hb(t, s, "app", "d1", "2026-01-01")
	hb(t, s, "app", "d1", "2026-01-02")
	hb(t, s, "app", "d2", "2026-01-02")
	for _, d := range []string{"2026-01-01", "2026-01-02"} {
		if _, err := s.Rollup(ctx, "app", d); err != nil {
			t.Fatal(err)
		}
	}
	// Re-rollup is idempotent (upsert).
	if _, err := s.Rollup(ctx, "app", "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(ctx, "app", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Day != "2026-01-01" || h[1].DAU != 2 || h[1].Total != 2 {
		t.Fatalf("history: %+v", h)
	}

	n, err := s.PruneActivity(ctx, "2026-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	// Summaries and device totals survive pruning.
	h, _ = s.History(ctx, "app", "2000-01-01")
	if len(h) != 2 {
		t.Fatalf("history after prune: %+v", h)
	}
	st, _ := s.Stats(ctx, "app", "2026-01-02")
	if st.Total != 2 || st.DAU != 2 {
		t.Fatalf("stats after prune: %+v", st)
	}
}

func TestDeviceMetadataKeptWhenOmitted(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	at := day(t, "2026-05-01")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.RecordHeartbeat(ctx, store.Heartbeat{AppID: "a", DeviceID: "d", Version: "1.0", Platform: "mac", At: at}))
	must(s.RecordHeartbeat(ctx, store.Heartbeat{AppID: "a", DeviceID: "d", At: at.Add(time.Hour)}))
	var v, p string
	var first, last int64
	must(s.db.QueryRow(`SELECT version, platform, first_seen, last_seen FROM devices`).Scan(&v, &p, &first, &last))
	if v != "1.0" || p != "mac" || first != at.Unix() || last != at.Add(time.Hour).Unix() {
		t.Fatalf("device row: %s %s %d %d", v, p, first, last)
	}
}

func hbMeta(t *testing.T, s *Store, app, dev, d, version, platform string) {
	t.Helper()
	if err := s.RecordHeartbeat(context.Background(), store.Heartbeat{
		AppID: app, DeviceID: dev, Version: version, Platform: platform, At: day(t, d),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformBreakdownAndFilter(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	// today: w1 (windows), a1 (android), e1 (empty platform)
	// 3 days ago: w2 (windows) — in WAU
	// 20 days ago: w3 (windows) — in MAU only
	// 40 days ago: w4 (windows) — total only
	// other app must not leak in
	hbMeta(t, s, "app", "w1", "2026-03-31", "1.0", "windows")
	hbMeta(t, s, "app", "w1", "2026-03-31", "1.0", "windows") // same device, same day
	hbMeta(t, s, "app", "a1", "2026-03-31", "1.0", "android")
	hbMeta(t, s, "app", "e1", "2026-03-31", "", "")
	hbMeta(t, s, "app", "w2", "2026-03-28", "1.0", "windows")
	hbMeta(t, s, "app", "w3", "2026-03-11", "1.0", "windows")
	hbMeta(t, s, "app", "w4", "2026-02-19", "1.0", "windows")
	hbMeta(t, s, "other", "w1", "2026-03-31", "1.0", "windows")

	got, err := s.Stats(ctx, "app", "2026-03-31")
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Stats{AppID: "app", Day: "2026-03-31", DAU: 3, WAU: 4, MAU: 5, Total: 6}); got != want {
		t.Fatalf("overall: got %+v want %+v", got, want)
	}

	plats, err := s.PlatformBreakdown(ctx, "app", "2026-03-31")
	if err != nil {
		t.Fatal(err)
	}
	wantPlats := []store.PlatformStat{
		{Platform: "windows", DAU: 1, WAU: 2, MAU: 3, Total: 4},
		{Platform: "", DAU: 1, WAU: 1, MAU: 1, Total: 1},
		{Platform: "android", DAU: 1, WAU: 1, MAU: 1, Total: 1},
	}
	if len(plats) != len(wantPlats) {
		t.Fatalf("platforms: %+v", plats)
	}
	for i, want := range wantPlats {
		if plats[i] != want {
			t.Fatalf("platforms[%d]: got %+v want %+v\nall: %+v", i, plats[i], want, plats)
		}
	}

	win, err := s.StatsByPlatform(ctx, "app", "2026-03-31", "windows")
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Stats{AppID: "app", Day: "2026-03-31", DAU: 1, WAU: 2, MAU: 3, Total: 4}); win != want {
		t.Fatalf("windows: got %+v want %+v", win, want)
	}
	empty, err := s.StatsByPlatform(ctx, "app", "2026-03-31", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Stats{AppID: "app", Day: "2026-03-31", DAU: 1, WAU: 1, MAU: 1, Total: 1}); empty != want {
		t.Fatalf("empty platform: got %+v want %+v", empty, want)
	}
	none, err := s.StatsByPlatform(ctx, "app", "2026-03-31", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Stats{AppID: "app", Day: "2026-03-31", DAU: 0, WAU: 0, MAU: 0, Total: 0}); none != want {
		t.Fatalf("unknown platform: got %+v want %+v", none, want)
	}

	// Historic day only counts devices first seen by then (w4).
	early, err := s.PlatformBreakdown(ctx, "app", "2026-03-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(early) != 1 || early[0] != (store.PlatformStat{Platform: "windows", DAU: 0, WAU: 0, MAU: 1, Total: 1}) {
		t.Fatalf("historic breakdown: %+v", early)
	}
}

func TestRollupPlatformHistory(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	hbMeta(t, s, "app", "w1", "2026-01-01", "1.0", "windows")
	hbMeta(t, s, "app", "a1", "2026-01-02", "1.0", "android")
	hbMeta(t, s, "app", "w1", "2026-01-02", "1.0", "windows")
	hbMeta(t, s, "app", "e1", "2026-01-02", "", "")
	for _, d := range []string{"2026-01-01", "2026-01-02"} {
		if _, err := s.Rollup(ctx, "app", d); err != nil {
			t.Fatal(err)
		}
	}
	// Re-rollup is idempotent and replaces per-platform rows.
	if _, err := s.Rollup(ctx, "app", "2026-01-02"); err != nil {
		t.Fatal(err)
	}

	h, err := s.History(ctx, "app", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].DAU != 1 || h[1].DAU != 3 || h[1].Total != 3 {
		t.Fatalf("overall history: %+v", h)
	}

	win, err := s.HistoryByPlatform(ctx, "app", "2026-01-01", "windows")
	if err != nil {
		t.Fatal(err)
	}
	if len(win) != 2 || win[0] != (store.Stats{AppID: "app", Day: "2026-01-01", DAU: 1, WAU: 1, MAU: 1, Total: 1}) {
		t.Fatalf("windows day1: %+v", win)
	}
	if win[1] != (store.Stats{AppID: "app", Day: "2026-01-02", DAU: 1, WAU: 1, MAU: 1, Total: 1}) {
		t.Fatalf("windows day2: %+v", win)
	}

	andH, err := s.HistoryByPlatform(ctx, "app", "2026-01-01", "android")
	if err != nil {
		t.Fatal(err)
	}
	// android first appears on 2026-01-02, so only one summary row.
	if len(andH) != 1 || andH[0] != (store.Stats{AppID: "app", Day: "2026-01-02", DAU: 1, WAU: 1, MAU: 1, Total: 1}) {
		t.Fatalf("android history: %+v", andH)
	}

	empty, err := s.HistoryByPlatform(ctx, "app", "2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 1 || empty[0].Day != "2026-01-02" || empty[0].DAU != 1 || empty[0].Total != 1 {
		t.Fatalf("empty platform history: %+v", empty)
	}

	none, err := s.HistoryByPlatform(ctx, "app", "2026-01-01", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown platform history: %+v", none)
	}

	// A later heartbeat that fills in a previously empty platform is reflected after re-rollup.
	hbMeta(t, s, "app", "e1", "2026-01-02", "", "linux")
	if _, err := s.Rollup(ctx, "app", "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	empty, _ = s.HistoryByPlatform(ctx, "app", "2026-01-01", "")
	if len(empty) != 0 {
		t.Fatalf("empty platform should be gone after update: %+v", empty)
	}
	linux, err := s.HistoryByPlatform(ctx, "app", "2026-01-01", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if len(linux) != 1 || linux[0].DAU != 1 || linux[0].Total != 1 {
		t.Fatalf("linux history after update: %+v", linux)
	}
}
