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

// calibrationItem is the grading view of a calibration item.
type calibrationItem struct {
	Key         string
	Seat        string
	Text        string
	HumanScores string
	HumanFlags  string
}

// toStoreItems adapts rotated slices back to store rows for gradeItems.
// Only the fields gradeItems reads are populated.
func toStoreItems(items []calibrationItem) []*store.CalibrationItem {
	out := make([]*store.CalibrationItem, 0, len(items))
	for _, it := range items {
		out = append(out, &store.CalibrationItem{
			ItemKey:         it.Key,
			Seat:            it.Seat,
			ResponseText:    it.Text,
			HumanScoresJSON: it.HumanScores,
			HumanFlagsJSON:  it.HumanFlags,
		})
	}
	return out
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

// gradeItems grades each item with the calibration rubric (one R-13 retry
// each) and computes the reversal test per item. It parses through the same
// tolerant parseAbsoluteGrade as the Grade path, so an identical real-model
// payload parses identically in both paths.
func (s *Service) gradeItems(ctx context.Context, in schema.CaseInput, acceptance string, grader *Grader, items []*store.CalibrationItem) ([]calibrationRow, int, error) {
	rows := make([]calibrationRow, 0, len(items))
	reversals := 0
	for _, it := range items {
		msgs, err := prompts.BuildCalibrationGrade(in, acceptance, it.Seat, it.ResponseText)
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
