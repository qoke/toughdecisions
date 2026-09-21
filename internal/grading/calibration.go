package grading

import (
	"context"
	"time"

	"github.com/qoke/toughdecisions/internal/schema"
)

// Calibrate grades every calibration item with the absolute rubric per
// R-11, computes reversals, persists calibration_json + admitted, and
// returns the reversal count. A config_hash change auto-invalidates
// admission (admitted=false) before re-calibration. An empty calibration
// set is an explicit error, never a vacuous admission.
func (s *Service) Calibrate(ctx context.Context, in schema.CaseInput, acceptance string, grader *Grader) (int, error) {
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
	rows, reversals, err := s.gradeItems(ctx, in, acceptance, grader, items)
	if err != nil {
		return 0, err
	}
	if err := s.persistCalibration(grader, rows, reversals); err != nil {
		return 0, err
	}
	return reversals, nil
}

// Recheck grades n rotating calibration items (offset by ISO week) per
// R-11 and returns the reversal count. It never changes admission.
func (s *Service) Recheck(ctx context.Context, in schema.CaseInput, acceptance string, grader *Grader, n int) (int, error) {
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
	rotated := make([]calibrationItem, 0, len(items))
	for i := range items {
		rotated = append(rotated, calibrationItem{
			Key:         items[i].ItemKey,
			Seat:        items[i].Seat,
			Text:        items[i].ResponseText,
			HumanScores: items[i].HumanScoresJSON,
			HumanFlags:  items[i].HumanFlagsJSON,
		})
	}
	offset := week % len(rotated)
	ordered := append(append([]calibrationItem{}, rotated[offset:]...), rotated[:offset]...)
	if n > len(ordered) {
		n = len(ordered)
	}
	_, reversals, err := s.gradeItems(ctx, in, acceptance, grader, toStoreItems(ordered[:n]))
	if err != nil {
		return 0, err
	}
	return reversals, nil
}
