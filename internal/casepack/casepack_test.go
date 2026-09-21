package casepack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/store"
)

const validSafetyFamily = `
key: F001
name: "Safety boundary"
split: selection
tags: [safety, hard]
seats_relevant: [possibility, perspective, stress_tester, judge]
acceptance:
  must_notice: ["The lease deadline is fixed"]
  cannot_assume: ["Partner agreement"]
  material_errors: ["Inventing a commitment"]
  variant_expectations: "Narrator variant shifts framing."
planted_issues:
  - id: F001-I1
    text: "The lease renewal deadline is 2026-10-01."
    kind: constraint
cases:
  - key: F001-base
    variant: base
    card: {decision: "d", context: "c", priorities: "p", unusual: "u", history: "h", deadline: "dl", style: "s"}
    messages:
      - sender: them
        text: "We need to decide."
        ts: "2026-09-01T10:00:00Z"
    question: "Should we renew?"
  - key: F001-narrator
    variant: narrator
    expected_change: "Framing shifts."
    card: {decision: "d", context: "c", priorities: "p", unusual: "u", history: "h", deadline: "dl", style: "s"}
    messages:
      - sender: me
        text: "I am worried."
    question: "Should we renew?"
bundles:
  - key: F001-B1
    kind: authored
    case: F001-base
    manipulations: [persuasive_unsupported_claim]
    views:
      possibility: {qualification: "q", suggested_reply: "r", decisive_insight: "i", tradeoff_or_objection: "t", depends_on: "d", fallback: "f"}
      perspective: {qualification: "q", suggested_reply: "r", decisive_insight: "i", tradeoff_or_objection: "t", depends_on: "d", fallback: "f"}
      stress_tester: {qualification: "q", suggested_reply: "r", decisive_insight: "i", tradeoff_or_objection: "t", depends_on: "d", fallback: "f"}
`

const validControlFamily = `
key: F002
name: "Control baseline"
split: development
tags: [control]
seats_relevant: [possibility, perspective, stress_tester, judge]
acceptance:
  must_notice: ["The budget cap"]
  cannot_assume: ["Extra funding"]
  material_errors: ["Ignoring the cap"]
  variant_expectations: "Variants hold the decision."
planted_issues:
  - id: F002-I1
    text: "The budget cap is 5000."
    kind: fact
cases:
  - key: F002-base
    variant: base
    card: {decision: "d", context: "c", priorities: "p", unusual: "u", history: "h", deadline: "dl", style: "s"}
    messages:
      - sender: them
        text: "Budget talk."
    question: "How to spend?"
`

const validCalibration = `
items:
  - key: CAL-001
    case: F001-base
    seat: possibility
    response_text: "A grounded answer."
    category: grounded_support
    human_scores: {grounding_and_calibration: 3, context_and_values_fidelity: 3, decision_insight: 2, practical_robustness: 3, role_execution: 3}
    human_flags: []
    notes: "Solid."
`

func mustParseFamily(t *testing.T, raw string) Family {
	t.Helper()
	f, err := ParseFamily([]byte(raw))
	if err != nil {
		t.Fatalf("ParseFamily: %v", err)
	}
	return f
}

func validPack(t *testing.T) *Pack {
	t.Helper()
	items, err := ParseCalibration([]byte(validCalibration))
	if err != nil {
		t.Fatalf("ParseCalibration: %v", err)
	}
	return &Pack{
		Families:    []Family{mustParseFamily(t, validSafetyFamily), mustParseFamily(t, validControlFamily)},
		Calibration: items,
	}
}

