package prompts_test

import (
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/prompts"
)

const testAcceptance = "must notice the lease deadline; cannot assume she agrees"

// TestBuildViewNoOverrideIsByteIdentical guards the production hot path:
// a view built with no override must match the legacy two-arg builder,
// and an explicit empty override must behave the same.
func TestBuildViewNoOverrideIsByteIdentical(t *testing.T) {
	// Arrange
	in := testInput()

	// Act
	legacy, err := prompts.BuildView("possibility", in)
	if err != nil {
		t.Fatalf("BuildView: %v", err)
	}
	current, err := prompts.BuildViewWithOverride("possibility", "", in)
	if err != nil {
		t.Fatalf("BuildViewWithOverride(empty): %v", err)
	}

	// Assert
	if len(legacy) != 2 || len(current) != 2 {
		t.Fatalf("want 2 messages, got %d/%d", len(legacy), len(current))
	}
	for i := range legacy {
		if legacy[i].Role != current[i].Role || legacy[i].Content != current[i].Content {
			t.Fatalf("message %d differs with no override", i)
		}
	}
}

// TestBuildViewOverrideReplacesRolePrompt checks the override replaces the
// embedded role text while keeping shared instructions and the contract.
func TestBuildViewOverrideReplacesRolePrompt(t *testing.T) {
	// Arrange
	in := testInput()

	// Act
	msgs, err := prompts.BuildViewWithOverride("possibility", "CUSTOM ROLE TEXT", in)
	if err != nil {
		t.Fatalf("BuildViewWithOverride: %v", err)
	}

	// Assert
	sys := msgs[0].Content
	if !strings.Contains(sys, "CUSTOM ROLE TEXT") {
		t.Error("system message missing the override text")
	}
	if strings.Contains(sys, "Find the strongest feasible path") {
		t.Error("system message still contains the embedded possibility role text")
	}
	if !strings.Contains(sys, "Work from the supplied account") {
		t.Error("system message lost the shared instructions")
	}
	if !strings.Contains(sys, "JSON matching the schema") {
		t.Error("system message lost the view contract")
	}
}

// TestBuildViewOverrideUnknownRole returns an error for an unknown role.
func TestBuildViewOverrideUnknownRole(t *testing.T) {
	if _, err := prompts.BuildViewWithOverride("nope", "x", testInput()); err == nil {
		t.Fatal("want error for unknown role, got nil")
	}
}

// TestBuildJudgeNoOverrideIsByteIdentical guards the judge hot path.
func TestBuildJudgeNoOverrideIsByteIdentical(t *testing.T) {
	// Arrange
	in := testInput()
	views := testViews()
	missing := []string{"stress_tester"}

	// Act
	legacy, err := prompts.BuildJudge(in, views, missing, false)
	if err != nil {
		t.Fatalf("BuildJudge: %v", err)
	}
	current, err := prompts.BuildJudgeWithOverride(in, views, missing, false, "")
	if err != nil {
		t.Fatalf("BuildJudgeWithOverride(empty): %v", err)
	}

	// Assert
	for i := range legacy {
		if legacy[i].Role != current[i].Role || legacy[i].Content != current[i].Content {
			t.Fatalf("judge message %d differs with no override", i)
		}
	}
	// No-views variant must also be identical.
	legacyNV, err := prompts.BuildJudge(in, nil, []string{"possibility", "perspective", "stress_tester"}, true)
	if err != nil {
		t.Fatalf("BuildJudge no-views: %v", err)
	}
	currentNV, err := prompts.BuildJudgeWithOverride(in, nil, []string{"possibility", "perspective", "stress_tester"}, true, "")
	if err != nil {
		t.Fatalf("BuildJudgeWithOverride no-views: %v", err)
	}
	for i := range legacyNV {
		if legacyNV[i].Content != currentNV[i].Content {
			t.Fatalf("no-views judge message %d differs with no override", i)
		}
	}
}

// TestBuildJudgeOverrideReplacesRolePrompt checks the judge override.
func TestBuildJudgeOverrideReplacesRolePrompt(t *testing.T) {
	// Arrange
	in := testInput()

	// Act
	msgs, err := prompts.BuildJudgeWithOverride(in, nil, nil, true, "CUSTOM JUDGE ROLE")
	if err != nil {
		t.Fatalf("BuildJudgeWithOverride: %v", err)
	}

	// Assert
	sys := msgs[0].Content
	if !strings.Contains(sys, "CUSTOM JUDGE ROLE") {
		t.Error("judge system message missing the override text")
	}
	if strings.Contains(sys, "Resolve disagreements through evidence") {
		t.Error("judge system message still contains the embedded judge role text")
	}
}

func TestGraderBuildersSelectRubric(t *testing.T) {
	in := testInput()
	cases := []struct {
		name string
		call func() ([]gateway.Message, error)
		want string
	}{
		{"absolute", func() ([]gateway.Message, error) {
			return prompts.BuildAbsoluteGrade(in, testAcceptance, "possibility", "rendered text")
		}, "each scored 0–4"},
		{"pairwise", func() ([]gateway.Message, error) {
			return prompts.BuildPairwiseGrade(in, testAcceptance, "possibility", "A text", "B text")
		}, "consequential difference"},
		{"coverage", func() ([]gateway.Message, error) {
			return prompts.BuildCoverageGrade(in, "I1: lease deadline", map[string]string{"possibility": "v"}, "judge text")
		}, "issue-coverage table"},
		{"calibration", func() ([]gateway.Message, error) {
			return prompts.BuildCalibrationGrade(in, testAcceptance, "possibility", "rendered text")
		}, "absolute rubric"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Arrange + Act
			msgs, err := c.call()
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}

			// Assert
			if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
				t.Fatalf("%s: want [system user], got %+v", c.name, msgs)
			}
			if !strings.Contains(msgs[0].Content, "Work from the supplied account") {
				t.Errorf("%s: system message missing shared instructions", c.name)
			}
			if !strings.Contains(msgs[0].Content, c.want) {
				t.Errorf("%s: system message missing rubric marker %q", c.name, c.want)
			}
			if !strings.Contains(msgs[0].Content, "Return JSON matching the schema") {
				t.Errorf("%s: system message missing JSON instruction", c.name)
			}
			for _, leaked := range []string{"council-gpt-x", "gpt-x-2026"} {
				if strings.Contains(msgs[0].Content, leaked) || strings.Contains(msgs[1].Content, leaked) {
					t.Errorf("%s: prompt leaks a model id", c.name)
				}
			}
		})
	}
}

func TestGraderBuildersUnknownRole(t *testing.T) {
	in := testInput()
	if _, err := prompts.BuildAbsoluteGrade(in, testAcceptance, "nope", "r"); err == nil {
		t.Error("BuildAbsoluteGrade: want error for unknown role, got nil")
	}
	if _, err := prompts.BuildPairwiseGrade(in, testAcceptance, "nope", "a", "b"); err == nil {
		t.Error("BuildPairwiseGrade: want error for unknown role, got nil")
	}
	if _, err := prompts.BuildCalibrationGrade(in, testAcceptance, "nope", "r"); err == nil {
		t.Error("BuildCalibrationGrade: want error for unknown role, got nil")
	}
}
