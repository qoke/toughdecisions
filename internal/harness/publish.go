package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/hash"
	"github.com/qoke/toughdecisions/internal/ids"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// PublishOptions configures one publish: the run and candidate, --force to
// override a failing checklist (recorded, never silent), or --baselines-only
// to fill baselines + snapshot natural bundles without activating a pack.
type PublishOptions struct {
	RunID         string
	CandidateKey  string
	Force         bool
	BaselinesOnly bool
}

// PublishResult is the outcome of one publish.
type PublishResult struct {
	Pack      *pack.Pack
	Forced    bool
	Baselines int
	Bundles   int
	// SkippedBaselines counts generated responses dropped as
	// non-gradeable (failed/substituted/timed-out/unparseable) instead of
	// becoming baselines. Always reported, never silent.
	SkippedBaselines int
}

// ChecklistBlockedError refuses a publish whose checklist fails without
// --force. It names the candidate so the operator knows what to fix.
type ChecklistBlockedError struct {
	CandidateKey string
	Rows         []ChecklistRow
}

func (e *ChecklistBlockedError) Error() string {
	return fmt.Sprintf("harness: checklist for candidate %q failed: publish refused (use --force to override)", e.CandidateKey)
}

// NoGradeableEvidenceError refuses a publish whose generated baseline
// responses contain no gradeable evidence: every generation failed, timed
// out, was substituted, or failed to parse. Publishing baselines from such
// a run would poison the promotion comparison with fake references.
type NoGradeableEvidenceError struct {
	Total   int
	Skipped int
}

func (e *NoGradeableEvidenceError) Error() string {
	return fmt.Sprintf("harness: publish refused: no gradeable baseline evidence (%d generated, %d skipped as failed/substituted/timed-out/unparseable)", e.Total, e.Skipped)
}

// Publish implements plan §12.7: refuse on a failing checklist unless
// --force (recording the override in promotion_json), build the new seats
// map, then swap the active pack AND insert every baseline + natural-bundle
// row in ONE store transaction. Baselines-only fills baselines and
// snapshots natural bundles on the active pack without activating a pack.
func (r *Runner) Publish(ctx context.Context, opts PublishOptions) (*PublishResult, error) {
	if opts.BaselinesOnly {
		pk, err := packActive(r)
		if err != nil {
			return nil, err
		}
		skipped, err := r.fillBaselinesTx(ctx, pk, pk.ID)
		if err != nil {
			return nil, err
		}
		nb, err := r.countNaturalBundles()
		if err != nil {
			return nil, err
		}
		nbl, err := r.countBaselines(pk.ID)
		if err != nil {
			return nil, err
		}
		if skipped > 0 {
			r.log.Warn("harness: publish skipped non-gradeable baselines", "skipped", skipped, "baselines", nbl)
		}
		return &PublishResult{Pack: pk, Baselines: nbl, Bundles: nb, SkippedBaselines: skipped}, nil
	}
	if opts.RunID == "" {
		return nil, fmt.Errorf("harness: publish needs a run id")
	}
	if opts.CandidateKey == "" {
		return nil, fmt.Errorf("harness: publish needs a candidate key")
	}
	row, err := r.db.GetCandidateByKey(opts.RunID, opts.CandidateKey)
	if err != nil {
		return nil, fmt.Errorf("harness: candidate %q: %w", opts.CandidateKey, err)
	}
	if row.PromotionJSON == nil {
		return nil, fmt.Errorf("harness: candidate %q has no promotion result", opts.CandidateKey)
	}
	var cl Checklist
	if err := json.Unmarshal([]byte(*row.PromotionJSON), &cl); err != nil {
		return nil, fmt.Errorf("harness: decode promotion: %w", err)
	}
	forced := false
	if !cl.PromoteRecommended && !opts.Force {
		return nil, &ChecklistBlockedError{CandidateKey: opts.CandidateKey, Rows: cl.Rows}
	}
	// The forced marker joins the PublishAtomic transaction below
	// (PackTxSeed.ForcedCandidateID/ForcedPromotionJSON) so the marker and
	// the pack swap commit atomically (M1).
	var forcedPromotionJSON *string
	if !cl.PromoteRecommended && opts.Force {
		forced = true
		cl.Forced = true
		raw, _ := json.Marshal(cl)
		s := string(raw)
		forcedPromotionJSON = &s
	}
	cur, err := packActive(r)
	if err != nil {
		return nil, err
	}
	seats, err := r.seatsForPublish(cur, row)
	if err != nil {
		return nil, err
	}
	seatsJSON, err := json.Marshal(seats)
	if err != nil {
		return nil, fmt.Errorf("harness: encode seats: %w", err)
	}
	newID := ids.NewID()
	seeds, skipped, bundles, err := r.publishSeeds(ctx, seats, newID)
	if err != nil {
		return nil, err
	}
	runID := opts.RunID
	seed := store.PackTxSeed{
		NewID:     newID,
		NewStatus: pack.StatusActive,
		NewSeats:  string(seatsJSON),
		NewHash:   prompts.PromptPackHash(),
		NewRunID:  &runID,
		Baselines: seeds,
		Bundles:   bundles,
	}
	if forcedPromotionJSON != nil {
		seed.ForcedCandidateID = row.ID
		seed.ForcedPromotionJSON = forcedPromotionJSON
	}
	res, err := r.db.PublishAtomic(seed)
	if err != nil {
		return nil, err
	}
	pk, err := pack.Show(r.db, res.CreatedID)
	if err != nil {
		return nil, err
	}
	if skipped > 0 {
		r.log.Warn("harness: publish skipped non-gradeable baselines", "skipped", skipped, "baselines", len(seeds))
	}
	return &PublishResult{Pack: pk, Forced: forced, Baselines: len(seeds), Bundles: len(bundles), SkippedBaselines: skipped}, nil
}

