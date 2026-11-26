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

func (s *Store) Rollup(ctx context.Context, appID, day string) (store.Stats, error) {
	st, err := s.Stats(ctx, appID, day)
	if err != nil {
		return st, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO daily_summary (app_id, day, dau, wau, mau, total, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (app_id, day) DO UPDATE SET
			dau = excluded.dau, wau = excluded.wau, mau = excluded.mau,
			total = excluded.total, updated_at = excluded.updated_at`,
		appID, day, st.DAU, st.WAU, st.MAU, st.Total, time.Now().Unix())
	return st, err
}

func (s *Store) History(ctx context.Context, appID, since string) ([]store.Stats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT day, dau, wau, mau, total FROM daily_summary
		WHERE app_id = ? AND day >= ? ORDER BY day`, appID, since)
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
