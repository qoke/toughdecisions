package grading

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// calibrationRow is one persisted per-item calibration outcome.
type calibrationRow struct {
	ItemKey    string         `json:"item_key"`
	HumanMean  float64        `json:"human_mean"`
	GraderMean float64        `json:"grader_mean"`
	HumanFlag  bool           `json:"human_flagged"`
	GraderFlag bool           `json:"grader_flagged"`
	Reversal   bool           `json:"reversal"`
	Scores     map[string]int `json:"scores"`
}

// resolvedItem pairs a calibration row with its resolved case context.
type resolvedItem struct {
	item       *store.CalibrationItem
	Input      schema.CaseInput
	Acceptance string
}

// invalidateAdmission sets admitted=false before re-calibration so a
// config_hash change can never inherit a stale admission.
func (s *Service) invalidateAdmission(configHash string) error {
	cfg, err := s.db.GetGraderConfig(configHash)
	if err != nil {
		return errf("load grader config: %v", err)
	}
	cal := "{}"
	if cfg.CalibrationJSON != nil {
		cal = *cfg.CalibrationJSON
	}
	if err := s.db.SetGraderCalibration(configHash, cal, false, cfg.AdmittedAt); err != nil {
		return errf("invalidate admission: %v", err)
	}
	return nil
}

// persistCalibration stores calibration_json and the admission decision:
// admitted = reversals <= AdmitMaxReversals.
func (s *Service) persistCalibration(grader *Grader, rows []calibrationRow, reversals int) error {
	raw, err := json.Marshal(map[string]any{"items": rows, "reversals": reversals})
	if err != nil {
		return errf("marshal calibration: %v", err)
	}
	admitted := reversals <= s.cfg.AdmitMaxReversals()
	var at *string
	if admitted {
		at = strPtr(time.Now().UTC().Format(time.RFC3339))
	}
	if err := s.db.SetGraderCalibration(grader.ConfigHash, string(raw), admitted, at); err != nil {
		return errf("store calibration: %v", err)
	}
	return nil
}

// gradeItems resolves each item's own case (stored case InputJSON decoded
// exactly like harness.CaseInputFor, plus the owning family's
// AcceptanceJSON), grades it with the calibration rubric (one R-13 retry
// each), and computes the reversal test per item. It parses through the
// same tolerant parseAbsoluteGrade as the Grade path, so an identical
// real-model payload parses identically in both paths. A missing,
// undecodable, or empty case fails closed: an empty CaseInput must never
// quietly produce a 0 score.
func (s *Service) gradeItems(ctx context.Context, grader *Grader, items []*store.CalibrationItem) ([]calibrationRow, int, error) {
	resolved := make([]resolvedItem, 0, len(items))
	for _, it := range items {
		in, acceptance, err := s.caseContextForItem(it)
		if err != nil {
			return nil, 0, err
		}
		resolved = append(resolved, resolvedItem{item: it, Input: in, Acceptance: acceptance})
	}
	rows := make([]calibrationRow, 0, len(resolved))
	reversals := 0
	for _, rit := range resolved {
		it := rit.item
		msgs, err := prompts.BuildCalibrationGrade(rit.Input, rit.Acceptance, it.Seat, it.ResponseText)
		if err != nil {
			return nil, 0, err
		}
		rf := s.graderResponseFormat(grader.Model)
		content, err := s.chatWithFormat(ctx, grader, msgs, rf)
		if err != nil {
			return nil, 0, err
		}
		g, ok := parseAbsoluteGrade(content)
		if !ok {
			retry := append(append([]gateway.Message{}, msgs...),
				gateway.Message{Role: "user", Content: "Return only the JSON object."})
			content, err = s.chatWithFormat(ctx, grader, retry, rf)
			if err != nil {
				return nil, 0, err
			}
			var ok2 bool
			g, ok2 = parseAbsoluteGrade(content)
			if !ok2 {
				return nil, 0, errf("grader %q returned unparseable calibration grade after one retry", grader.GraderKey)
			}
		}
		humanScores := map[string]int{}
		if err := json.Unmarshal([]byte(it.HumanScoresJSON), &humanScores); err != nil {
			return nil, 0, errf("decode human scores for %q: %v", it.ItemKey, err)
		}
		var humanFlags []string
		if strings.TrimSpace(it.HumanFlagsJSON) != "" {
			if err := json.Unmarshal([]byte(it.HumanFlagsJSON), &humanFlags); err != nil {
				return nil, 0, errf("decode human flags for %q: %v", it.ItemKey, err)
			}
		}
		row := calibrationRow{
			ItemKey:    it.ItemKey,
			HumanMean:  mean(humanScores),
			GraderMean: mean(g.Scores),
			HumanFlag:  len(humanFlags) > 0,
			GraderFlag: len(g.Flags) > 0,
			Scores:     g.Scores,
		}
		row.Reversal = row.HumanFlag != row.GraderFlag ||
			math.Abs(row.HumanMean-row.GraderMean) >= 1.5
		if row.Reversal {
			reversals++
		}
		rows = append(rows, row)
	}
	return rows, reversals, nil
}

// caseContextForItem resolves one calibration item's own case: the stored
// case InputJSON decoded exactly like harness.CaseInputFor, plus the owning
// family's AcceptanceJSON. A missing case row, undecodable InputJSON, or an
// empty CaseInput (no card content, no messages, no question) fails closed
// — it must never silently grade a blank card into a 0 score.
func (s *Service) caseContextForItem(it *store.CalibrationItem) (schema.CaseInput, string, error) {
	var zero schema.CaseInput
	if it == nil || strings.TrimSpace(it.CaseID) == "" {
		key := ""
		if it != nil {
			key = it.ItemKey
		}
		return zero, "", errf("calibration %q has no case linkage: refusing blank case", key)
	}
	c, err := s.db.GetCase(it.CaseID)
	if err != nil {
		return zero, "", errf("calibration %q: load case: %v", it.ItemKey, err)
	}
	var in schema.CaseInput
	if err := json.Unmarshal([]byte(c.InputJSON), &in); err != nil {
		return zero, "", errf("calibration %q: decode case %q input: %v", it.ItemKey, c.CaseKey, err)
	}
	if caseInputEmpty(in) {
		return zero, "", errf("calibration %q: case %q has empty input: refusing blank case", it.ItemKey, c.CaseKey)
	}
	acceptance := ""
	if fam, err := s.db.GetFamily(c.FamilyID); err == nil {
		acceptance = fam.AcceptanceJSON
	}
	return in, acceptance, nil
}

// caseInputEmpty reports whether a CaseInput carries no gradeable context:
// no card content, no messages, and no question.
func caseInputEmpty(in schema.CaseInput) bool {
	if strings.TrimSpace(in.Question) != "" || len(in.Messages) > 0 {
		return false
	}
	c := in.Card
	return strings.TrimSpace(c.Decision+c.Context+c.Priorities+c.Unusual+c.History+c.Deadline+c.Style) == ""
}

func mean(scores map[string]int) float64 {
	if len(scores) == 0 {
		return 0
	}
	sum := 0
	for _, v := range scores {
		sum += v
	}
	return float64(sum) / float64(len(scores))
}
