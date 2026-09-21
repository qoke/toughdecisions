// Package prompts assembles LLM prompts from embedded verbatim files.
//
// Role prompts and shared instructions are copied verbatim from
// PRODUCT_SPEC.md. Prompts never contain model names: views are labelled
// by role name only.
//
// This package must NOT import internal/pack (pack imports prompts).
package prompts

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

//go:embed files/shared_instructions.md
var sharedInstructionsMD string

//go:embed files/roles/possibility.md
var rolePossibilityMD string

//go:embed files/roles/perspective.md
var rolePerspectiveMD string

//go:embed files/roles/stress_tester.md
var roleStressTesterMD string

//go:embed files/roles/judge.md
var roleJudgeMD string

//go:embed files/contracts/view.md
var contractViewMD string

//go:embed files/contracts/judge.md
var contractJudgeMD string

//go:embed files/rewrite.md
var rewriteMD string

//go:embed files/graders/absolute.md
var graderAbsoluteMD string

//go:embed files/graders/pairwise.md
var graderPairwiseMD string

//go:embed files/graders/coverage.md
var graderCoverageMD string

//go:embed files/graders/calibration.md
var graderCalibrationMD string

//go:embed files
var promptFiles embed.FS

// roleFile resolves a role name to its embedded role prompt.
func roleFile(role string) (string, error) {
	switch role {
	case "possibility":
		return rolePossibilityMD, nil
	case "perspective":
		return rolePerspectiveMD, nil
	case "stress_tester":
		return roleStressTesterMD, nil
	case "judge":
		return roleJudgeMD, nil
	default:
		return "", fmt.Errorf("prompts: unknown role %q", role)
	}
}

// renderContext renders the user message per files/context_format.md:
// Case card (7 fields) / Messages (verbatim, sender+ts) / Current request.
func renderContext(in schema.CaseInput) string {
	var b strings.Builder
	b.WriteString("Case card:\n")
	fmt.Fprintf(&b, "- Current question or decision: %s\n", in.Card.Decision)
	fmt.Fprintf(&b, "- Relevant relationship context: %s\n", in.Card.Context)
	fmt.Fprintf(&b, "- Your priorities and boundaries: %s\n", in.Card.Priorities)
	fmt.Fprintf(&b, "- Important unusual facts and practical constraints: %s\n", in.Card.Unusual)
	fmt.Fprintf(&b, "- What has actually happened or been tried: %s\n", in.Card.History)
	fmt.Fprintf(&b, "- Deadline and cost of waiting: %s\n", in.Card.Deadline)
	fmt.Fprintf(&b, "- Preferred reply style: %s\n", in.Card.Style)
	b.WriteString("\nMessages (verbatim, with sender and timestamp):\n")
	for _, m := range in.Messages {
		fmt.Fprintf(&b, "- [%s %s] %s\n", m.Sender, m.TS, m.Text)
	}
	b.WriteString("\nCurrent request: " + in.Question)
	if in.Style != "" {
		b.WriteString(" (style: " + in.Style + ")")
	}
	return b.String()
}

// resolveRole returns the embedded role prompt, or the override when set.
// An unknown role is always an error, even when an override is supplied, so
// callers cannot silently address a seat that does not exist.
func resolveRole(role, override string) (string, error) {
	embedded, err := roleFile(role)
	if err != nil {
		return "", err
	}
	if override != "" {
		return override, nil
	}
	return embedded, nil
}

// viewSystem builds the system message for a view seat.
func viewSystem(role string) (string, error) {
	return viewSystemWithOverride(role, "")
}

// viewSystemWithOverride builds the view system message, replacing the
// embedded role prompt when override is non-empty.
func viewSystemWithOverride(role, override string) (string, error) {
	rolePrompt, err := resolveRole(role, override)
	if err != nil {
		return "", err
	}
	return sharedInstructionsMD + "\n\n" + rolePrompt + "\n\n" + contractViewMD, nil
}

