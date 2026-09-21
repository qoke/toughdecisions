package main

import (
	"fmt"
	"net/http"
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
func serveHTTP(cfg *config.Config, log logx.Logger) error {
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
	return http.ListenAndServe(cfg.ServerListen(), srv.Handler())
}
