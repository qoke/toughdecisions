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

func strPtr(s string) *string { return &s }

func errf(format string, args ...any) error {
	return fmt.Errorf("grading: "+format, args...)
}
