package harness

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

// Checklist statuses: the six automated rules use pass|fail; the three
// informational rows use info and never affect the verdict.
const (
	CheckPass = "pass"
	CheckFail = "fail"
	CheckInfo = "info"
)

// ChecklistRow is one promotion checklist row with the evidence (numbers)
// that decided it.
type ChecklistRow struct {
	Name     string
	Status   string
	Evidence string
}

// Checklist is the promotion evaluation for one candidate.
// PromoteRecommended is true iff all six automated rules pass.
type Checklist struct {
	CandidateKey       string
	Rows               []ChecklistRow
	PromoteRecommended bool
}

// ChecklistInput carries the per-run evidence for the checklist. Every
// field comes from this run's compare/downstream results and this run's
// flags — never cross-run history.
type ChecklistInput struct {
	OpenFlags      int
	ConfirmedFlags int
	RoleMeans      []float64
	RoleMins       []int
	RoleMinMean    float64
	RoleMinFloor   int
	HardWins       int
	HardLosses     int
	P50Cand        int64
	P50Inc         int64
	SpeedRatio     float64
	PriorityLosses int
	DownstreamNet  int
	P95Cand        int64
	SeatBudgetMs   int64
	Timeouts       int
	MeanCostCand   float64
	MeanCostInc    float64
	Fragile        bool
	UniqueNoticed  []string
	Preserved      int
}

// EvaluateChecklist applies the plan §12.6 promotion rules: the six
// automated rules decide the verdict, the three informational rows
// (cost, fragility, issue coverage) never block.
func EvaluateChecklist(candidateKey string, in ChecklistInput) Checklist {
	rows := make([]ChecklistRow, 0, 9)
	allPass := true
	automated := func(name string, pass bool, evidence string) {
		status := CheckPass
		if !pass {
			status = CheckFail
			allPass = false
		}
		rows = append(rows, ChecklistRow{Name: name, Status: status, Evidence: evidence})
	}

	// 1. "No unresolved material flag / confirmed critical failure":
	// zero flags on candidate responses with status open or confirmed.
	open, confirmed := in.OpenFlags, in.ConfirmedFlags
	automated("no unresolved material flag",
		open == 0 && confirmed == 0,
		fmt.Sprintf("open=%d confirmed=%d (this run only)", open, confirmed))

	// 2. "Executes role reliably": role_execution mean >= promo_role_min_mean
	// and min >= promo_role_min_floor (both graders).
	rolePass := len(in.RoleMeans) == 2 && len(in.RoleMins) == 2
	if rolePass {
		for gi := range in.RoleMeans {
			if in.RoleMeans[gi] < in.RoleMinMean || in.RoleMins[gi] < in.RoleMinFloor {
				rolePass = false
				break
			}
		}
	}
	automated("executes role reliably", rolePass,
		fmt.Sprintf("role_execution means=%v mins=%v thresholds mean>=%.2f min>=%d (both graders)",
			in.RoleMeans, in.RoleMins, in.RoleMinMean, in.RoleMinFloor))

	// 3. "Improves hard cases, or same quality with better speed": net
	// agreed pairwise wins on hard cases > 0; OR net >= 0 and p50 <=
	// promo_speed_ratio x incumbent.
	netHard := in.HardWins - in.HardLosses
	speedOK := float64(in.P50Cand) <= in.SpeedRatio*float64(in.P50Inc)
	automated("improves hard cases or same quality with better speed",
		netHard > 0 || (netHard >= 0 && speedOK),
		fmt.Sprintf("agreed hard net=%d (wins=%d losses=%d) p50cand=%dms p50inc=%dms ratio=%.2f",
			netHard, in.HardWins, in.HardLosses, in.P50Cand, in.P50Inc, in.SpeedRatio))

	// 4. "No unacceptable regression on priority cases": no priority case
	// with an agreed loss.
	automated("no unacceptable regression on priority cases",
		in.PriorityLosses == 0,
		fmt.Sprintf("agreed priority losses=%d", in.PriorityLosses))

	// 5. "Preserves or improves final recommendation": downstream agreed
	// net >= 0.
	automated("preserves or improves final recommendation",
		in.DownstreamNet >= 0,
		fmt.Sprintf("downstream agreed net=%d", in.DownstreamNet))

	// 6. "Reliably fits the deadline": p95 candidate latency <= seat
	// budget; zero timeouts in compare.
	automated("reliably fits the deadline",
		in.P95Cand <= in.SeatBudgetMs && in.Timeouts == 0,
		fmt.Sprintf("p95cand=%dms budget=%dms timeouts=%d (this run only)",
			in.P95Cand, in.SeatBudgetMs, in.Timeouts))

	// Informational rows: evidence only, never block.
	rows = append(rows,
		ChecklistRow{Name: "cost tie-break", Status: CheckInfo,
			Evidence: fmt.Sprintf("mean cost/case candidate=%.6f incumbent=%.6f", in.MeanCostCand, in.MeanCostInc)},
		ChecklistRow{Name: "fragility", Status: CheckInfo,
			Evidence: fmt.Sprintf("fragile=%v", in.Fragile)},
		ChecklistRow{Name: "issue coverage", Status: CheckInfo,
			Evidence: fmt.Sprintf("unique_noticed=%d preserved=%d issues=[%s]",
				len(in.UniqueNoticed), in.Preserved, strings.Join(in.UniqueNoticed, ","))},
	)
	return Checklist{CandidateKey: candidateKey, Rows: rows, PromoteRecommended: allPass}
}

