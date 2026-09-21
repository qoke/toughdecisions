// Package render renders parsed views/judges (and raw fallback text) to
// uniform, blind Markdown: the same section template for all seats, labelled
// by role name only. Never include model names, grades, or rankings here.
package render

import (
	"strings"

	"github.com/qoke/toughdecisions/internal/schema"
)

func section(b *strings.Builder, heading, body string) {
	b.WriteString("### " + heading + "\n\n")
	if strings.TrimSpace(body) == "" {
		body = "—"
	}
	b.WriteString(strings.TrimSpace(body) + "\n\n")
}

// ViewMarkdown renders one independent view. Sections follow the spec order:
// urgent danger (if present), qualification, reply, insight, trade-off,
// depends-on, fallback.
func ViewMarkdown(v schema.View, role string) string {
	var b strings.Builder
	b.WriteString("## " + strings.TrimSpace(role) + "\n\n")
	if v.UrgentDanger.Present {
		caution := v.UrgentDanger.Caution
		if strings.TrimSpace(caution) == "" {
			caution = "Credible urgent danger flagged — treat with care."
		}
		section(&b, "Urgent danger", caution)
	}
	section(&b, "Necessary qualification", v.Qualification)
	reply := v.SuggestedReply
	if strings.TrimSpace(v.RecommendedMove) != "" {
		reply += "\n\nRecommended move: " + strings.TrimSpace(v.RecommendedMove)
	}
	section(&b, "Suggested reply", reply)
	section(&b, "Decisive insight", v.DecisiveInsight)
	section(&b, "Main trade-off / objection", v.TradeoffOrObjection)
	section(&b, "Depends on / what would change it", v.DependsOn)
	section(&b, "Fallback", v.Fallback)
	return strings.TrimSpace(b.String()) + "\n"
}

// JudgeMarkdown renders the synthesis. Sections follow the spec order:
// reply/action, why, accepted cost, next, change-course-if, disagreement.
func JudgeMarkdown(j schema.Judge) string {
	var b strings.Builder
	b.WriteString("## Council recommendation\n\n")
	reply := j.RecommendedReply
	if strings.TrimSpace(j.RecommendedAction) != "" {
		reply += "\n\nRecommended action: " + strings.TrimSpace(j.RecommendedAction)
	}
	section(&b, "Recommended reply / action", reply)
	section(&b, "Why", j.Why)
	section(&b, "Accepted cost", j.AcceptedCost)
	next := "Immediate: " + strings.TrimSpace(j.Next.Immediate) +
		"\n\nForward: " + strings.TrimSpace(j.Next.Forward)
	section(&b, "Next", next)
	section(&b, "Change course if", j.ChangeCourseIf)
	section(&b, "Unresolved disagreement", j.UnresolvedDisagreement)
	return strings.TrimSpace(b.String()) + "\n"
}

// Raw renders unparseable output in the same outer template so parsed and
// raw renderings stay visually consistent.
func Raw(text string) string {
	var b strings.Builder
	b.WriteString("## Council output\n\n")
	section(&b, "Raw output", text)
	return strings.TrimSpace(b.String()) + "\n"
}