// seatsForPublish builds the new seats map: the active pack with the
// candidate seat replaced by the candidate config.
func (r *Runner) seatsForPublish(cur *pack.Pack, row *store.Candidate) (map[pack.Seat]pack.SeatConfig, error) {
	seats := make(map[pack.Seat]pack.SeatConfig, len(cur.Seats))
	for s, c := range cur.Seats {
		seats[s] = c
	}
	seat := pack.Seat(row.Seat)
	if !seat.Valid() {
		return nil, fmt.Errorf("harness: candidate %q has unknown seat %q", row.CandidateKey, row.Seat)
	}
	var cfg pack.SeatConfig
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		return nil, fmt.Errorf("harness: decode candidate %q config: %w", row.CandidateKey, err)
	}
	cfg.Seat = seat
	if requiresStrictSchema(r.models, cfg.Model) {
		return nil, fmt.Errorf("harness: candidate %q model %q does not support strict structured outputs: %w", row.CandidateKey, cfg.Model, gateway.ErrUnsupportedSetting)
	}
	if err := r.models.ValidateSettings(cfg); err != nil {
		return nil, fmt.Errorf("harness: candidate %q: %w", row.CandidateKey, err)
	}
	seats[seat] = cfg
	for s, c := range seats {
		if requiresStrictSchema(r.models, c.Model) {
			return nil, fmt.Errorf("harness: seat %q model %q does not support strict structured outputs: %w", string(s), c.Model, gateway.ErrUnsupportedSetting)
		}
		if err := r.models.ValidateSettings(c); err != nil {
			return nil, fmt.Errorf("harness: seat %q: %w", string(s), err)
		}
	}
	if len(seats) != len(pack.AllSeats) {
		return nil, fmt.Errorf("harness: publish needs %d seats, got %d", len(pack.AllSeats), len(seats))
	}
	return seats, nil
}

