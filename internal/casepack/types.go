// Package casepack loads, validates, and upserts user-authored YAML case packs.
package casepack

import (
	"errors"
	"fmt"
	"strings"
)

// Valid value sets from DEVELOPMENT_PLAN §10 and §11.
var (
	validSplits      = map[string]bool{"development": true, "selection": true}
	validTags        = map[string]bool{"hard": true, "priority": true, "hardest": true, "safety": true, "control": true, "sentinel": true}
	validSeats       = map[string]bool{"possibility": true, "perspective": true, "stress_tester": true, "judge": true}
	validVariants    = map[string]bool{"base": true, "narrator": true, "fact": true, "delay_cost": true, "alternative": true, "pushback": true, "unusual_detail": true}
	validKinds       = map[string]bool{"constraint": true, "fact": true, "motive": true, "option": true, "timing": true, "safety": true, "values": true}
	validBundleKinds = map[string]bool{"natural": true, "authored": true}
	validScoreKeys   = []string{"grounding_calibration", "context_values_fidelity", "decision_insight", "practical_robustness", "role_execution"}
	validCategories  = map[string]bool{
		"grounded_support": true, "flattering_agreement": true, "justified_challenge": true,
		"invented_objection": true, "appropriate_caution": true, "unwarranted_alarm": true,
		"planted_fabrication": true, "planted_omission": true, "infeasible": true, "defensible_disliked": true,
	}
	validFlagTypes = map[string]bool{
		"fabrication": true, "ignored_danger_or_impossibility": true, "coercive": true,
		"invented_commitment": true, "values_substitution": true, "misrepresentation": true,
	}
)

// Family is one families/*.yaml file: a set of cases sharing acceptance notes.
type Family struct {
	Key           string         `yaml:"key"`
	Name          string         `yaml:"name"`
	Split         string         `yaml:"split"`
	Tags          []string       `yaml:"tags"`
	SeatsRelevant []string       `yaml:"seats_relevant"`
	Acceptance    Acceptance     `yaml:"acceptance"`
	PlantedIssues []PlantedIssue `yaml:"planted_issues"`
	Cases         []Case         `yaml:"cases"`
	Bundles       []Bundle       `yaml:"bundles"`
}

// Acceptance holds the author-written constraints for a family.
type Acceptance struct {
	MustNotice          []string `yaml:"must_notice"`
	CannotAssume        []string `yaml:"cannot_assume"`
	MaterialErrors      []string `yaml:"material_errors"`
	VariantExpectations string   `yaml:"variant_expectations"`
}

// PlantedIssue is a checkable fact planted in a family.
type PlantedIssue struct {
	ID   string `yaml:"id"`
	Text string `yaml:"text"`
	Kind string `yaml:"kind"`
}

// Card is the 7-field case card.
type Card struct {
	Decision   string `yaml:"decision" json:"decision"`
	Context    string `yaml:"context" json:"context"`
	Priorities string `yaml:"priorities" json:"priorities"`
	Unusual    string `yaml:"unusual" json:"unusual"`
	History    string `yaml:"history" json:"history"`
	Deadline   string `yaml:"deadline" json:"deadline"`
	Style      string `yaml:"style" json:"style"`
}

// Message is one verbatim message in a case.
type Message struct {
	Sender string `yaml:"sender" json:"sender"`
	Text   string `yaml:"text" json:"text"`
	TS     string `yaml:"ts,omitempty" json:"ts,omitempty"`
}

// Case is one council input within a family.
type Case struct {
	Key            string    `yaml:"key"`
	Variant        string    `yaml:"variant"`
	ExpectedChange string    `yaml:"expected_change,omitempty"`
	Card           Card      `yaml:"card"`
	Messages       []Message `yaml:"messages"`
	Question       string    `yaml:"question"`
}

// ViewInput is one rendered view inside an authored bundle.
type ViewInput struct {
	Qualification       string `yaml:"qualification" json:"qualification"`
	SuggestedReply      string `yaml:"suggested_reply" json:"suggested_reply"`
	DecisiveInsight     string `yaml:"decisive_insight" json:"decisive_insight"`
	TradeoffOrObjection string `yaml:"tradeoff_or_objection" json:"tradeoff_or_objection"`
	DependsOn           string `yaml:"depends_on" json:"depends_on"`
	Fallback            string `yaml:"fallback" json:"fallback"`
}

// Bundle pins views for judge-seat testing.
type Bundle struct {
	Key           string               `yaml:"key"`
	Kind          string               `yaml:"kind"`
	Case          string               `yaml:"case"`
	Manipulations []string             `yaml:"manipulations"`
	Views         map[string]ViewInput `yaml:"views"`
}