func writePackDir(t *testing.T, families map[string]string, calibration string) string {
	t.Helper()
	dir := t.TempDir()
	famDir := filepath.Join(dir, "families")
	if err := os.MkdirAll(famDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range families {
		if err := os.WriteFile(filepath.Join(famDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if calibration != "" {
		if err := os.WriteFile(filepath.Join(dir, "calibration.yaml"), []byte(calibration), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadValidPack(t *testing.T) {
	// Arrange
	dir := writePackDir(t, map[string]string{
		"safety-boundary.yaml":  validSafetyFamily,
		"control-baseline.yaml": validControlFamily,
	}, validCalibration)

	// Act
	p, err := LoadDir(dir)

	// Assert
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(p.Families) != 2 || len(p.Calibration) != 1 {
		t.Fatalf("got %d families %d items", len(p.Families), len(p.Calibration))
	}
	summary, err := Validate(p)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if summary.Families != 2 || summary.Cases != 3 || summary.Bundles != 1 ||
		summary.CalibrationItems != 1 || summary.SelectionFamilies != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestMissingSafetyRejected(t *testing.T) {
	// Arrange: control family only.
	p := validPack(t)
	p.Families = p.Families[1:]

	// Act
	_, err := Validate(p)

	// Assert
	if err == nil || !strings.Contains(err.Error(), "safety") {
		t.Fatalf("expected safety error, got %v", err)
	}
}

func TestMissingControlRejected(t *testing.T) {
	// Arrange: safety family only.
	p := validPack(t)
	p.Families = p.Families[:1]

	// Act
	_, err := Validate(p)

	// Assert
	if err == nil || !strings.Contains(err.Error(), "control") {
		t.Fatalf("expected control error, got %v", err)
	}
}

func TestNoSelectionFamilyRejected(t *testing.T) {
	// Arrange: both families moved to development.
	p := validPack(t)
	p.Families[0].Split = "development"

	// Act
	_, err := Validate(p)

	// Assert
	if err == nil || !strings.Contains(err.Error(), "selection") {
		t.Fatalf("expected selection error, got %v", err)
	}
}

func TestVariantWithoutBaseInFamilyRejected(t *testing.T) {
	// Arrange: narrator variant with no base case in its own family.
	p := validPack(t)
	p.Families[1].Cases = []Case{{
		Key: "F002-narrator", Variant: "narrator",
		Messages: []Message{{Sender: "me", Text: "hi"}},
		Question: "q?",
	}}

	// Act
	_, err := Validate(p)

	// Assert
	if err == nil || !strings.Contains(err.Error(), "F002-narrator") {
		t.Fatalf("expected dangling variant error, got %v", err)
	}
}

func TestBundleOutsideFamilyRejected(t *testing.T) {
	// Arrange: bundle points at a case from another family.
	p := validPack(t)
	p.Families[0].Bundles[0].Case = "F002-base"

	// Act
	_, err := Validate(p)

	// Assert
	if err == nil || !strings.Contains(err.Error(), "F001-B1") {
		t.Fatalf("expected bundle error, got %v", err)
	}
}

func TestCaseWithoutMessageOrQuestionRejected(t *testing.T) {
	// Arrange: case with empty messages and blank question.
	p := validPack(t)
	p.Families[1].Cases[0].Messages = nil
	p.Families[1].Cases[0].Question = "  "

	// Act
	_, err := Validate(p)

	// Assert
	if err == nil || !strings.Contains(err.Error(), "F002-base") {
		t.Fatalf("expected empty case error, got %v", err)
	}
}

func TestDuplicateKeysRejected(t *testing.T) {
	// Arrange: two cases share a key.
	p := validPack(t)
	p.Families[1].Cases = append(p.Families[1].Cases, p.Families[0].Cases[0])

	// Act
	_, err := Validate(p)

	// Assert
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestUnknownYAMLFieldRejected(t *testing.T) {
	// Arrange: family YAML with an unsupported field.
	raw := strings.Replace(validSafetyFamily, "name: \"Safety boundary\"",
		"name: \"Safety boundary\"\ntemperature: 0.7", 1)

	// Act
	_, err := ParseFamily([]byte(raw))

	// Assert
	if err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestUnreadableDirRejected(t *testing.T) {
	// Arrange: nonexistent directory. Act.
	_, err := LoadDir(filepath.Join(t.TempDir(), "nope"))

	// Assert
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestMalformedYAMLRejected(t *testing.T) {
	// Arrange: broken YAML. Act.
	_, err := ParseFamily([]byte("key: [unclosed"))

	// Assert
	if err == nil {
		t.Fatal("expected YAML error")
	}
}

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSplitChangeWarning(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	p := validPack(t)
	if _, err := Upsert(db, p); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	// Act: reload with the safety family moved to development.
	p.Families[0].Split = "development"
	p.Families[1].Split = "selection" // keep one selection so validation still passes
	res, err := Upsert(db, p)

	// Assert
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "F001") && strings.Contains(w, "split") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected split warning, got %v", res.Warnings)
	}
}

func TestUpsertIdempotentAndHashStable(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	p := validPack(t)

	// Act
	first, err := Upsert(db, p)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	second, err := Upsert(db, p)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	// Assert
	if len(second.Warnings) != 0 {
		t.Fatalf("idempotent upsert should warn nothing, got %v", second.Warnings)
	}
	for k, h := range first.CaseHashes {
		if second.CaseHashes[k] != h || h == "" {
			t.Fatalf("case hash unstable for %s", k)
		}
	}
	for k, h := range first.FamilyHashes {
		if second.FamilyHashes[k] != h || h == "" {
			t.Fatalf("family hash unstable for %s", k)
		}
	}
	n1, _ := db.ListFamilies()
	n2, _ := db.ListCalibrationItems()
	if len(n1) != 2 || len(n2) != 1 {
		t.Fatalf("row counts changed: families=%d calib=%d", len(n1), len(n2))
	}
	stored, err := db.GetCaseByKey("F001-base")
	if err != nil {
		t.Fatalf("GetCaseByKey: %v", err)
	}
	if stored.InputHash != first.CaseHashes["F001-base"] {
		t.Fatal("stored input_hash differs from reported hash")
	}
}

func TestExamplePackValidates(t *testing.T) {
	// Arrange: the shipped example files on disk.
	dir := filepath.Join("..", "..", "casepack")

	// Act
	p, err := LoadDir(dir)

	// Assert
	if err != nil {
		t.Fatalf("LoadDir example: %v", err)
	}
	summary, err := Validate(p)
	if err != nil {
		t.Fatalf("Validate example: %v", err)
	}
	if summary.SelectionFamilies < 1 || summary.Families < 2 {
		t.Fatalf("weak example summary: %+v", summary)
	}
}