// publishSeeds ensures a cached response exists under seats for every seat
// x selection case and snapshots one natural bundle per selection case.
// The judge baseline response is keyed (pack, judge, case) with the
// baseline bundle shown. All ids are preassigned so PublishAtomic writes
// them in one transaction.
//
// Only gradeable responses (parsed OK, no error/timeout/substitution,
// non-empty text) become baselines; failures are recorded as absent, never
// silently substituted. Each skipped (seat, case) triple is counted in the
// returned skipped total and logged by the caller, and a run with no
// gradeable evidence at all is refused. Bundles still snapshot from every
// generation (even failed text, rendered as raw qualification) so the judge
// call shape stays stable.
func (r *Runner) publishSeeds(ctx context.Context, seats map[pack.Seat]pack.SeatConfig, packID string) ([]store.BaselineSeed, int, []store.BundleSeed, error) {
	families, err := r.db.ListFamilies()
	if err != nil {
		return nil, 0, nil, fmt.Errorf("harness: list families: %w", err)
	}
	var seeds []store.BaselineSeed
	var bundles []store.BundleSeed
	total, skipped := 0, 0
	for _, f := range families {
		if f.Split != "selection" {
			continue
		}
		cases, err := r.db.ListCasesByFamily(f.ID)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("harness: list cases for %q: %w", f.FamilyKey, err)
		}
		for _, c := range cases {
			in, err := CaseInputFor(c)
			if err != nil {
				return nil, 0, nil, err
			}
			inputHash := InputHashFor(in)
			views := map[string]schema.RenderedView{}
			type pendingSeed struct {
				seed store.BaselineSeed
				resp *store.Response
			}
			var pending []pendingSeed
			for _, seat := range []pack.Seat{pack.SeatPossibility, pack.SeatPerspective, pack.SeatStressTester} {
				cfg, ok := seats[seat]
				if !ok {
					return nil, 0, nil, fmt.Errorf("harness: seat %q not in publish seats", string(seat))
				}
				gen, err := r.GenerateResponse(ctx, GenRequest{
					Seat: seat, SeatCfg: cfg, Input: in,
					InputHash: inputHash, RunID: "",
				})
				if err != nil {
					return nil, 0, nil, fmt.Errorf("harness: baseline seat %q case %q: %w", string(seat), c.CaseKey, err)
				}
				total++
				v, ok := parseView(gen.Response)
				if !ok {
					v = schema.RenderedView{Role: string(seat), Qualification: gen.Response.RawText}
				}
				views[string(seat)] = v
				pending = append(pending, pendingSeed{
					seed: store.BaselineSeed{
						Key:    packID + "/" + string(seat) + "/" + c.ID,
						PackID: packID, Seat: string(seat), CaseID: c.ID,
						ResponseID: gen.Response.ID,
					},
					resp: gen.Response,
				})
			}
			bundle, err := r.naturalBundleFor(c, views, packID)
			if err != nil {
				return nil, 0, nil, err
			}
			bundle.ID = ids.NewID()
			bundles = append(bundles, *bundle)
			jcfg, ok := seats[pack.SeatJudge]
			if !ok {
				return nil, 0, nil, fmt.Errorf("harness: seat %q not in publish seats", string(pack.SeatJudge))
			}
			jgen, err := r.GenerateResponse(ctx, GenRequest{
				Seat: pack.SeatJudge, SeatCfg: jcfg, Input: in,
				InputHash: inputHash, Bundle: bundleRow(bundle), RunID: "",
			})
			if err != nil {
				return nil, 0, nil, fmt.Errorf("harness: baseline seat %q case %q: %w", string(pack.SeatJudge), c.CaseKey, err)
			}
			total++
			bid := bundle.ID
			pending = append(pending, pendingSeed{
				seed: store.BaselineSeed{
					Key:    packID + "/" + string(pack.SeatJudge) + "/" + c.ID,
					PackID: packID, Seat: string(pack.SeatJudge), CaseID: c.ID,
					BundleID: &bid, ResponseID: jgen.Response.ID,
				},
				resp: jgen.Response,
			})
			for _, p := range pending {
				if gradeableResponse(p.resp) {
					seeds = append(seeds, p.seed)
				} else {
					skipped++
				}
			}
		}
	}
	if len(seeds) == 0 && total == 0 {
		return nil, 0, nil, fmt.Errorf("harness: no selection cases for baselines")
	}
	if len(seeds) == 0 {
		return nil, 0, nil, &NoGradeableEvidenceError{Total: total, Skipped: skipped}
	}
	return seeds, skipped, bundles, nil
}

