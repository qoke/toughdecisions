package prompts_test

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
)

func testInput() schema.CaseInput {
	return schema.CaseInput{
		Card: schema.Card{
			Decision:   "Whether to move in together",
			Context:    "Dating for a year, different cities",
			Priorities: "Honesty and autonomy",
			Unusual:    "Night-shift schedule",
			History:    "One brief breakup in spring",
			Deadline:   "Lease ends Friday",
			Style:      "Direct",
		},
		Messages: []schema.Message{
			{Sender: "them", Text: "We need to decide soon.", TS: "2026-09-01T10:00:00Z"},
			{Sender: "me", Text: "I need more time.", TS: "2026-09-01T10:05:00Z"},
		},
		Question: "What should I reply?",
		Style:    "short",
	}
}

// TestBuildViewSystemHasSharedRoleContract is the RED test for view assembly.
func TestBuildViewSystemHasSharedRoleContract(t *testing.T) {
	msgs, err := prompts.BuildView("possibility", testInput())
	if err != nil {
		t.Fatalf("BuildView: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("want [system user], got %+v", msgs)
	}
	for _, want := range []string{
		"Work from the supplied account",   // shared instructions
		"Find the strongest feasible path", // possibility role prompt
		"150–220 words",                    // view contract
		"JSON matching the schema",         // JSON instruction
	} {
		if !strings.Contains(msgs[0].Content, want) {
			t.Errorf("system message missing %q", want)
		}
	}
}

// TestBuildViewUserHasCardAndMessages is the RED test for context rendering.
func TestBuildViewUserHasCardAndMessages(t *testing.T) {
	msgs, err := prompts.BuildView("perspective", testInput())
	if err != nil {
		t.Fatalf("BuildView: %v", err)
	}
	user := msgs[1].Content
	for _, want := range []string{
		"Whether to move in together", "Dating for a year",
		"Honesty and autonomy", "Night-shift schedule",
		"One brief breakup in spring", "Lease ends Friday",
		"them", "2026-09-01T10:00:00Z", "We need to decide soon.",
		"me", "What should I reply?",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user message missing %q", want)
		}
	}
	if strings.Contains(user, "council-gpt-x") || strings.Contains(user, "gpt-x-2026") {
		t.Error("user message leaks a model id")
	}
}

// TestBuildViewUnknownRole is the RED test for the unknown-role error.
func TestBuildViewUnknownRole(t *testing.T) {
	if _, err := prompts.BuildView("nope", testInput()); err == nil {
		t.Fatal("want error for unknown role, got nil")
	}
}

func testViews() map[string]schema.RenderedView {
	return map[string]schema.RenderedView{
		"possibility": {
			Role: "possibility", Qualification: "If both cooperate",
			SuggestedReply: "Let's try this.", DecisiveInsight: "The lease drives timing.",
			TradeoffOrObjection: "Moving too fast risks resentment.",
			DependsOn:           "Her job transfer.", Fallback: "Stay separate one more term.",
		},
		"perspective": {
			Role: "perspective", Qualification: "No danger seen",
			SuggestedReply: "Ask for a week.", DecisiveInsight: "Autonomy matters most.",
			TradeoffOrObjection: "Delay may frustrate her.",
			DependsOn:           "Whether she can wait.", Fallback: "Weekly visits.",
		},
	}
}

func TestBuildJudgeNoViewsNotice(t *testing.T) {
	msgs, err := prompts.BuildJudge(testInput(), nil, []string{"possibility", "perspective", "stress_tester"}, true)
	if err != nil {
		t.Fatalf("BuildJudge: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("want [system user], got %+v", msgs)
	}
	if !strings.Contains(msgs[1].Content, "No independent views are available") {
		t.Error("judge user message missing the no-independent-views notice")
	}
	if !strings.Contains(msgs[1].Content, "Missing roles: possibility, perspective, stress_tester") {
		t.Error("judge user message missing missing-roles list")
	}
	if !strings.Contains(msgs[0].Content, "consequential issue every view may have missed") {
		t.Error("judge system message missing audit checklist")
	}
	if strings.Contains(msgs[1].Content, "council-gpt-x") {
		t.Error("judge user message leaks a model id")
	}
}

func TestBuildJudgeIncludesRoleLabelledViews(t *testing.T) {
	msgs, err := prompts.BuildJudge(testInput(), testViews(), []string{"stress_tester"}, false)
	if err != nil {
		t.Fatalf("BuildJudge: %v", err)
	}
	user := msgs[1].Content
	for _, want := range []string{
		"## possibility", "## perspective", "Let's try this.",
		"Missing roles: stress_tester",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("judge user message missing %q", want)
		}
	}
	if strings.Contains(user, "No independent views are available") {
		t.Error("judge user message must not include the no-views notice when views exist")
	}
}

