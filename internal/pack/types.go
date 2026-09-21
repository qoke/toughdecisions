// Package pack holds the production pack seat types.
//
// internal/models imports this package (one-way); this package must not
// import internal/models (or prompts/store).
package pack

import (
	"github.com/qoke/toughdecisions/internal/hash"
	"github.com/qoke/toughdecisions/internal/ids"
)

// Output-budget defaults (spec §1b; plan §7).
const (
	DefaultViewsMaxOutputTokens = 2000
	DefaultJudgeMaxOutputTokens = 3000
)

// Seat is a council seat identifier.
type Seat string

// Council seats.
const (
	SeatPossibility  Seat = "possibility"
	SeatPerspective  Seat = "perspective"
	SeatStressTester Seat = "stress_tester"
	SeatJudge        Seat = "judge"
)

// AllSeats lists every council seat in canonical order.
var AllSeats = []Seat{SeatPossibility, SeatPerspective, SeatStressTester, SeatJudge}

// Valid reports whether s is a known council seat.
func (s Seat) Valid() bool {
	switch s {
	case SeatPossibility, SeatPerspective, SeatStressTester, SeatJudge:
		return true
	default:
		return false
	}
}

// DefaultMaxOutputTokens returns the output-budget default for a seat:
// 3000 for the judge, 2000 for the three view seats, 2000 for anything else.
func DefaultMaxOutputTokens(seat Seat) int {
	if seat == SeatJudge {
		return DefaultJudgeMaxOutputTokens
	}
	return DefaultViewsMaxOutputTokens
}

// SeatConfig is the frozen configuration for one council seat.
type SeatConfig struct {
	Seat               Seat     `json:"seat"`
	Model              string   `json:"model"`
	Family             string   `json:"family"`
	Temperature        *float64 `json:"temperature"`
	TopP               *float64 `json:"top_p"`
	ReasoningEffort    string   `json:"reasoning_effort"`
	MaxOutputTokens    int      `json:"max_output_tokens"`
	RolePromptOverride string   `json:"role_prompt_override"`
}

// Hash returns the sha256 hex of the canonical JSON of ALL fields,
// including RolePromptOverride.
func (s SeatConfig) Hash() string {
	return hash.SHA256Hex(hash.CanonicalJSON(s))
}

// Pack is one immutable production pack.
type Pack struct {
	ID             string
	Seats          map[Seat]SeatConfig
	PromptPackHash string
	Status         string
}

// NewID returns a fresh pack id.
func NewID() string { return ids.NewID() }
