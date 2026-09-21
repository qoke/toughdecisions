package council

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

func setupFixtureWithPack(t *testing.T, packYAML string, scripts map[string][]gateway.Step) *testFixture {
	t.Helper()
	t.Setenv("COUNCIL_VIEWS_DEADLINE", "2s")
	t.Setenv("COUNCIL_JUDGE_DEADLINE", "2s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := pack.Init(db, writeTemp(t, "pack.yaml", packYAML)); err != nil {
		t.Fatalf("pack.Init: %v", err)
	}
	mreg, err := models.LoadRegistry(writeTemp(t, "models.yaml", testModelsYAML))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	th, err := db.CreateThread("t")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := db.InsertCard(th.ID, testCardJSON); err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	fake := gateway.NewFake(scripts)
	reg := NewRegistry(5 * time.Minute)
	t.Cleanup(reg.Close)
	log := logx.NewWithWriter(cfg, testWriter{t})
	return &testFixture{db: db, fake: fake, runner: NewRunner(db, fake, reg, cfg, log, mreg), reg: reg, thread: th.ID}
}

func withModelReturned(scripts map[string][]gateway.Step) map[string][]gateway.Step {
	out := make(map[string][]gateway.Step, len(scripts))
	for model, steps := range scripts {
		cp := make([]gateway.Step, len(steps))
		copy(cp, steps)
		for i := range cp {
			if cp[i].ModelReturned == "" {
				cp[i].ModelReturned = model
			}
		}
		out[model] = cp
	}
	return out
}

// TestRunHonoursRolePromptOverride is RED: production passes the seat's
// RolePromptOverride into the prompt builder (fd-review B1).
func TestRunHonoursRolePromptOverride(t *testing.T) {
	// Arrange
	const overrideYAML = `seats:
  possibility: {model: v-poss, family: openai, role_prompt_override: "OVERRIDE POSSIBILITY ROLE"}
  perspective: {model: v-persp, family: openai}
  stress_tester: {model: v-stress, family: openai}
  judge: {model: j1, family: openai}
`
	fx := setupFixtureWithPack(t, overrideYAML, withModelReturned(map[string][]gateway.Step{
		"v-poss":   {{Content: testViewJSON}},
		"v-persp":  {{Content: testViewJSON}},
		"v-stress": {{Content: testViewJSON}},
		"j1":       {{Content: testJudgeJSON}},
	}))

	// Act
	id, err := fx.runner.Run(t.Context(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete", 10*time.Second)

	// Assert
	var possSys, perspSys string
	for _, c := range fx.fake.Calls {
		if len(c.Messages) == 0 {
			continue
		}
		switch c.Model {
		case "v-poss":
			possSys = c.Messages[0].Content
		case "v-persp":
			perspSys = c.Messages[0].Content
		}
	}
	if possSys == "" {
		t.Fatal("no recorded call for the possibility seat")
	}
	if !strings.Contains(possSys, "OVERRIDE POSSIBILITY ROLE") {
		t.Error("possibility system prompt does not contain the seat override")
	}
	if strings.Contains(possSys, "Find the strongest feasible path") {
		t.Error("possibility system prompt still contains the embedded role text")
	}
	if perspSys == "" {
		t.Fatal("no recorded call for the perspective seat")
	}
	if !strings.Contains(perspSys, "strongest evidence-consistent interpretations") {
		t.Error("perspective system prompt lost its embedded role text")
	}
	if strings.Contains(perspSys, "OVERRIDE POSSIBILITY ROLE") {
		t.Error("override leaked into a seat that has no override")
	}
}