func TestBuildJudgeSystemHasSharedAndContract(t *testing.T) {
	msgs, err := prompts.BuildJudge(testInput(), nil, nil, true)
	if err != nil {
		t.Fatalf("BuildJudge: %v", err)
	}
	for _, want := range []string{
		"Work from the supplied account",
		"Resolve disagreements through evidence",
		"200–300 words",
	} {
		if !strings.Contains(msgs[0].Content, want) {
			t.Errorf("judge system message missing %q", want)
		}
	}
}

func TestPromptAndRubricHashSeparation(t *testing.T) {
	base := map[string]string{
		"files/shared_instructions.md": "shared",
		"files/roles/possibility.md":   "role",
		"files/contracts/view.md":      "contract",
		"files/graders/absolute.md":    "rubric",
	}
	beforePack := prompts.HashFSForTest(mapFS(t, base), false)
	beforeRubric := prompts.HashFSForTest(mapFS(t, base), true)
	if beforePack == "" || beforeRubric == "" {
		t.Fatal("hash seam returned empty hash")
	}
	if beforePack == beforeRubric {
		t.Fatal("pack and rubric hashes must differ on these fixtures")
	}
	// Changing a non-grader file changes PromptPackHash, not RubricHash.
	changed := map[string]string{
		"files/shared_instructions.md": "shared v2",
		"files/roles/possibility.md":   "role",
		"files/contracts/view.md":      "contract",
		"files/graders/absolute.md":    "rubric",
	}
	if got := prompts.HashFSForTest(mapFS(t, changed), false); got == beforePack {
		t.Error("PromptPackHash did not change after a non-grader file changed")
	}
	if got := prompts.HashFSForTest(mapFS(t, changed), true); got != beforeRubric {
		t.Error("RubricHash changed after a non-grader file changed")
	}
	// Changing a grader file changes RubricHash, not PromptPackHash.
	changedGrader := map[string]string{
		"files/shared_instructions.md": "shared",
		"files/roles/possibility.md":   "role",
		"files/contracts/view.md":      "contract",
		"files/graders/absolute.md":    "rubric v2",
	}
	if got := prompts.HashFSForTest(mapFS(t, changedGrader), true); got == beforeRubric {
		t.Error("RubricHash did not change after a grader file changed")
	}
	if got := prompts.HashFSForTest(mapFS(t, changedGrader), false); got != beforePack {
		t.Error("PromptPackHash changed after a grader file changed")
	}
}

func mapFS(t *testing.T, files map[string]string) fstest.MapFS {
	t.Helper()
	m := make(fstest.MapFS, len(files))
	for k, v := range files {
		m[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return m
}

func TestPromptPackHashAndRubricHashNonEmpty(t *testing.T) {
	if prompts.PromptPackHash() == "" {
		t.Error("PromptPackHash is empty")
	}
	if prompts.RubricHash() == "" {
		t.Error("RubricHash is empty")
	}
	if prompts.PromptPackHash() == prompts.RubricHash() {
		t.Error("pack and rubric hashes must differ")
	}
}

func writeGolden(t *testing.T, name, content string) {
	t.Helper()
	if os.Getenv("WRITE_GOLDENS") == "" {
		return
	}
	if err := os.WriteFile("testdata/"+name, []byte(content), 0o644); err != nil {
		t.Fatalf("write golden %s: %v", name, err)
	}
}

func checkGolden(t *testing.T, name, content string) {
	t.Helper()
	writeGolden(t, name, content)
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with WRITE_GOLDENS=1)", name, err)
	}
	if string(want) != content {
		t.Errorf("golden %s mismatch: got %d bytes, want %d bytes", name, len(content), len(want))
	}
}

func TestGoldenBuildView(t *testing.T) {
	msgs, err := prompts.BuildView("possibility", testInput())
	if err != nil {
		t.Fatalf("BuildView: %v", err)
	}
	checkGolden(t, "view_possibility.system.md", msgs[0].Content)
	checkGolden(t, "view_possibility.user.md", msgs[1].Content)
}

func TestGoldenBuildJudge(t *testing.T) {
	msgs, err := prompts.BuildJudge(testInput(), testViews(), []string{"stress_tester"}, false)
	if err != nil {
		t.Fatalf("BuildJudge: %v", err)
	}
	checkGolden(t, "judge.system.md", msgs[0].Content)
	checkGolden(t, "judge.user.md", msgs[1].Content)

	noviews, err := prompts.BuildJudge(testInput(), nil, []string{"possibility", "perspective", "stress_tester"}, true)
	if err != nil {
		t.Fatalf("BuildJudge: %v", err)
	}
	checkGolden(t, "judge_noviews.user.md", noviews[1].Content)
}
