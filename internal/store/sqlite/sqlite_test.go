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
