// Package server implements the HTTP API.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DYD-Tech/superlight-uma/internal/config"
	"github.com/DYD-Tech/superlight-uma/internal/store"
)

const (
	maxBodyBytes = 4 << 10
	maxFieldLen  = 64
	maxHistory   = 3650
)

// Server wires config, store and HTTP handlers.
type Server struct {
	cfg     config.Config
	store   store.Store
	log     *slog.Logger
	limiter *ipLimiter
	apps    map[string]bool
	// Now returns the current time; overridable in tests.
	Now func() time.Time
}

// New creates a Server.
func New(cfg config.Config, st store.Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, store: st, log: log, apps: map[string]bool{}, Now: time.Now}
	for _, a := range cfg.AllowedApps {
		s.apps[a] = true
	}
	if cfg.RateLimit > 0 {
		s.limiter = newIPLimiter(cfg.RateLimit, cfg.RateBurst)
	}
	return s
}

// Handler returns the HTTP handler for all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("GET /v1/apps", s.admin(s.handleApps))
	mux.HandleFunc("GET /v1/stats", s.admin(s.handleStats))
	mux.HandleFunc("GET /v1/stats/history", s.admin(s.handleHistory))
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return s.cors(mux)
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"apps": s.cfg.AllowedApps})
}

type heartbeatReq struct {
	AppID    string `json:"app_id"`
	DeviceID string `json:"device_id"`
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform,omitempty"`
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if s.limiter != nil && !s.limiter.allow(s.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var req heartbeatReq
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !s.apps[req.AppID] {
		writeErr(w, http.StatusForbidden, "unknown app_id")
		return
	}
	id, ok := normalizeUUID(req.DeviceID)
	if !ok {
		writeErr(w, http.StatusBadRequest, "device_id must be a UUID")
		return
	}
	if len(req.Version) > maxFieldLen || len(req.Platform) > maxFieldLen {
		writeErr(w, http.StatusBadRequest, "version/platform too long")
		return
	}
	hb := store.Heartbeat{
		AppID:    req.AppID,
		DeviceID: id,
		Version:  strings.TrimSpace(req.Version),
		Platform: strings.ToLower(strings.TrimSpace(req.Platform)),
		At:       s.Now().UTC(),
	}
	if err := s.store.RecordHeartbeat(r.Context(), hb); err != nil {
		s.log.Error("record heartbeat", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type statsResponse struct {
	store.Stats
	Platforms []store.PlatformStat `json:"platforms"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appParam(w, r)
	if !ok {
		return
	}
	platform, filtered, ok := s.platformParam(w, r)
	if !ok {
		return
	}
	day := store.Day(s.Now())
	var (
		st  store.Stats
		err error
	)
	if filtered {
		st, err = s.store.StatsByPlatform(r.Context(), app, day, platform)
	} else {
		st, err = s.store.Stats(r.Context(), app, day)
	}
	if err != nil {
		s.log.Error("stats", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	plats, err := s.store.PlatformBreakdown(r.Context(), app, day)
	if err != nil {
		s.log.Error("platform breakdown", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if plats == nil {
		plats = []store.PlatformStat{}
	}
	writeJSON(w, http.StatusOK, statsResponse{Stats: st, Platforms: plats})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appParam(w, r)
	if !ok {
		return
	}
	platform, filtered, ok := s.platformParam(w, r)
	if !ok {
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxHistory {
			writeErr(w, http.StatusBadRequest, "days must be 1-3650")
			return
		}
		days = n
	}
	since := store.Day(s.Now().UTC().AddDate(0, 0, -(days - 1)))
	var (
		hist []store.Stats
		err  error
	)
	if filtered {
		hist, err = s.store.HistoryByPlatform(r.Context(), app, since, platform)
	} else {
		hist, err = s.store.History(r.Context(), app, since)
	}
	if err != nil {
		s.log.Error("history", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"app_id": app, "days": hist})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) appParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	app := r.URL.Query().Get("app_id")
	if app == "" {
		writeErr(w, http.StatusBadRequest, "app_id is required")
		return "", false
	}
	if !s.apps[app] {
		writeErr(w, http.StatusNotFound, "unknown app_id")
		return "", false
	}
	return app, true
}

// platformParam parses the optional platform query parameter.
// If the parameter is absent, filtered is false (all platforms).
// If it is present — including ?platform= — the value is trimmed and
// lowercased and filtered is true; a value longer than maxFieldLen is a 400.
func (s *Server) platformParam(w http.ResponseWriter, r *http.Request) (string, bool, bool) {
	if !r.URL.Query().Has("platform") {
		return "", false, true
	}
	p := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("platform")))
	if len(p) > maxFieldLen {
		writeErr(w, http.StatusBadRequest, "platform too long")
		return "", false, false
	}
	return p, true, true
}

// admin protects next with the admin bearer key. With no key configured the
// admin endpoints are open (a warning is logged at startup).
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	if s.cfg.AdminKey == "" {
		return next
	}
	want := []byte("Bearer " + s.cfg.AdminKey)
	return func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="superlight-uma"`)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// normalizeUUID accepts the canonical 8-4-4-4-12 hex form (any case) and
// returns it lowercased.
func normalizeUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return "", false
			}
		}
	}
	return string(b), true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
