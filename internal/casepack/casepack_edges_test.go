package casepack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateEnumEdges(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Pack)
		wantErr string
	}{
		{"dup family", func(p *Pack) { p.Families = append(p.Families, p.Families[0]) }, "duplicate family"},
		{"bad split", func(p *Pack) { p.Families[0].Split = "test" }, "unknown split"},
		{"bad tag", func(p *Pack) { p.Families[0].Tags = append(p.Families[0].Tags, "nope") }, "unknown tag"},
		{"bad seat", func(p *Pack) { p.Families[0].SeatsRelevant = []string{"nope"} }, "unknown seat"},
		{"bad variant", func(p *Pack) { p.Families[0].Cases[0].Variant = "nope" }, "unknown variant"},
		{"bad sender", func(p *Pack) {
			c := p.Families[0].Cases[0]
			c.Messages = []Message{{Sender: "bot", Text: "x"}}
			p.Families[0].Cases[0] = c
		}, "unknown sender"},
		{"missing expected change", func(p *Pack) { p.Families[0].Cases[1].ExpectedChange = "" }, "expected_change"},
		{"bundle unknown case", func(p *Pack) { p.Families[0].Bundles[0].Case = "ZZZ" }, "unknown case"},
		{"bundle bad kind", func(p *Pack) { p.Families[0].Bundles[0].Kind = "weird" }, "unknown kind"},
		{"bundle bad seat", func(p *Pack) {
			p.Families[0].Bundles[0].Views = map[string]ViewInput{"bot": {}}
		}, "unknown view seat"},
		{"dup bundle", func(p *Pack) {
			p.Families[0].Bundles = append(p.Families[0].Bundles, p.Families[0].Bundles[0])
		}, "duplicate bundle"},
		{"dup calib", func(p *Pack) { p.Calibration = append(p.Calibration, p.Calibration[0]) }, "duplicate calibration"},
		{"calib bad case", func(p *Pack) { p.Calibration[0].Case = "ZZZ" }, "unknown case"},
		{"calib bad seat", func(p *Pack) { p.Calibration[0].Seat = "bot" }, "unknown seat"},
		{"calib bad category", func(p *Pack) { p.Calibration[0].Category = "meh" }, "unknown category"},
		{"calib bad score key", func(p *Pack) {
			p.Calibration[0].HumanScores = map[string]int{"bogus": 1}
		}, "score"},
		{"calib score range", func(p *Pack) { p.Calibration[0].HumanScores["grounding_and_calibration"] = 9 }, "out of range"},
		{"calib bad flag", func(p *Pack) { p.Calibration[0].HumanFlags = []string{"bogus"} }, "unknown flag"},
		{"empty planted", func(p *Pack) {
			p.Families[0].PlantedIssues = []PlantedIssue{{ID: "", Text: "", Kind: "fact"}}
		}, "planted issue"},
		{"bad kind", func(p *Pack) {
			p.Families[0].PlantedIssues = []PlantedIssue{{ID: "x", Text: "y", Kind: "bogus"}}
		}, "unknown kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validPack(t)
			tc.mutate(p)
			if _, err := Validate(p); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
	}
	if _, err := Validate(nil); err == nil {
		t.Fatal("expected nil pack error")
	}
	if _, err := Upsert(nil, validPack(t)); err == nil {
		t.Fatal("expected nil store error")
	}
	if _, err := Upsert(openTestDB(t), nil); err == nil {
		t.Fatal("expected nil pack error")
	}
}

func TestParseCalibrationFlatList(t *testing.T) {
	items, err := ParseCalibration([]byte("- key: K1\n  case: C\n  seat: judge\n  response_text: r\n  category: infeasible\n  human_scores: {grounding_and_calibration: 1, context_and_values_fidelity: 1, decision_insight: 1, practical_robustness: 1, role_execution: 1}\n"))
	if err != nil {
		t.Fatalf("flat parse: %v", err)
	}
	if len(items) != 1 || items[0].Key != "K1" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if _, err := ParseCalibration([]byte("key: [unclosed")); err == nil {
		t.Fatal("expected calibration parse error")
	}
}

func TestLoadDirEmptyFamilies(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "families"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected empty families error")
	}
	if _, err := LoadDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing dir error")
	}
}
