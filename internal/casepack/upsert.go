package casepack

import (
	"encoding/json"
	"fmt"

	"github.com/qoke/toughdecisions/internal/hash"
	"github.com/qoke/toughdecisions/internal/store"
)

// Result reports the content hashes stored by Upsert plus non-fatal warnings.
type Result struct {
	FamilyHashes map[string]string
	CaseHashes   map[string]string
	BundleHashes map[string]string
	Warnings     []string
}

// caseInput is the stored JSON for a case (card + messages + question).
type caseInput struct {
	Card     Card      `json:"card"`
	Messages []Message `json:"messages"`
	Question string    `json:"question"`
}

// bundleViews is the stored JSON for a bundle's views.
type bundleViews struct {
	Views map[string]ViewInput `json:"views"`
}

// InputHash returns the stable content hash for a case.
func InputHash(c Case) string {
	return hash.SHA256Hex(hash.CanonicalJSON(caseInput{Card: c.Card, Messages: c.Messages, Question: c.Question}))
}

// FamilyHash returns the stable content hash for a family (acceptance +
// planted issues + tags + seats relevant).
func FamilyHash(f Family) string {
	return hash.SHA256Hex(hash.CanonicalJSON(map[string]any{
		"acceptance":     f.Acceptance,
		"planted_issues": f.PlantedIssues,
		"seats_relevant": f.SeatsRelevant,
		"tags":           f.Tags,
	}))
}

// BundleHash returns the stable content hash for a bundle.
func BundleHash(b Bundle) string {
	return hash.SHA256Hex(hash.CanonicalJSON(map[string]any{
		"case":          b.Case,
		"manipulations": b.Manipulations,
		"views":         bundleViews{Views: b.Views},
	}))
}

func mustJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// Upsert validates the pack, then upserts families, cases, bundles, and
// calibration items keyed on their natural keys. It warns when a family's
// split changed since the last load.
func Upsert(db *store.DB, p *Pack) (*Result, error) {
	if db == nil {
		return nil, fmt.Errorf("casepack: nil store")
	}
	if p == nil {
		return nil, fmt.Errorf("casepack: nil pack")
	}
	if _, err := Validate(p); err != nil {
		return nil, err
	}
	res := &Result{
		FamilyHashes: map[string]string{},
		CaseHashes:   map[string]string{},
		BundleHashes: map[string]string{},
	}
	caseIDs := map[string]string{}
	for i := range p.Families {
		f := &p.Families[i]
		if prev, err := db.GetFamilyByKey(f.Key); err == nil && prev.Split != "" && prev.Split != f.Split {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("casepack: family %q split changed from %q to %q", f.Key, prev.Split, f.Split))
		}
		fh := FamilyHash(*f)
		res.FamilyHashes[f.Key] = fh
		row, err := db.UpsertFamily(&store.Family{
			FamilyKey:         f.Key,
			Name:              f.Name,
			Split:             f.Split,
			TagsJSON:          mustJSON(f.Tags),
			AcceptanceJSON:    mustJSON(f.Acceptance),
			PlantedIssuesJSON: mustJSON(f.PlantedIssues),
			FamilyHash:        fh,
			SeatsRelevantJSON: mustJSON(f.SeatsRelevant),
		})
		if err != nil {
			return nil, fmt.Errorf("casepack: upsert family %q: %w", f.Key, err)
		}
		for j := range f.Cases {
			c := &f.Cases[j]
			ih := InputHash(*c)
			res.CaseHashes[c.Key] = ih
			var expected *string
			if c.ExpectedChange != "" {
				v := c.ExpectedChange
				expected = &v
			}
			crow, err := db.UpsertCase(&store.Case{
				CaseKey:        c.Key,
				FamilyID:       row.ID,
				Variant:        c.Variant,
				InputJSON:      mustJSON(caseInput{Card: c.Card, Messages: c.Messages, Question: c.Question}),
				InputHash:      ih,
				ExpectedChange: expected,
			})
			if err != nil {
				return nil, fmt.Errorf("casepack: upsert case %q: %w", c.Key, err)
			}
			caseIDs[c.Key] = crow.ID
		}
		for j := range f.Bundles {
			b := &f.Bundles[j]
			bh := BundleHash(*b)
			res.BundleHashes[b.Key] = bh
			kind := b.Kind
			if kind == "" {
				kind = "authored"
			}
			_, err := db.UpsertBundle(&store.Bundle{
				BundleKey:         b.Key,
				CaseID:            caseIDs[b.Case],
				Kind:              kind,
				ViewsJSON:         mustJSON(bundleViews{Views: b.Views}),
				ManipulationsJSON: mustJSON(b.Manipulations),
				BundleHash:        bh,
			})
			if err != nil {
				return nil, fmt.Errorf("casepack: upsert bundle %q: %w", b.Key, err)
			}
		}
	}
	for i := range p.Calibration {
		it := &p.Calibration[i]
		caseID := caseIDs[it.Case]
		if caseID == "" {
			if row, err := db.GetCaseByKey(it.Case); err == nil {
				caseID = row.ID
			}
		}
		if caseID == "" {
			return nil, fmt.Errorf("casepack: calibration %q: case %q not stored", it.Key, it.Case)
		}
		_, err := db.UpsertCalibrationItem(&store.CalibrationItem{
			ItemKey:         it.Key,
			CaseID:          caseID,
			Seat:            it.Seat,
			ResponseText:    it.ResponseText,
			Category:        it.Category,
			HumanScoresJSON: mustJSON(it.HumanScores),
			HumanFlagsJSON:  mustJSON(it.HumanFlags),
			Notes:           it.Notes,
		})
		if err != nil {
			return nil, fmt.Errorf("casepack: upsert calibration %q: %w", it.Key, err)
		}
	}
	return res, nil
}
