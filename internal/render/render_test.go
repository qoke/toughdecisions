package render

import (
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/schema"
)

func testView() schema.View {
	return schema.View{
		UrgentDanger:        schema.Danger{Present: true, Caution: "watch the deadline"},
		Qualification:       "only if X holds",
		SuggestedReply:      "here is a draft",
		RecommendedMove:     "send it tuesday",
		DecisiveInsight:     "the pivotal uncertainty is Y",
		TradeoffOrObjection: "costs Z",
		DependsOn:           "depends on W",
		Fallback:            "fallback plan",
	}
}

func testJudge() schema.Judge {
	j := schema.Judge{
		Qualification:     "qual",
		RecommendedReply:  "recommended reply text",
		RecommendedAction: "take the meeting",
		Why:               "because priorities say so",
		AcceptedCost:      "accepted cost text",
		ChangeCourseIf:    "if new facts arrive",
	}
	j.Next.Immediate = "send tonight"
	j.Next.Forward = "review next week"
	j.UnresolvedDisagreement = "views differ on timing"
	return j
}

func TestViewMarkdownSections(t *testing.T) {
	md := ViewMarkdown(testView(), "Possibility")
	for _, want := range []string{
		"## Possibility", "Urgent danger", "Necessary qualification",
		"Suggested reply", "Decisive insight", "Main trade-off",
		"Depends on", "Fallback",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("ViewMarkdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "council-gpt-x") {
		t.Errorf("ViewMarkdown leaks model id:\n%s", md)
	}
}

func TestViewMarkdownOmitsDangerWhenAbsent(t *testing.T) {
	v := testView()
	v.UrgentDanger = schema.Danger{}
	md := ViewMarkdown(v, "Possibility")
	if strings.Contains(md, "Urgent danger") {
		t.Errorf("ViewMarkdown should omit danger section when absent:\n%s", md)
	}
}

func TestJudgeMarkdownSections(t *testing.T) {
	md := JudgeMarkdown(testJudge())
	for _, want := range []string{
		"Recommended reply", "Why", "Accepted cost",
		"Next", "Change course if", "Unresolved disagreement",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("JudgeMarkdown missing %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "gpt-x-2026") {
		t.Errorf("JudgeMarkdown leaks model id:\n%s", md)
	}
}

func TestRawWrapsText(t *testing.T) {
	md := Raw("some **raw** output <council-gpt-x>")
	if !strings.Contains(strings.ToLower(md), "raw output") {
		t.Errorf("Raw missing heading:\n%s", md)
	}
	if !strings.Contains(md, "some **raw** output") {
		t.Errorf("Raw dropped raw text:\n%s", md)
	}
}
