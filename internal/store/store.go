// Package store defines the storage abstraction used by the service.
//
// Days are always UTC calendar days formatted as "2006-01-02".
package store

import (
	"context"
	"time"
)

// DayLayout is the canonical format of a day key.
const DayLayout = "2006-01-02"

// Day returns the UTC day key for t.
func Day(t time.Time) string { return t.UTC().Format(DayLayout) }

// Heartbeat is one device report.
type Heartbeat struct {
	AppID    string
	DeviceID string
	Version  string
	Platform string
	At       time.Time
}

// Stats is the active-device snapshot for one app on one day.
type Stats struct {
	AppID string `json:"app_id"`
	Day   string `json:"day"`
	DAU   int64  `json:"dau"`
	WAU   int64  `json:"wau"`
	MAU   int64  `json:"mau"`
	Total int64  `json:"total"`
}

// PlatformStat is the active-device snapshot for one platform of an app on one day.
type PlatformStat struct {
	Platform string `json:"platform"`
	DAU      int64  `json:"dau"`
	WAU      int64  `json:"wau"`
	MAU      int64  `json:"mau"`
	Total    int64  `json:"total"`
}

// Store persists heartbeats and aggregates.
type Store interface {
	// RecordHeartbeat upserts the device and marks it active on the heartbeat's day.
	RecordHeartbeat(ctx context.Context, hb Heartbeat) error
	// Stats computes live stats for appID as of day (inclusive windows of 1/7/30 days).
	Stats(ctx context.Context, appID, day string) (Stats, error)
	// StatsByPlatform is Stats limited to devices whose stored platform equals
	// platform (exact match, including the empty string).
	StatsByPlatform(ctx context.Context, appID, day, platform string) (Stats, error)
	// PlatformBreakdown returns per-platform stats for appID as of day,
	// ordered by total descending, then platform ascending.
	PlatformBreakdown(ctx context.Context, appID, day string) ([]PlatformStat, error)
	// Rollup computes stats for appID on day and saves them as the daily
	// summary (overall and per-platform).
	Rollup(ctx context.Context, appID, day string) (Stats, error)
	// History returns saved daily summaries for appID with day >= since, oldest first.
	History(ctx context.Context, appID, since string) ([]Stats, error)
	// HistoryByPlatform returns saved per-platform daily summaries for appID
	// with day >= since, oldest first. platform is an exact match (including "").
	HistoryByPlatform(ctx context.Context, appID, since, platform string) ([]Stats, error)
	// PruneActivity deletes per-day activity rows older than before (exclusive).
	PruneActivity(ctx context.Context, before string) (int64, error)
	// Ping checks the store is reachable.
	Ping(ctx context.Context) error
	Close() error
}