// CalibrationItem is one human-scored reference answer.
type CalibrationItem struct {
	Key          string         `yaml:"key"`
	Case         string         `yaml:"case"`
	Seat         string         `yaml:"seat"`
	ResponseText string         `yaml:"response_text"`
	Category     string         `yaml:"category"`
	HumanScores  map[string]int `yaml:"human_scores"`
	HumanFlags   []string       `yaml:"human_flags"`
	Notes        string         `yaml:"notes,omitempty"`
}

// Pack is a loaded case pack: families plus calibration items.
type Pack struct {
	Families    []Family
	Calibration []CalibrationItem
}

// Summary reports counts from Validate.
type Summary struct {
	Families          int
	Cases             int
	Bundles           int
	SelectionFamilies int
	CalibrationItems  int
}

// Validate checks a pack and returns its summary. An error joining every
// problem is returned when the pack is invalid.
func Validate(p *Pack) (Summary, error) {
	if p == nil {
		return Summary{}, errors.New("casepack: nil pack")
	}
	var errs []error
	var s Summary
	s.Families = len(p.Families)
	s.CalibrationItems = len(p.Calibration)

	caseKeys := map[string]string{}
	caseFamily := map[string]string{}
	familyKeys := map[string]bool{}
	hasSafety, hasSelection := false, false
	hasControl := false

	for i := range p.Families {
		f := &p.Families[i]
		if f.Key == "" {
			errs = append(errs, fmt.Errorf("casepack: family %d: empty key", i))
		} else if familyKeys[f.Key] {
			errs = append(errs, fmt.Errorf("casepack: duplicate family key %q", f.Key))
		} else {
			familyKeys[f.Key] = true
		}
		if !validSplits[f.Split] {
			errs = append(errs, fmt.Errorf("casepack: family %q: unknown split %q", f.Key, f.Split))
		}
		if f.Split == "selection" {
			hasSelection = true
			s.SelectionFamilies++
		}
		for _, t := range f.Tags {
			if t == "safety" {
				hasSafety = true
			}
			if t == "control" {
				hasControl = true
			}
			if !validTags[t] {
				errs = append(errs, fmt.Errorf("casepack: family %q: unknown tag %q", f.Key, t))
			}
		}
		for _, seat := range f.SeatsRelevant {
			if !validSeats[seat] {
				errs = append(errs, fmt.Errorf("casepack: family %q: unknown seat %q", f.Key, seat))
			}
		}
		if len(f.Acceptance.MustNotice) == 0 {
			errs = append(errs, fmt.Errorf("casepack: family %q: acceptance.must_notice is empty", f.Key))
		}
		if strings.TrimSpace(f.Acceptance.VariantExpectations) == "" {
			errs = append(errs, fmt.Errorf("casepack: family %q: acceptance.variant_expectations is empty", f.Key))
		}
		for _, pi := range f.PlantedIssues {
			if strings.TrimSpace(pi.ID) == "" || strings.TrimSpace(pi.Text) == "" {
				errs = append(errs, fmt.Errorf("casepack: family %q: planted issue with empty id or text", f.Key))
			}
			if !validKinds[pi.Kind] {
				errs = append(errs, fmt.Errorf("casepack: family %q: planted issue %q: unknown kind %q", f.Key, pi.ID, pi.Kind))
			}
		}
		hasBase := false
		for j := range f.Cases {
			c := &f.Cases[j]
			s.Cases++
			if c.Key == "" {
				errs = append(errs, fmt.Errorf("casepack: family %q: case %d: empty key", f.Key, j))
				continue
			}
			if prev, dup := caseKeys[c.Key]; dup {
				errs = append(errs, fmt.Errorf("casepack: duplicate case key %q (in %q and %q)", c.Key, prev, f.Key))
			} else {
				caseKeys[c.Key] = f.Key
				caseFamily[c.Key] = f.Key
			}
			if c.Variant == "" {
				c.Variant = "base"
			}
			if !validVariants[c.Variant] {
				errs = append(errs, fmt.Errorf("casepack: case %q: unknown variant %q", c.Key, c.Variant))
			}
			if c.Variant == "base" {
				hasBase = true
			}
			if c.Variant != "base" && strings.TrimSpace(c.ExpectedChange) == "" {
				errs = append(errs, fmt.Errorf("casepack: case %q: non-base variant requires expected_change", c.Key))
			}
			hasMsg := false
			for _, m := range c.Messages {
				if strings.TrimSpace(m.Text) != "" {
					hasMsg = true
				}
				if m.Sender != "me" && m.Sender != "them" {
					errs = append(errs, fmt.Errorf("casepack: case %q: unknown sender %q", c.Key, m.Sender))
				}
			}
			if !hasMsg && strings.TrimSpace(c.Question) == "" {
				errs = append(errs, fmt.Errorf("casepack: case %q: needs at least one message or a question", c.Key))
			}
		}
		// Non-base variants must reference a base case in the same family.
		if !hasBase {
			for j := range f.Cases {
				if f.Cases[j].Variant != "base" {
					errs = append(errs, fmt.Errorf("casepack: case %q: variant %q has no base case in family %q", f.Cases[j].Key, f.Cases[j].Variant, f.Key))
				}
			}
		}
		bundleKeys := map[string]bool{}
		for j := range f.Bundles {
			b := &f.Bundles[j]
			s.Bundles++
			if b.Key == "" {
				errs = append(errs, fmt.Errorf("casepack: family %q: bundle %d: empty key", f.Key, j))
				continue
			}
			if bundleKeys[b.Key] {
				errs = append(errs, fmt.Errorf("casepack: duplicate bundle key %q", b.Key))
			}
			bundleKeys[b.Key] = true
			if !validBundleKinds[b.Kind] {
				errs = append(errs, fmt.Errorf("casepack: bundle %q: unknown kind %q", b.Key, b.Kind))
			}
			owner, ok := caseFamily[b.Case]
			if !ok {
				errs = append(errs, fmt.Errorf("casepack: bundle %q: unknown case %q", b.Key, b.Case))
			} else if owner != f.Key {
				errs = append(errs, fmt.Errorf("casepack: bundle %q: case %q is outside family %q", b.Key, b.Case, f.Key))
			}
			for seat := range b.Views {
				if !validSeats[seat] {
					errs = append(errs, fmt.Errorf("casepack: bundle %q: unknown view seat %q", b.Key, seat))
				}
			}
		}
	}
	if !hasSafety {
		errs = append(errs, errors.New("casepack: need at least one family tagged safety"))
	}
	if !hasControl {
		errs = append(errs, errors.New("casepack: need at least one family tagged control"))
	}
	if !hasSelection {
		errs = append(errs, errors.New("casepack: need at least one family with split selection"))
	}
	calKeys := map[string]bool{}
	for i := range p.Calibration {
		it := &p.Calibration[i]
		if it.Key == "" {
			errs = append(errs, fmt.Errorf("casepack: calibration item %d: empty key", i))
			continue
		}
		if calKeys[it.Key] {
			errs = append(errs, fmt.Errorf("casepack: duplicate calibration key %q", it.Key))
		}
		calKeys[it.Key] = true
		if _, ok := caseFamily[it.Case]; !ok {
			errs = append(errs, fmt.Errorf("casepack: calibration %q: unknown case %q", it.Key, it.Case))
		}
		if !validSeats[it.Seat] {
			errs = append(errs, fmt.Errorf("casepack: calibration %q: unknown seat %q", it.Key, it.Seat))
		}
		if !validCategories[it.Category] {
			errs = append(errs, fmt.Errorf("casepack: calibration %q: unknown category %q", it.Key, it.Category))
		}
		if strings.TrimSpace(it.ResponseText) == "" {
			errs = append(errs, fmt.Errorf("casepack: calibration %q: empty response_text", it.Key))
		}
		for _, k := range validScoreKeys {
			v, ok := it.HumanScores[k]
			if !ok {
				errs = append(errs, fmt.Errorf("casepack: calibration %q: missing score %q", it.Key, k))
				continue
			}
			if v < 0 || v > 4 {
				errs = append(errs, fmt.Errorf("casepack: calibration %q: score %q out of range 0-4", it.Key, k))
			}
		}
		for sk := range it.HumanScores {
			known := false
			for _, k := range validScoreKeys {
				if sk == k {
					known = true
				}
			}
			if !known {
				errs = append(errs, fmt.Errorf("casepack: calibration %q: unknown score key %q", it.Key, sk))
			}
		}
		for _, fl := range it.HumanFlags {
			if !validFlagTypes[fl] {
				errs = append(errs, fmt.Errorf("casepack: calibration %q: unknown flag %q", it.Key, fl))
			}
		}
	}
	if len(errs) > 0 {
		return Summary{}, errors.Join(errs...)
	}
	return s, nil
}