// BuildView assembles the [system, user] messages for one independent view.
// role is a plain role name ("possibility", "perspective", "stress_tester").
func BuildView(role string, in schema.CaseInput) ([]gateway.Message, error) {
	return BuildViewWithOverride(role, "", in)
}

// BuildViewWithOverride is BuildView with a role-prompt override: a
// non-empty override replaces the embedded role prompt for that seat.
// An empty override uses the embedded file unchanged.
func BuildViewWithOverride(role, override string, in schema.CaseInput) ([]gateway.Message, error) {
	sys, err := viewSystemWithOverride(role, override)
	if err != nil {
		return nil, err
	}
	return []gateway.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: renderContext(in)},
	}, nil
}

// renderJudgeView renders one independent view labelled by role name only.
func renderJudgeView(role string, v schema.RenderedView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n", role)
	if v.UrgentDanger.Present {
		fmt.Fprintf(&b, "Urgent danger: %s\n", v.UrgentDanger.Caution)
	}
	fmt.Fprintf(&b, "Qualification: %s\n", v.Qualification)
	fmt.Fprintf(&b, "Suggested reply: %s\n", v.SuggestedReply)
	fmt.Fprintf(&b, "Decisive insight: %s\n", v.DecisiveInsight)
	fmt.Fprintf(&b, "Main trade-off or strongest objection: %s\n", v.TradeoffOrObjection)
	fmt.Fprintf(&b, "What the advice depends on: %s\n", v.DependsOn)
	fmt.Fprintf(&b, "Fallback: %s\n", v.Fallback)
	return b.String()
}

// noViewsNotice is used when no independent views are available.
const noViewsNotice = "No independent views are available. Answer directly from the original material, clearly labeled as lacking independent views."

// judgeSystem builds the judge system message with an optional role-prompt
// override. An empty override uses the embedded judge role file unchanged.
func judgeSystem(override string) string {
	rolePrompt := roleJudgeMD
	if override != "" {
		rolePrompt = override
	}
	return sharedInstructionsMD + "\n\n" + rolePrompt + "\n\n" + contractJudgeMD
}

// BuildJudge assembles the [system, user] messages for the synthesis judge.
// views is keyed by role name only; model ids must never appear.
func BuildJudge(in schema.CaseInput, views map[string]schema.RenderedView, missing []string, noViews bool) ([]gateway.Message, error) {
	return BuildJudgeWithOverride(in, views, missing, noViews, "")
}

// BuildJudgeWithOverride is BuildJudge with a role-prompt override: a
// non-empty override replaces the embedded judge role prompt.
func BuildJudgeWithOverride(in schema.CaseInput, views map[string]schema.RenderedView, missing []string, noViews bool, override string) ([]gateway.Message, error) {
	sys := judgeSystem(override)
	var b strings.Builder
	b.WriteString(renderContext(in))
	if len(views) > 0 {
		roles := make([]string, 0, len(views))
		for r := range views {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		b.WriteString("\n\nIndependent views by role:\n")
		for _, r := range roles {
			b.WriteString("\n" + renderJudgeView(r, views[r]))
		}
	}
	if len(missing) > 0 {
		b.WriteString("\nMissing roles: " + strings.Join(missing, ", "))
	}
	if noViews {
		b.WriteString("\n" + noViewsNotice)
	}
	return []gateway.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}, nil
}

// graderSystem builds the system message for a grader: shared instructions
// plus the selected grader rubric. The role name labels the response under
// review; model names never appear in any prompt.
func graderSystem(role, rubric string) (string, error) {
	if _, err := roleFile(role); err != nil {
		return "", err
	}
	return sharedInstructionsMD + "\n\n" + rubric, nil
}

// gradeAcceptanceBlock renders the acceptance notes as constraints.
func gradeAcceptanceBlock(acceptance string) string {
	if strings.TrimSpace(acceptance) == "" {
		return ""
	}
	return "\n\nAcceptance notes (constraints, not a prescribed answer):\n" + acceptance
}

