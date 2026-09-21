package pack

import (
	"testing"
)

func f64ptr(v float64) *float64 { return &v }

func baseSeatConfig() SeatConfig {
	return SeatConfig{
		Seat:            SeatPossibility,
		Model:           "council-gpt-x",
		Family:          "openai",
		Temperature:     f64ptr(0.7),
		TopP:            f64ptr(0.9),
		ReasoningEffort: "medium",
		MaxOutputTokens: DefaultViewsMaxOutputTokens,
	}
}

func TestSeatValid(t *testing.T) {
	valid := []Seat{SeatPossibility, SeatPerspective, SeatStressTester, SeatJudge}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("Seat(%q).Valid() = false, want true", s)
		}
	}
	for _, s := range []Seat{"", "possibilities", "JUDGE", "judge "} {
		if s.Valid() {
			t.Errorf("Seat(%q).Valid() = true, want false", s)
		}
	}
	if len(AllSeats) != 4 {
		t.Fatalf("len(AllSeats) = %d, want 4", len(AllSeats))
	}
}

func TestDefaultMaxOutputTokens(t *testing.T) {
	cases := []struct {
		seat Seat
		want int
	}{
		{SeatPossibility, 2000},
		{SeatPerspective, 2000},
		{SeatStressTester, 2000},
		{SeatJudge, 3000},
	}
	for _, c := range cases {
		if got := DefaultMaxOutputTokens(c.seat); got != c.want {
			t.Errorf("DefaultMaxOutputTokens(%q) = %d, want %d", c.seat, got, c.want)
		}
	}
	if DefaultViewsMaxOutputTokens != 2000 {
		t.Errorf("DefaultViewsMaxOutputTokens = %d, want 2000", DefaultViewsMaxOutputTokens)
	}
	if DefaultJudgeMaxOutputTokens != 3000 {
		t.Errorf("DefaultJudgeMaxOutputTokens = %d, want 3000", DefaultJudgeMaxOutputTokens)
	}
}

func TestSeatConfigHashStable(t *testing.T) {
	a, b := baseSeatConfig(), baseSeatConfig()
	if a.Hash() != b.Hash() {
		t.Fatalf("identical configs hash differently: %q vs %q", a.Hash(), b.Hash())
	}
	if len(a.Hash()) != 64 {
		t.Fatalf("hash %q is not 64-char sha256 hex", a.Hash())
	}
}

func TestSeatConfigHashChangesWhenAnyFieldChanges(t *testing.T) {
	base := baseSeatConfig()
	baseHash := base.Hash()
	cases := []struct {
		name   string
		mutate func(*SeatConfig)
	}{
		{"seat", func(s *SeatConfig) { s.Seat = SeatJudge }},
		{"model", func(s *SeatConfig) { s.Model = "council-gpt-y" }},
		{"family", func(s *SeatConfig) { s.Family = "anthropic" }},
		{"temperature", func(s *SeatConfig) { s.Temperature = f64ptr(0.1) }},
		{"temperature_nil", func(s *SeatConfig) { s.Temperature = nil }},
		{"top_p", func(s *SeatConfig) { s.TopP = f64ptr(0.2) }},
		{"top_p_nil", func(s *SeatConfig) { s.TopP = nil }},
		{"reasoning_effort", func(s *SeatConfig) { s.ReasoningEffort = "high" }},
		{"max_output_tokens", func(s *SeatConfig) { s.MaxOutputTokens = 999 }},
		{"role_prompt_override", func(s *SeatConfig) { s.RolePromptOverride = "custom role text" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := baseSeatConfig()
			c.mutate(&v)
			if got := v.Hash(); got == baseHash {
				t.Errorf("changing %s did not change Hash()", c.name)
			}
		})
	}
}
