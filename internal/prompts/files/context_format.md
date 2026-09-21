Case card:
- Current question or decision: {{.Card.Decision}}
- Relevant relationship context: {{.Card.Context}}
- Your priorities and boundaries: {{.Card.Priorities}}
- Important unusual facts and practical constraints: {{.Card.Unusual}}
- What has actually happened or been tried: {{.Card.History}}
- Deadline and cost of waiting: {{.Card.Deadline}}
- Preferred reply style: {{.Card.Style}}

Messages (verbatim, with sender and timestamp):
{{range .Messages}}- [{{.Sender}} {{.TS}}] {{.Text}}
{{end}}
Current request: {{.Question}}{{if .Style}} (style: {{.Style}}){{end}}