// BuildAbsoluteGrade assembles the [system, user] messages for the absolute
// (0-4) rubric grader over one rendered response.
func BuildAbsoluteGrade(in schema.CaseInput, acceptance, role, rendered string) ([]gateway.Message, error) {
	sys, err := graderSystem(role, graderAbsoluteMD)
	if err != nil {
		return nil, err
	}
	user := renderContext(in) + "\n\nResponse under review (role: " + role + "):\n" + rendered + gradeAcceptanceBlock(acceptance)
	return []gateway.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}, nil
}

// BuildPairwiseGrade assembles the [system, user] messages for the blind A/B
// grader over two rendered responses.
func BuildPairwiseGrade(in schema.CaseInput, acceptance, role, a, b string) ([]gateway.Message, error) {
	sys, err := graderSystem(role, graderPairwiseMD)
	if err != nil {
		return nil, err
	}
	user := renderContext(in) + "\n\nResponse A (role: " + role + "):\n" + a + "\n\nResponse B (role: " + role + "):\n" + b + gradeAcceptanceBlock(acceptance)
	return []gateway.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}, nil
}

// BuildCoverageGrade assembles the [system, user] messages for the
// issue-coverage grader. views maps role name to rendered view text.
func BuildCoverageGrade(in schema.CaseInput, issues string, views map[string]string, judge string) ([]gateway.Message, error) {
	sys := sharedInstructionsMD + "\n\n" + graderCoverageMD
	var b strings.Builder
	b.WriteString(renderContext(in))
	if strings.TrimSpace(issues) != "" {
		b.WriteString("\n\nIssues:\n" + issues)
	}
	if len(views) > 0 {
		roles := make([]string, 0, len(views))
		for r := range views {
			roles = append(roles, r)
		}
		sort.Strings(roles)
		b.WriteString("\n\nViews by role:\n")
		for _, r := range roles {
			b.WriteString("\n## " + r + "\n" + views[r])
		}
	}
	if strings.TrimSpace(judge) != "" {
		b.WriteString("\n\nJudge output:\n" + judge)
	}
	return []gateway.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	}, nil
}

// BuildCalibrationGrade assembles the [system, user] messages for grader
// admission: the calibration rubric over one rendered response.
func BuildCalibrationGrade(in schema.CaseInput, acceptance, role, rendered string) ([]gateway.Message, error) {
	sys, err := graderSystem(role, graderCalibrationMD)
	if err != nil {
		return nil, err
	}
	user := renderContext(in) + "\n\nResponse under review (role: " + role + "):\n" + rendered + gradeAcceptanceBlock(acceptance)
	return []gateway.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}, nil
}

// BuildRewrite assembles the [system, user] messages for a one-call rewrite.
// Meaning, commitments, and boundaries are preserved; the instruction is applied.
func BuildRewrite(draft, instruction string, in schema.CaseInput) ([]gateway.Message, error) {
	sys := sharedInstructionsMD + "\n\n" + rewriteMD
	user := renderContext(in) + "\n\nDraft:\n" + draft + "\n\nInstruction:\n" + instruction
	return []gateway.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}, nil
}

// hashFS hashes all files in fsys selected by include. Files are hashed in
// sorted path order as sha256(path + 0x00 + content), hex-encoded. It is the
// testable seam behind PromptPackHash and RubricHash.
func hashFS(fsys fs.FS, include func(string) bool) (string, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if include != nil && !include(path) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("prompts: hash walk: %w", err)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", fmt.Errorf("prompts: hash read %s: %w", p, err)
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(content)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isGrader(path string) bool {
	return strings.HasPrefix(path, "files/graders/") || strings.HasPrefix(path, "graders/")
}

// PromptPackHash is the sha256 over all embedded files under files/
// EXCEPT graders/*.
func PromptPackHash() string {
	h, err := hashFS(promptFiles, func(path string) bool {
		return !isGrader(path) && strings.HasSuffix(path, ".md")
	})
	if err != nil {
		return ""
	}
	return h
}

// RubricHash is the sha256 over files/graders/*.
func RubricHash() string {
	h, err := hashFS(promptFiles, func(path string) bool {
		return isGrader(path)
	})
	if err != nil {
		return ""
	}
	return h
}
