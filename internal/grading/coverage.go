package grading

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// Issue describes one consequential issue for coverage grading.
type Issue struct {
	ID        string
	Text      string
	IsPlanted bool
}

// Cover writes issue_coverage rows per R-10 using grader A (the first
// selection grader): one row per issue recording planted vs noticed vs
// judge outcome. Views maps role name to blind rendered text; judge is the
// blind rendered judge output. The grader sees role names only.
func (s *Service) Cover(ctx context.Context, in schema.CaseInput, runID, caseID, councilLabel string, planted []Issue, views map[string]string, judge string, grader *Grader) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(caseID) == "" {
		return errf("cover requires run_id and case_id")
	}
	if grader == nil {
		return errf("cover requires a grader")
	}
	if len(planted) == 0 {
		return errf("cover requires at least one issue")
	}
	var issues strings.Builder
	for i, is := range planted {
		if i > 0 {
			issues.WriteString("\n")
		}
		issues.WriteString(is.ID + ": " + is.Text)
	}
	msgs, err := prompts.BuildCoverageGrade(in, issues.String(), views, judge)
	if err != nil {
		return err
	}
	content, err := s.chat(ctx, grader, msgs)
	if err != nil {
		return err
	}
	cov, ok := schema.ParseLenient[schema.Coverage](content)
	if !ok {
		retry := append(append([]gateway.Message{}, msgs...),
			gateway.Message{Role: "user", Content: "Return only the JSON object."})
		content, err = s.chat(ctx, grader, retry)
		if err != nil {
			return err
		}
		var ok2 bool
		cov, ok2 = schema.ParseLenient[schema.Coverage](content)
		if !ok2 {
			return errf("grader %q returned unparseable coverage after one retry", grader.GraderKey)
		}
	}
	byID := map[string]struct {
		IssueID       string   `json:"issue_id"`
		IssueText     string   `json:"issue_text"`
		IsPlanted     bool     `json:"is_planted"`
		NoticedBy     []string `json:"noticed_by"`
		UnsupportedBy []string `json:"unsupported_by"`
		JudgeOutcome  string   `json:"judge_outcome"`
	}{}
	for _, r := range cov.Rows {
		byID[r.IssueID] = r
	}
	for _, is := range planted {
		row := byID[is.ID]
		noticed, err := json.Marshal(orEmpty(row.NoticedBy))
		if err != nil {
			return errf("marshal noticed_by: %v", err)
		}
		unsupported, err := json.Marshal(orEmpty(row.UnsupportedBy))
		if err != nil {
			return errf("marshal unsupported_by: %v", err)
		}
		outcome := row.JudgeOutcome
		if strings.TrimSpace(outcome) == "" {
			outcome = "na"
		}
		if _, err := s.db.InsertCoverage(&store.CoverageRow{
			RunID: runID, CaseID: caseID, CouncilLabel: councilLabel,
			IssueID: is.ID, IssueText: is.Text, IsPlanted: is.IsPlanted,
			NoticedByJSON: string(noticed), UnsupportedByJSON: string(unsupported),
			JudgeOutcome: outcome,
		}); err != nil {
			return errf("insert coverage: %v", err)
		}
	}
	return nil
}

func orEmpty(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}
