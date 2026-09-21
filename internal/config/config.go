// Package config provides the cfggo-backed council configuration.
package config

import (
	"time"

	"github.com/iqhive/cfggo"
)

// Config is the council configuration. Each field is a cfggo accessor.
type Config struct {
	cfggo.Structure
	ServerListen              func() string        `cfggo:"server_listen" default:":8080" help:"HTTP listen address"`
	DBPath                    func() string        `cfggo:"db_path" default:"./data/council.db" help:"SQLite database path"`
	GatewayBaseURL            func() string        `cfggo:"gateway_base_url" default:"http://localhost:4000" help:"LiteLLM base URL"`
	GatewayAPIKey             func() string        `cfggo:"gateway_api_key" default:"" secret:"true" help:"LiteLLM API key"`
	GatewayMaxConcurrent      func() int           `cfggo:"gateway_max_concurrent" default:"8" help:"Max concurrent gateway calls"`
	ViewsDeadline             func() time.Duration `cfggo:"views_deadline" default:"30s" help:"Parallel views deadline"`
	JudgeDeadline             func() time.Duration `cfggo:"judge_deadline" default:"30s" help:"Judge deadline"`
	RewriteDeadline           func() time.Duration `cfggo:"rewrite_deadline" default:"20s" help:"Rewrite call deadline"`
	RewriteSeat               func() string        `cfggo:"rewrite_seat" default:"judge" help:"Pack seat used for rewrites"`
	CasepackDir               func() string        `cfggo:"casepack_dir" default:"./casepack" help:"Case pack directory"`
	PackFile                  func() string        `cfggo:"pack_file" default:"./config/pack.yaml" help:"Initial pack file"`
	GradersFile               func() string        `cfggo:"graders_file" default:"./config/graders.yaml" help:"Graders file"`
	ModelsFile                func() string        `cfggo:"models_file" default:"./config/models.yaml" help:"Model capabilities file"`
	CandidatesFile            func() string        `cfggo:"candidates_file" default:"./config/candidates.yaml" help:"Candidates file"`
	ReportsDir                func() string        `cfggo:"reports_dir" default:"./reports" help:"Reports output directory"`
	NotifyWebhookURL          func() string        `cfggo:"notify_webhook_url" default:"" help:"n8n webhook URL; empty skips notify"`
	HarnessConcurrency        func() int           `cfggo:"harness_concurrency" default:"3" help:"Harness worker pool size"`
	HarnessSentinelCount      func() int           `cfggo:"harness_sentinel_count" default:"4" help:"Sentinel cases per run"`
	HarnessScreenCases        func() int           `cfggo:"harness_screen_cases" default:"6" help:"Screen cases per candidate"`
	HarnessCalibrationRecheck func() int           `cfggo:"harness_calibration_recheck" default:"3" help:"Calibration items per grader per week"`
	HarnessBlockOnGraderDrift func() bool          `cfggo:"harness_block_on_grader_drift" default:"true" help:"Block later steps on grader drift"`
	PromoRoleMinMean          func() float64       `cfggo:"promo_role_min_mean" default:"3.0" help:"Promotion role_execution mean threshold"`
	PromoRoleMinFloor         func() int           `cfggo:"promo_role_min_floor" default:"2" help:"Promotion role_execution floor"`
	PromoSpeedRatio           func() float64       `cfggo:"promo_speed_ratio" default:"0.8" help:"Materially better speed ratio"`
	AdmitMaxReversals         func() int           `cfggo:"admit_max_reversals" default:"1" help:"Grader admission max reversals"`
	RetentionProductionDays   func() int           `cfggo:"retention_production_days" default:"90" help:"Production row retention days"`
	LogLevel                  func() string        `cfggo:"log_level" default:"info" help:"Log level: debug|info|warn|error"`
	LogRedactContent          func() bool          `cfggo:"log_redact_content" default:"true" help:"Redact card/message/response text from logs"`
}

// Load constructs the Config, initialises it from env (COUNCIL_ prefix),
// and fails fast on invalid values.
func Load() (*Config, error) {
	cfg := &Config{}
	err := cfggo.Init(cfg,
		cfggo.WithSnakeCaseFieldNames(true),
		cfggo.WithoutFlags(),
		cfggo.WithEnvPrefix("COUNCIL_"),
		cfggo.WithValidation("log_level", cfggo.OneOf("debug", "info", "warn", "error")),
	)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// RedactedString returns header-safe config output with secrets masked.
func (c *Config) RedactedString() string {
	return c.Report()
}
