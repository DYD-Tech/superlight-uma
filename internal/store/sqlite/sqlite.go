// Package sqlite implements store.Store on SQLite via the pure-Go modernc driver.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/DYD-Tech/superlight-uma/internal/store"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const schema = `
CREATE TABLE IF NOT EXISTS devices (
	app_id     TEXT    NOT NULL,
	device_id  TEXT    NOT NULL,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	first_day  TEXT    NOT NULL,
	version    TEXT    NOT NULL DEFAULT '',
	platform   TEXT    NOT NULL DEFAULT '',
	PRIMARY KEY (app_id, device_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS devices_first_day ON devices (app_id, first_day);

CREATE TABLE IF NOT EXISTS daily_activity (
	app_id    TEXT NOT NULL,
	day       TEXT NOT NULL,
	device_id TEXT NOT NULL,
	PRIMARY KEY (app_id, day, device_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS daily_activity_day ON daily_activity (day);

CREATE TABLE IF NOT EXISTS daily_summary (
	app_id     TEXT    NOT NULL,
	day        TEXT    NOT NULL,
	dau        INTEGER NOT NULL,
	wau        INTEGER NOT NULL,
	mau        INTEGER NOT NULL,
	total      INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (app_id, day)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS daily_platform_summary (
	app_id     TEXT    NOT NULL,
	day        TEXT    NOT NULL,
	platform   TEXT    NOT NULL,
	dau        INTEGER NOT NULL,
	wau        INTEGER NOT NULL,
	mau        INTEGER NOT NULL,
	total      INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (app_id, day, platform)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS daily_platform_summary_platform
	ON daily_platform_summary (app_id, platform, day);
`

// Store is a SQLite-backed store.Store.
type Store struct {
	db *sql.DB
}

var _ store.Store = (*Store)(nil)

// Open opens (creating if needed) the database at path and applies the schema.
// Use ":memory:" for an in-memory database (tests).
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection keeps things
	// simple and is plenty for heartbeat traffic. It also keeps ":memory:" sane.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) RecordHeartbeat(ctx context.Context, hb store.Heartbeat) error {
	day := store.Day(hb.At)
	ts := hb.At.Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO devices (app_id, device_id, first_seen, last_seen, first_day, version, platform)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (app_id, device_id) DO UPDATE SET
			last_seen = MAX(last_seen, excluded.last_seen),
			version   = CASE WHEN excluded.version  <> '' THEN excluded.version  ELSE version  END,
			platform  = CASE WHEN excluded.platform <> '' THEN excluded.platform ELSE platform END`,
		hb.AppID, hb.DeviceID, ts, ts, day, hb.Version, hb.Platform); err != nil {
		return fmt.Errorf("upsert device: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO daily_activity (app_id, day, device_id) VALUES (?, ?, ?)`,
		hb.AppID, day, hb.DeviceID); err != nil {
		return fmt.Errorf("insert activity: %w", err)
	}
	return tx.Commit()
}

func (s *Store) Stats(ctx context.Context, appID, day string) (store.Stats, error) {
	d, err := time.Parse(store.DayLayout, day)
	if err != nil {
		return store.Stats{}, fmt.Errorf("bad day %q: %w", day, err)
	}
	from7 := d.AddDate(0, 0, -6).Format(store.DayLayout)
	from30 := d.AddDate(0, 0, -29).Format(store.DayLayout)
	st := store.Stats{AppID: appID, Day: day}
	err = s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM daily_activity WHERE app_id = ?1 AND day = ?2),
			(SELECT COUNT(DISTINCT device_id) FROM daily_activity WHERE app_id = ?1 AND day BETWEEN ?3 AND ?2),
			(SELECT COUNT(DISTINCT device_id) FROM daily_activity WHERE app_id = ?1 AND day BETWEEN ?4 AND ?2),
			(SELECT COUNT(*) FROM devices WHERE app_id = ?1 AND first_day <= ?2)`,
		appID, day, from7, from30).Scan(&st.DAU, &st.WAU, &st.MAU, &st.Total)
	if err != nil {
		return store.Stats{}, err
	}
	return st, nil
}

func (s *Store) StatsByPlatform(ctx context.Context, appID, day, platform string) (store.Stats, error) {
	d, err := time.Parse(store.DayLayout, day)
	if err != nil {
		return store.Stats{}, fmt.Errorf("bad day %q: %w", day, err)
	}
	from7 := d.AddDate(0, 0, -6).Format(store.DayLayout)
	from30 := d.AddDate(0, 0, -29).Format(store.DayLayout)
	st := store.Stats{AppID: appID, Day: day}
	err = s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM daily_activity a
				JOIN devices d ON d.app_id = a.app_id AND d.device_id = a.device_id
				WHERE a.app_id = ?1 AND a.day = ?2 AND d.platform = ?5),
			(SELECT COUNT(DISTINCT a.device_id) FROM daily_activity a
				JOIN devices d ON d.app_id = a.app_id AND d.device_id = a.device_id
				WHERE a.app_id = ?1 AND a.day BETWEEN ?3 AND ?2 AND d.platform = ?5),
			(SELECT COUNT(DISTINCT a.device_id) FROM daily_activity a
				JOIN devices d ON d.app_id = a.app_id AND d.device_id = a.device_id
				WHERE a.app_id = ?1 AND a.day BETWEEN ?4 AND ?2 AND d.platform = ?5),
			(SELECT COUNT(*) FROM devices WHERE app_id = ?1 AND first_day <= ?2 AND platform = ?5)`,
		appID, day, from7, from30, platform).Scan(&st.DAU, &st.WAU, &st.MAU, &st.Total)
	if err != nil {
		return store.Stats{}, err
	}
	return st, nil
}

func (s *Store) PlatformBreakdown(ctx context.Context, appID, day string) ([]store.PlatformStat, error) {
	d, err := time.Parse(store.DayLayout, day)
	if err != nil {
		return nil, fmt.Errorf("bad day %q: %w", day, err)
	}
	from7 := d.AddDate(0, 0, -6).Format(store.DayLayout)
	from30 := d.AddDate(0, 0, -29).Format(store.DayLayout)
	rows, err := s.db.QueryContext(ctx, `
		WITH plat AS (
			SELECT platform, COUNT(*) AS total
			FROM devices
			WHERE app_id = ?1 AND first_day <= ?2
			GROUP BY platform
		),
		dau AS (
			SELECT d.platform, COUNT(*) AS n
			FROM daily_activity a
			JOIN devices d ON d.app_id = a.app_id AND d.device_id = a.device_id
			WHERE a.app_id = ?1 AND a.day = ?2
			GROUP BY d.platform
		),
		wau AS (
			SELECT d.platform, COUNT(DISTINCT a.device_id) AS n
			FROM daily_activity a
			JOIN devices d ON d.app_id = a.app_id AND d.device_id = a.device_id
			WHERE a.app_id = ?1 AND a.day BETWEEN ?3 AND ?2
			GROUP BY d.platform
		),
		mau AS (
			SELECT d.platform, COUNT(DISTINCT a.device_id) AS n
			FROM daily_activity a
			JOIN devices d ON d.app_id = a.app_id AND d.device_id = a.device_id
			WHERE a.app_id = ?1 AND a.day BETWEEN ?4 AND ?2
			GROUP BY d.platform
		)
		SELECT plat.platform,
			COALESCE(dau.n, 0),
			COALESCE(wau.n, 0),
			COALESCE(mau.n, 0),
			plat.total
		FROM plat
		LEFT JOIN dau ON dau.platform = plat.platform
		LEFT JOIN wau ON wau.platform = plat.platform
		LEFT JOIN mau ON mau.platform = plat.platform
		ORDER BY plat.total DESC, plat.platform ASC`,
		appID, day, from7, from30)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.PlatformStat{}
	for rows.Next() {
		var p store.PlatformStat
		if err := rows.Scan(&p.Platform, &p.DAU, &p.WAU, &p.MAU, &p.Total); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Rollup(ctx context.Context, appID, day string) (store.Stats, error) {
	st, err := s.Stats(ctx, appID, day)
	if err != nil {
		return st, err
	}
	plats, err := s.PlatformBreakdown(ctx, appID, day)
	if err != nil {
		return st, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO daily_summary (app_id, day, dau, wau, mau, total, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (app_id, day) DO UPDATE SET
			dau = excluded.dau, wau = excluded.wau, mau = excluded.mau,
			total = excluded.total, updated_at = excluded.updated_at`,
		appID, day, st.DAU, st.WAU, st.MAU, st.Total, now); err != nil {
		return st, err
	}
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM daily_platform_summary WHERE app_id = ? AND day = ?`,
		appID, day); err != nil {
		return st, err
	}
	for _, p := range plats {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO daily_platform_summary
				(app_id, day, platform, dau, wau, mau, total, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			appID, day, p.Platform, p.DAU, p.WAU, p.MAU, p.Total, now); err != nil {
			return st, err
		}
	}
	return st, tx.Commit()
}

func (s *Store) History(ctx context.Context, appID, since string) ([]store.Stats, error) {
	return s.history(ctx, `
		SELECT day, dau, wau, mau, total FROM daily_summary
		WHERE app_id = ? AND day >= ? ORDER BY day`, appID, since)
}

func (s *Store) HistoryByPlatform(ctx context.Context, appID, since, platform string) ([]store.Stats, error) {
	return s.history(ctx, `
		SELECT day, dau, wau, mau, total FROM daily_platform_summary
		WHERE app_id = ? AND platform = ? AND day >= ? ORDER BY day`, appID, platform, since)
}

func (s *Store) history(ctx context.Context, query, appID string, args ...any) ([]store.Stats, error) {
	rows, err := s.db.QueryContext(ctx, query, append([]any{appID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Stats{}
	for rows.Next() {
		st := store.Stats{AppID: appID}
		if err := rows.Scan(&st.Day, &st.DAU, &st.WAU, &st.MAU, &st.Total); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) PruneActivity(ctx context.Context, before string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM daily_activity WHERE day < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
