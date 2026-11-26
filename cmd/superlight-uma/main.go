// Command superlight-uma is a tiny anonymous active-device counter.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DYD-Tech/superlight-uma/internal/config"
	"github.com/DYD-Tech/superlight-uma/internal/server"
	"github.com/DYD-Tech/superlight-uma/internal/store/sqlite"
)

// version is set at build time: -ldflags "-X main.version=<git sha>".
var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	st, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if cfg.AdminKey == "" {
		log.Warn("UMA_ADMIN_KEY is empty: admin endpoints (/v1/stats, /v1/stats/history, /v1/apps) are open to anyone without a key")
	}
	srv := server.New(cfg, st, log)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	maintDone := make(chan struct{})
	go func() {
		defer close(maintDone)
		srv.RunMaintenance(ctx, cfg.RollupEvery)
	}()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "version", version, "addr", cfg.Addr, "db", cfg.DBPath, "apps", cfg.AllowedApps)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		stop()
		<-maintDone
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shutCtx)
	<-maintDone
	// Final rollup so today's summary reflects everything received.
	if mErr := srv.Maintain(shutCtx); mErr != nil {
		log.Error("final maintenance", "err", mErr)
	}
	return err
}
