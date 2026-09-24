package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/hash"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/store"
	"gopkg.in/yaml.v3"
)

// gatewayFactory builds the gateway client for grader model calls. It is a
// variable so CLI tests inject gateway.Fake with temp dirs and no network.
var gatewayFactory = defaultGateway

func defaultGateway(cfg *config.Config, log logx.Logger) gateway.Client {
	return gateway.New(newHTTPClient(), cfg.GatewayBaseURL(), normalizeGatewayKey(cfg.GatewayAPIKey()), cfg.GatewayMaxConcurrent(), log)
}

type gradersFile struct {
	Graders []graderEntry `yaml:"graders"`
}

type graderEntry struct {
	Key             string   `yaml:"key"`
	Model           string   `yaml:"model"`
	Family          string   `yaml:"family"`
	Role            string   `yaml:"role"`
	Temperature     *float64 `yaml:"temperature"`
	TopP            *float64 `yaml:"top_p"`
	ReasoningEffort string   `yaml:"reasoning_effort"`
	MaxOutputTokens int      `yaml:"max_output_tokens"`
}

// graderConfigHash is the stable identity for a graders.yaml entry: sha256
// over the canonical JSON of model+family+params. A changed entry gets a
// new config_hash row that starts unadmitted until calibrated.
func graderConfigHash(e graderEntry) string {
	return hash.SHA256Hex(hash.CanonicalJSON(map[string]any{
		"family": e.Family, "model": e.Model,
		"params": map[string]any{
			"max_output_tokens": e.MaxOutputTokens,
			"reasoning_effort":  e.ReasoningEffort,
			"temperature":       e.Temperature, "top_p": e.TopP,
		},
	}))
}

// decodeGradersStrict unmarshals graders YAML while rejecting
// unknown/misspelled fields, matching internal/casepack's strict decoder.
func decodeGradersStrict(raw []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(v)
}

// syncGradersFile parses the graders file, upserts every entry by
// config_hash, and returns the stored rows.
func syncGradersFile(db *store.DB, path string) ([]*store.GraderConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read graders %s: %w", path, err)
	}
	var f gradersFile
	if err := decodeGradersStrict(raw, &f); err != nil {
		return nil, fmt.Errorf("parse graders %s: %w", path, err)
	}
	if len(f.Graders) == 0 {
		return nil, fmt.Errorf("no graders in %s", path)
	}
	rubric := prompts.RubricHash()
	var out []*store.GraderConfig
	for _, e := range f.Graders {
		if strings.TrimSpace(e.Key) == "" {
			return nil, fmt.Errorf("grader with empty key in %s", path)
		}
		if strings.TrimSpace(e.Model) == "" {
			return nil, fmt.Errorf("grader %q has empty model", e.Key)
		}
		switch e.Role {
		case "selection", "screening", "substitute":
		default:
			return nil, fmt.Errorf("grader %q has invalid role %q (want selection|screening|substitute)", e.Key, e.Role)
		}
		params, _ := json.Marshal(map[string]any{
			"max_output_tokens": e.MaxOutputTokens,
			"reasoning_effort":  e.ReasoningEffort,
			"temperature":       e.Temperature, "top_p": e.TopP,
		})
		stored, err := db.UpsertGraderConfig(&store.GraderConfig{
			GraderKey: e.Key, Model: e.Model, Family: e.Family,
			ParamsJSON: string(params), RubricHash: rubric,
			ConfigHash: graderConfigHash(e), Role: e.Role,
		})
		if err != nil {
			return nil, fmt.Errorf("upsert grader %q: %w", e.Key, err)
		}
		out = append(out, stored)
	}
	return out, nil
}

// gradersStatus syncs the graders file and prints admission state.
func gradersStatus(args []string) int {
	fs := flag.NewFlagSet("graders status", flag.ContinueOnError)
	file := fs.String("file", "", "graders YAML file (default: graders_file from config)")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	db, cfg, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "graders status: %v\n", err)
		return exitError
	}
	defer db.Close()
	path := *file
	if path == "" {
		path = cfg.GradersFile()
	}
	stored, err := syncGradersFile(db, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "graders status: %v\n", err)
		return exitError
	}
	rows, err := db.ListGraderConfigs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "graders status: %v\n", err)
		return exitError
	}
	_ = stored
	sort.Slice(rows, func(i, j int) bool { return rows[i].GraderKey < rows[j].GraderKey })
	fmt.Printf("graders status: ok (%d graders)\n", len(rows))
	for _, g := range rows {
		state := "pending"
		if g.Admitted {
			state = "admitted"
		}
		fmt.Printf("  %s (%s): %s\n", g.GraderKey, g.Role, state)
	}
	return exitOK
}

// gradersCalibrate syncs the graders file then calibrates the selected
// graders. Reversals above admit_max_reversals exit 3 (blocked).
func gradersCalibrate(args []string) int {
	fs := flag.NewFlagSet("graders calibrate", flag.ContinueOnError)
	file := fs.String("file", "", "graders YAML file (default: graders_file from config)")
	only := fs.String("grader", "", "calibrate only this grader key")
	all := fs.Bool("all", false, "calibrate all graders")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if *only != "" && *all {
		fmt.Fprintf(os.Stderr, "graders calibrate: --grader and --all are mutually exclusive\n")
		return exitValidation
	}
	db, cfg, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "graders calibrate: %v\n", err)
		return exitError
	}
	defer db.Close()
	if err := requireGatewayKey(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "graders calibrate: %v\n", err)
		return exitError
	}
	path := *file
	if path == "" {
		path = cfg.GradersFile()
	}
	stored, err := syncGradersFile(db, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "graders calibrate: %v\n", err)
		return exitError
	}
	var targets []*store.GraderConfig
	switch {
	case *only != "":
		found := false
		for _, g := range stored {
			if g.GraderKey == *only {
				targets = append(targets, g)
				found = true
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "graders calibrate: unknown grader %q\n", *only)
			return exitValidation
		}
	case *all:
		targets = stored
	default:
		fmt.Fprintf(os.Stderr, "graders calibrate: need --grader <key> or --all\n")
		return exitValidation
	}
	mreg, err := models.LoadRegistry(cfg.ModelsFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "graders calibrate: %v\n", err)
		return exitError
	}
	_ = mreg
	log := logx.New(cfg)
	gw := gatewayFactory(cfg, log)
	svc := grading.NewService(db, gw, cfg)
	svc.SetModels(mreg)
	blocked := false
	failed := false
	for _, g := range targets {
		reversals, err := svc.Calibrate(context.Background(), (*grading.Grader)(g))
		if err != nil {
			// Record the hard failure for this grader (failed/absent,
			// with the reason) and keep evaluating the others — one
			// unparseable grader must not mask the remaining results.
			if ferr := svc.RecordCalibrationFailure((*grading.Grader)(g), err); ferr != nil {
				fmt.Fprintf(os.Stderr, "graders calibrate: %s: %v (record failure: %v)\n", g.GraderKey, err, ferr)
			} else {
				fmt.Fprintf(os.Stderr, "graders calibrate: %s: %v\n", g.GraderKey, err)
			}
			failed = true
			continue
		}
		status := "admitted"
		if reversals > cfg.AdmitMaxReversals() {
			status = "blocked"
			blocked = true
		}
		fmt.Printf("graders calibrate: %s reversals=%d %s\n", g.GraderKey, reversals, status)
	}
	if failed {
		return exitError
	}
	if blocked {
		return exitBlocked
	}
	return exitOK
}