// Promotion evaluates the checklist for one candidate in a run, reading the
// stored compare/downstream results and this run's flags and coverage.
// It stores promotion_json on the candidate row.
func (r *Runner) Promotion(runID, candidateKey string) (*Checklist, error) {
	row, err := r.db.GetCandidateByKey(runID, candidateKey)
	if err != nil {
		return nil, fmt.Errorf("harness: candidate %q: %w", candidateKey, err)
	}
	if row.CompareResultJSON == nil {
		return nil, fmt.Errorf("harness: candidate %q has no compare result", candidateKey)
	}
	if row.DownstreamResultJSON == nil {
		return nil, fmt.Errorf("harness: candidate %q has no downstream result", candidateKey)
	}
	var cmp CompareResult
	if err := json.Unmarshal([]byte(*row.CompareResultJSON), &cmp); err != nil {
		return nil, fmt.Errorf("harness: decode compare result: %w", err)
	}
	var ds DownstreamResult
	if err := json.Unmarshal([]byte(*row.DownstreamResultJSON), &ds); err != nil {
		return nil, fmt.Errorf("harness: decode downstream result: %w", err)
	}
	budget := r.DeadlineFor(pack.Seat(row.Seat)).Milliseconds()
	in := ChecklistInput{
		OpenFlags: cmp.OpenFlags, ConfirmedFlags: cmp.ConfirmedFlags,
		RoleMeans: cmp.RoleMeans, RoleMins: cmp.RoleMins,
		RoleMinMean: r.cfg.PromoRoleMinMean(), RoleMinFloor: r.cfg.PromoRoleMinFloor(),
		HardWins: cmp.HardWins, HardLosses: cmp.HardLosses,
		P50Cand: cmp.P50Cand, P50Inc: cmp.P50Inc, SpeedRatio: r.cfg.PromoSpeedRatio(),
		PriorityLosses: cmp.PriorityLosses, DownstreamNet: ds.AgreedNet,
		P95Cand: cmp.P95Cand, SeatBudgetMs: budget, Timeouts: cmp.Timeouts,
		MeanCostCand: cmp.MeanCostCand, MeanCostInc: cmp.MeanCostInc,
		Fragile: cmp.Fragile,
	}
	unique, preserved := r.coverageInfo(runID, row.Seat)
	in.UniqueNoticed, in.Preserved = unique, preserved
	cl := EvaluateChecklist(candidateKey, in)
	raw, _ := json.Marshal(cl)
	s := string(raw)
	if err := r.db.UpdateCandidateResults(row.ID, &store.CandidateResults{PromotionJSON: &s}); err != nil {
		return nil, fmt.Errorf("harness: store promotion: %w", err)
	}
	return &cl, nil
}

// coverageInfo reports the informational issue-coverage row for this run:
// planted issues the candidate's seat noticed that no other view seat
// noticed, and how many the judge preserved. It reads only this run's
// issue_coverage rows.
func (r *Runner) coverageInfo(runID, seat string) ([]string, int) {
	rows, err := r.db.ListCoverageByRun(runID)
	if err != nil {
		return nil, 0
	}
	byIssue := map[string][]*store.CoverageRow{}
	for _, row := range rows {
		if !row.IsPlanted {
			continue
		}
		byIssue[row.IssueID] = append(byIssue[row.IssueID], row)
	}
	var unique []string
	preserved := 0
	viewSeats := []string{"possibility", "perspective", "stress_tester"}
	for issueID, covs := range byIssue {
		noticedBySeat := false
		noticedByOther := false
		for _, cov := range covs {
			for _, n := range decodeStrings(cov.NoticedByJSON) {
				if n == seat {
					noticedBySeat = true
				}
				for _, s := range viewSeats {
					if s != seat && n == s {
						noticedByOther = true
					}
				}
			}
			if cov.JudgeOutcome == "preserved" && noticedBySeat {
				preserved++
			}
		}
		if noticedBySeat && !noticedByOther {
			unique = append(unique, issueID)
		}
	}
	return unique, preserved
}

func decodeStrings(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
