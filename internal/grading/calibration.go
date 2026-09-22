package grading

import (
	"context"
	"time"

	"github.com/qoke/toughdecisions/internal/store"
)

// Calibrate grades every calibration item with the absolute rubric per
// R-11, computes reversals, persists calibration_json + admitted, and
// returns the reversal count. A config_hash change auto-invalidates
// admission (admitted=false) before re-calibration. An empty calibration
// set is an explicit error, never a vacuous admission.
//
// Each item is graded against its own case: the item's CaseID resolves the
// stored case InputJSON (the same CaseInput normal grading builds via
// harness.CaseInputFor) and its family's AcceptanceJSON. A missing,
// undecodable, or empty case fails closed — an empty case must never
// quietly produce a 0 score.
func (s *Service) Calibrate(ctx context.Context, grader *Grader) (int, error) {
	if grader == nil {
		return 0, errf("calibrate requires a grader")
	}
	if err := s.invalidateAdmission(grader.ConfigHash); err != nil {
		return 0, err
	}
	items, err := s.db.ListCalibrationItems()
	if err != nil {
		return 0, errf("list calibration items: %v", err)
	}
	if len(items) == 0 {
		return 0, errf("no calibration items: refusing vacuous admission")
	}
	rows, reversals, err := s.gradeItems(ctx, grader, items)
	if err != nil {
		return 0, err
	}
	if err := s.persistCalibration(grader, rows, reversals); err != nil {
		return 0, err
	}
	return reversals, nil
}

// Recheck grades n rotating calibration items (offset by ISO week) per
// R-11 and returns the reversal count. It never changes admission. Like
// Calibrate it resolves each item's own case and fails closed on a
// missing, undecodable, or empty case.
func (s *Service) Recheck(ctx context.Context, grader *Grader, n int) (int, error) {
	if grader == nil {
		return 0, errf("recheck requires a grader")
	}
	if n <= 0 {
		return 0, errf("recheck requires n > 0")
	}
	items, err := s.db.ListCalibrationItems()
	if err != nil {
		return 0, errf("list calibration items: %v", err)
	}
	if len(items) == 0 {
		return 0, errf("no calibration items: refusing vacuous recheck")
	}
	_, week := time.Now().UTC().ISOWeek()
	offset := week % len(items)
	ordered := append(append([]*store.CalibrationItem{}, items[offset:]...), items[:offset]...)
	if n > len(ordered) {
		n = len(ordered)
	}
	_, reversals, err := s.gradeItems(ctx, grader, ordered[:n])
	if err != nil {
		return 0, err
	}
	return reversals, nil
}
