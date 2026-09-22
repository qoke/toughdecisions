// Package grading implements blind absolute, pairwise, coverage, and
// calibration grading. Graders never see model names or prior results:
// blind material is render.Markdown of the parsed (or raw) response plus
// the role name only.
package grading

import (
	"fmt"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/store"
)

// Grader aliases store.GraderConfig so callers pass repository rows directly.
type Grader = store.GraderConfig

// Service owns grading against one DB, gateway client, and config.
type Service struct {
	db     *store.DB
	gw     gateway.Client
	cfg    *config.Config
	models *models.Registry
}

// NewService builds a Service. All arguments are required.
func NewService(db *store.DB, gw gateway.Client, cfg *config.Config) *Service {
	return &Service{db: db, gw: gw, cfg: cfg}
}

// SetModels attaches the model-capability registry used to select the
// response_format for grader calls. A nil registry (or an unknown model)
// sends no response_format, mirroring the view/judge mechanism.
func (s *Service) SetModels(m *models.Registry) {
	s.models = m
}

// ValidateGrader rejects a grader whose model cannot be used for grading:
// with a model registry attached, the grader model must support strict
// json_schema. Without schema enforcement the grader returns unenforced
// shapes (abbreviated criteria, invented containers), so the configuration
// is rejected before any gateway call is made. A nil registry (the
// unit-test path) skips the check; production always attaches one.
func (s *Service) ValidateGrader(grader *Grader) error {
	if grader == nil {
		return errf("grader is required")
	}
	if s.models == nil {
		return nil
	}
	if !s.models.Known(grader.Model) {
		return errf("grader %q uses unknown model %q which cannot be used for grading", grader.GraderKey, grader.Model)
	}
	_, _, _, jsonSchema, _ := s.models.Supports(grader.Model)
	if !jsonSchema {
		return errf("grader %q uses model %q which does not support strict structured outputs and cannot be used for grading", grader.GraderKey, grader.Model)
	}
	return nil
}

func strPtr(s string) *string { return &s }

func errf(format string, args ...any) error {
	return fmt.Errorf("grading: "+format, args...)
}
