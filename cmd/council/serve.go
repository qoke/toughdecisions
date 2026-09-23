package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/council"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/httpapi"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/store"
)

// serveHTTP wires config → store → gateway (LiteLLM) → models registry →
// runner → server and blocks on ListenAndServe at server_listen.
// A non-loopback bind requires a non-empty server_token; otherwise it
// refuses to start so the API never listens fail-open.
func serveHTTP(cfg *config.Config, log logx.Logger) error {
	if !isLoopbackListen(cfg.ServerListen()) && strings.TrimSpace(cfg.ServerToken()) == "" {
		return fmt.Errorf("non-loopback listen %q requires server_token (COUNCIL_SERVER_TOKEN)", cfg.ServerListen())
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	mreg, err := models.LoadRegistry(cfg.ModelsFile())
	if err != nil {
		return fmt.Errorf("load models: %w", err)
	}
	gw := gateway.New(&http.Client{}, cfg.GatewayBaseURL(), cfg.GatewayAPIKey(), cfg.GatewayMaxConcurrent(), log)
	reg := council.NewRegistry(5 * time.Minute)
	defer reg.Close()
	runner := council.NewRunner(db, gw, reg, cfg, log, mreg)
	srv := httpapi.New(db, runner, reg, cfg, log)
	log.Info("serving", "listen", cfg.ServerListen())
	httpSrv := &http.Server{
		Addr:              cfg.ServerListen(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return httpSrv.ListenAndServe()
}

// isLoopbackListen reports whether addr binds loopback only. The host is
// resolved (never trusted literally): an empty host, an unresolvable name,
// a non-loopback IP, or any non-loopback resolved IP is non-loopback.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}
