package server

import (
	"context"
	"time"

	"github.com/DYD-Tech/superlight-uma/internal/store"
)

// Maintain runs one maintenance pass: it saves daily summaries for yesterday
// (final) and today (partial, overwritten on later passes) for every app,
// then prunes activity rows older than the retention window.
func (s *Server) Maintain(ctx context.Context) error {
	now := s.Now().UTC()
	days := []string{store.Day(now.AddDate(0, 0, -1)), store.Day(now)}
	for _, app := range s.cfg.AllowedApps {
		for _, d := range days {
			if _, err := s.store.Rollup(ctx, app, d); err != nil {
				return err
			}
		}
	}
	cutoff := store.Day(now.AddDate(0, 0, -(s.cfg.RetentionDays - 1)))
	n, err := s.store.PruneActivity(ctx, cutoff)
	if err != nil {
		return err
	}
	if n > 0 {
		s.log.Info("pruned activity rows", "rows", n, "before", cutoff)
	}
	return nil
}

// RunMaintenance calls Maintain immediately and then every interval until ctx ends.
func (s *Server) RunMaintenance(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Maintain(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("maintenance", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