// gradeableResponse reports whether a generated baseline response is valid
// evidence: parsed OK with no gateway error, no timeout, no substitution,
// and non-empty text. Baselines point at responses rows that the promotion
// checklist later grades, so a failed/substituted/timed-out/unparseable row
// is not valid baseline evidence.
func gradeableResponse(resp *store.Response) bool {
	if resp == nil {
		return false
	}
	if resp.Substituted || resp.TimedOut || !resp.ParseOK {
		return false
	}
	if resp.Error != nil && *resp.Error != "" {
		return false
	}
	return resp.RawText != ""
}

// fillBaselinesTx generates baselines under the pack's own seats and writes
// them plus natural snapshots atomically on packID. It returns the number
// of non-gradeable generations skipped (reported, never silent).
func (r *Runner) fillBaselinesTx(ctx context.Context, pk *pack.Pack, packID string) (int, error) {
	seeds, skipped, bundles, err := r.publishSeeds(ctx, pk.Seats, packID)
	if err != nil {
		return 0, err
	}
	_, err = r.db.PublishAtomic(store.PackTxSeed{Baselines: seeds, Bundles: bundles})
	return skipped, err
}

// naturalBundleFor builds the natural-bundle seed for one case from freshly
// generated view outputs.
func (r *Runner) naturalBundleFor(c *store.Case, views map[string]schema.RenderedView, packID string) (*store.BundleSeed, error) {
	viewsJSON, err := json.Marshal(views)
	if err != nil {
		return nil, fmt.Errorf("harness: encode natural bundle: %w", err)
	}
	bh := hash.SHA256Hex(hash.CanonicalJSON(map[string]any{
		"case": c.CaseKey, "views": string(viewsJSON),
	}))
	source := packID
	return &store.BundleSeed{
		Key: c.CaseKey, BundleKey: "natural-" + c.CaseKey,
		CaseID: c.ID, ViewsJSON: string(viewsJSON),
		ManipulationsJSON: "[]", BundleHash: bh, SourcePackID: &source,
	}, nil
}

// bundleRow wraps a bundle seed as a *store.Bundle for the judge cache key.
func bundleRow(b *store.BundleSeed) *store.Bundle {
	if b == nil {
		return nil
	}
	return &store.Bundle{ID: b.ID, BundleKey: b.BundleKey, CaseID: b.CaseID, BundleHash: b.BundleHash, ViewsJSON: b.ViewsJSON}
}

func parseView(resp *store.Response) (schema.RenderedView, bool) {
	if resp == nil || resp.ParsedJSON == nil {
		return schema.RenderedView{}, false
	}
	v, ok := schema.ParseLenient[schema.View](*resp.ParsedJSON)
	if !ok {
		return schema.RenderedView{}, false
	}
	return schema.RenderedView{
		Role: resp.Seat, Qualification: v.Qualification,
		SuggestedReply: v.SuggestedReply, DecisiveInsight: v.DecisiveInsight,
		TradeoffOrObjection: v.TradeoffOrObjection, DependsOn: v.DependsOn,
		Fallback: v.Fallback,
	}, true
}

func (r *Runner) countNaturalBundles() (int, error) {
	families, err := r.db.ListFamilies()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range families {
		if f.Split != "selection" {
			continue
		}
		cases, err := r.db.ListCasesByFamily(f.ID)
		if err != nil {
			return 0, err
		}
		n += len(cases)
	}
	return n, nil
}

func (r *Runner) countBaselines(packID string) (int, error) {
	families, err := r.db.ListFamilies()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range families {
		if f.Split != "selection" {
			continue
		}
		cases, err := r.db.ListCasesByFamily(f.ID)
		if err != nil {
			return 0, err
		}
		for _, c := range cases {
			for _, seat := range pack.AllSeats {
				if _, err := r.db.GetBaseline(packID, string(seat), c.ID); err == nil {
					n++
				}
			}
		}
	}
	return n, nil
}
