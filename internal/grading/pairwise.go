package grading

import (
	"context"
	"crypto/rand"
	"math/big"
	"strings"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// CompareResult is the agreed pairwise outcome across both selection
// graders: the final verdict plus the consequential difference.
type CompareResult struct {
	Verdict    string
	Difference string
}

// coinFlip reports whether the left response is shown as A. It uses
// crypto/rand; tests override flip for determinism.
var flip = func() (bool, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(2))
	if err != nil {
		return false, errf("coin flip: %v", err)
	}
	return n.Int64() == 0, nil
}

// Compare runs blind A/B comparison per R-09 with both selection graders.
// Each grader gets one coin flip (left_shown_as), one call, and exactly one
// reversal call when verdict is tie/unable or margin is close. Disagreement
// between the two graders resolves to a tie. The agreed row is marked
// final=true. agreed=false reports per-grader disagreement.
func (s *Service) Compare(ctx context.Context, in schema.CaseInput, acceptance, purpose, caseID, seat string, left, right *store.Response, graders []*Grader, runID string) (*CompareResult, bool, error) {
	if len(graders) != 2 {
		return nil, false, errf("compare requires exactly two selection graders, got %d", len(graders))
	}
	if left == nil || right == nil {
		return nil, false, errf("compare requires both responses")
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(caseID) == "" {
		return nil, false, errf("compare requires run_id and case_id")
	}
	finals := make([]*store.PairwiseRow, 0, 2)
	for _, g := range graders {
		final, err := s.compareOne(ctx, in, acceptance, purpose, caseID, seat, left, right, g, runID)
		if err != nil {
			return nil, false, err
		}
		finals = append(finals, final)
	}
	if finals[0].VerdictLeftRight == finals[1].VerdictLeftRight {
		return &CompareResult{Verdict: finals[0].VerdictLeftRight, Difference: finals[0].ConsequentialDifference}, true, nil
	}
	return &CompareResult{Verdict: "tie", Difference: finals[0].ConsequentialDifference}, false, nil
}

// compareOne runs one grader's comparison: coin flip, first call, optional
// single reversal, final marking.
func (s *Service) compareOne(ctx context.Context, in schema.CaseInput, acceptance, purpose, caseID, seat string, left, right *store.Response, grader *Grader, runID string) (*store.PairwiseRow, error) {
	leftAsA, err := flip()
	if err != nil {
		return nil, err
	}
	shown := "B"
	if leftAsA {
		shown = "A"
	}
	blindL, blindR := Blind(left), Blind(right)
	firstA, firstB := blindL.Rendered, blindR.Rendered
	if !leftAsA {
		firstA, firstB = blindR.Rendered, blindL.Rendered
	}
	pw, err := s.pairwiseCall(ctx, in, acceptance, blindL.Role, firstA, firstB, grader)
	if err != nil {
		return nil, err
	}
	first, err := s.db.InsertPairwise(&store.PairwiseRow{
		RunID: runID, Purpose: purpose, CaseID: caseID, Seat: seat,
		LeftResponseID: left.ID, RightResponseID: right.ID,
		GraderConfigHash: grader.ConfigHash, LeftShownAs: shown,
		Verdict: pw.Verdict, Margin: pw.Margin,
		VerdictLeftRight:        normalise(pw.Verdict, shown),
		ConsequentialDifference: pw.ConsequentialDifference,
		Final:                   !needsReversal(pw),
	})
	if err != nil {
		return nil, errf("insert pairwise: %v", err)
	}
	if !needsReversal(pw) {
		return first, nil
	}
	revShown := "A"
	if shown == "A" {
		revShown = "B"
	}
	rev, err := s.pairwiseCall(ctx, in, acceptance, blindL.Role, firstB, firstA, grader)
	if err != nil {
		return nil, err
	}
	revLR := normalise(rev.Verdict, revShown)
	finalLR := "tie"
	if first.VerdictLeftRight == revLR &&
		(first.VerdictLeftRight == "left" || first.VerdictLeftRight == "right") {
		finalLR = first.VerdictLeftRight
	}
	second, err := s.db.InsertPairwise(&store.PairwiseRow{
		RunID: runID, Purpose: purpose, CaseID: caseID, Seat: seat,
		LeftResponseID: left.ID, RightResponseID: right.ID,
		GraderConfigHash: grader.ConfigHash, LeftShownAs: revShown,
		Verdict: rev.Verdict, Margin: rev.Margin,
		VerdictLeftRight:        finalLR,
		ConsequentialDifference: rev.ConsequentialDifference,
		ReversalOfID:            &first.ID,
		Final:                   true,
	})
	if err != nil {
		return nil, errf("insert pairwise reversal: %v", err)
	}
	return second, nil
}

// pairwiseCall performs one pairwise grader call with the single R-13
// parse retry, then validates the verdict and margin.
func (s *Service) pairwiseCall(ctx context.Context, in schema.CaseInput, acceptance, role, a, b string, grader *Grader) (schema.Pairwise, error) {
	msgs, err := prompts.BuildPairwiseGrade(in, acceptance, role, a, b)
	if err != nil {
		return schema.Pairwise{}, err
	}
	content, err := s.chat(ctx, grader, msgs)
	if err != nil {
		return schema.Pairwise{}, err
	}
	if pw, ok := schema.ParseLenient[schema.Pairwise](content); ok {
		if verr := validatePairwise(pw); verr == nil {
			return pw, nil
		}
	}
	retry := append(append([]gateway.Message{}, msgs...),
		gateway.Message{Role: "user", Content: "Return only the JSON object."})
	content, err = s.chat(ctx, grader, retry)
	if err != nil {
		return schema.Pairwise{}, err
	}
	pw, ok := schema.ParseLenient[schema.Pairwise](content)
	if !ok {
		return schema.Pairwise{}, errf("grader %q returned unparseable pairwise grade after one retry", grader.GraderKey)
	}
	if err := validatePairwise(pw); err != nil {
		return schema.Pairwise{}, err
	}
	return pw, nil
}

// validatePairwise rejects verdicts/margins outside the plan vocabularies.
func validatePairwise(pw schema.Pairwise) error {
	switch pw.Verdict {
	case "A", "B", "tie", "unable":
	default:
		return errf("invalid pairwise verdict %q", pw.Verdict)
	}
	switch pw.Margin {
	case "clear", "close":
	default:
		return errf("invalid pairwise margin %q", pw.Margin)
	}
	return nil
}

// needsReversal reports whether a tie/unable verdict or a close margin
// requires exactly one reversed-order call.
func needsReversal(pw schema.Pairwise) bool {
	return pw.Verdict == "tie" || pw.Verdict == "unable" || pw.Margin == "close"
}

// normalise maps an A/B verdict back to left/right given left_shown_as.
func normalise(verdict, leftShownAs string) string {
	switch verdict {
	case "A":
		if leftShownAs == "A" {
			return "left"
		}
		return "right"
	case "B":
		if leftShownAs == "B" {
			return "left"
		}
		return "right"
	default:
		return verdict
	}
}
