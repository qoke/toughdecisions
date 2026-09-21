// Package pack implements the production-pack lifecycle on top of *store.DB.
//
// Pack ids come from internal/ids. The prompt-pack hash comes from
// internal/prompts. This package must not be imported by prompts
// (pack -> prompts, one way).
package pack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/store"
	"gopkg.in/yaml.v3"
)

// Pack statuses.
const (
	StatusActive   = "active"
	StatusPrevious = "previous"
	StatusRetired  = "retired"
)

// seatsFile is the YAML shape for Init's seat definition file.
type seatsFile struct {
	Seats map[string]seatYAML `yaml:"seats"`
}

type seatYAML struct {
	Model              string   `yaml:"model"`
	Family             string   `yaml:"family"`
	Temperature        *float64 `yaml:"temperature"`
	TopP               *float64 `yaml:"top_p"`
	ReasoningEffort    string   `yaml:"reasoning_effort"`
	MaxOutputTokens    int      `yaml:"max_output_tokens"`
	RolePromptOverride string   `yaml:"role_prompt_override"`
	RolePromptFile     string   `yaml:"role_prompt_file"`
}

// ResolveRolePromptFile validates ref against traversal/absolute paths and
// returns the resolved path inside the declaring YAML file's directory.
// dir is the directory of the declaring YAML file (e.g. filepath.Dir(seats
// file)). Absolute refs and refs escaping dir are hard errors.
func ResolveRolePromptFile(dir, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("pack: role_prompt_file is empty")
	}
	if filepath.IsAbs(ref) {
		return "", fmt.Errorf("pack: role_prompt_file %q must be a relative path inside the seats directory", ref)
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("pack: resolve base dir: %w", err)
	}
	candidate := filepath.Clean(filepath.Join(base, ref))
	if candidate != base && !strings.HasPrefix(candidate, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("pack: role_prompt_file %q escapes the seats directory", ref)
	}
	return candidate, nil
}

// decodeStrict unmarshals YAML while rejecting unknown/misspelled fields.
func decodeStrict(raw []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(v)
}

// resolveRolePromptOverride returns the inline override, or the content of
// role_prompt_file (resolved relative to the seats file) when set. Setting
// both is an error; a missing file is a hard error, never silently ignored.
func resolveRolePromptOverride(seat, seatsFile string, sc seatYAML) (string, error) {
	if sc.RolePromptOverride != "" && sc.RolePromptFile != "" {
		return "", fmt.Errorf("pack: seat %q sets both role_prompt_override and role_prompt_file", seat)
	}
	if sc.RolePromptFile == "" {
		return sc.RolePromptOverride, nil
	}
	path, err := ResolveRolePromptFile(filepath.Dir(seatsFile), sc.RolePromptFile)
	if err != nil {
		return "", fmt.Errorf("pack: seat %q %w", seat, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("pack: seat %q role_prompt_file %q: %w", seat, sc.RolePromptFile, err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", fmt.Errorf("pack: seat %q role_prompt_file %q is empty", seat, sc.RolePromptFile)
	}
	return text, nil
}

// loadSeats parses file, validates all 4 seats are present with no unknown
// seats, and applies DefaultMaxOutputTokens when max_output_tokens is 0.
func loadSeats(file string) (map[Seat]SeatConfig, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("pack: read seats file: %w", err)
	}
	var sf seatsFile
	if err := decodeStrict(raw, &sf); err != nil {
		return nil, fmt.Errorf("pack: parse seats file: %w", err)
	}
	if len(sf.Seats) == 0 {
		return nil, fmt.Errorf("pack: no seats defined")
	}
	out := make(map[Seat]SeatConfig, 4)
	for name, sc := range sf.Seats {
		seat := Seat(name)
		if !seat.Valid() {
			return nil, fmt.Errorf("pack: unknown seat %q", name)
		}
		maxTokens := sc.MaxOutputTokens
		if maxTokens == 0 {
			maxTokens = DefaultMaxOutputTokens(seat)
		}
		override, err := resolveRolePromptOverride(string(seat), file, sc)
		if err != nil {
			return nil, err
		}
		out[seat] = SeatConfig{
			Seat: seat, Model: sc.Model, Family: sc.Family,
			Temperature: sc.Temperature, TopP: sc.TopP,
			ReasoningEffort: sc.ReasoningEffort, MaxOutputTokens: maxTokens,
			RolePromptOverride: override,
		}
	}
	for _, seat := range AllSeats {
		if _, ok := out[seat]; !ok {
			return nil, fmt.Errorf("pack: missing seat %q", seat)
		}
	}
	return out, nil
}

// toPack converts a store row into a Pack.
func toPack(row *store.Pack) (*Pack, error) {
	var seats map[Seat]SeatConfig
	if err := json.Unmarshal([]byte(row.SeatsJSON), &seats); err != nil {
		return nil, fmt.Errorf("pack: decode seats: %w", err)
	}
	return &Pack{
		ID: row.ID, Seats: seats,
		PromptPackHash: row.PromptPackHash, Status: row.Status,
	}, nil
}

// insertPack persists a new pack row with the given status.
func insertPack(db *store.DB, seats map[Seat]SeatConfig, status, fromRunID string) (*Pack, error) {
	seatsJSON, err := json.Marshal(seats)
	if err != nil {
		return nil, fmt.Errorf("pack: encode seats: %w", err)
	}
	row, err := db.InsertPack(status, string(seatsJSON), prompts.PromptPackHash(), "")
	if err != nil {
		return nil, err
	}
	patch := map[string]any{}
	if fromRunID != "" {
		patch["published_from_run_id"] = fromRunID
	}
	patch["activated_at"] = row.CreatedAt
	if err := db.UpdatePackMeta(row.ID, patch); err != nil {
		return nil, err
	}
	row.PublishedFromRunID = nil
	if fromRunID != "" {
		row.PublishedFromRunID = &fromRunID
	}
	row.ActivatedAt = &row.CreatedAt
	return toPack(row)
}

// Init loads a seats YAML file into pack v1 with status "active".
func Init(db *store.DB, file string) (*Pack, error) {
	seats, err := loadSeats(file)
	if err != nil {
		return nil, err
	}
	return insertPack(db, seats, StatusActive, "")
}

// LoadSeats parses a seats YAML file without persisting anything. It lets
// callers validate every seat (e.g. model settings against the models
// registry) BEFORE any pack row is written, so a rejected init leaves the
// store untouched. See InitValidated.
func LoadSeats(file string) (map[Seat]SeatConfig, error) {
	return loadSeats(file)
}

// InsertActive persists an already-validated seats map as the active pack.
// Callers must validate seats before calling: this function persists
// unconditionally.
func InsertActive(db *store.DB, seats map[Seat]SeatConfig) (*Pack, error) {
	for _, seat := range AllSeats {
		if _, ok := seats[seat]; !ok {
			return nil, fmt.Errorf("pack: missing seat %q", seat)
		}
	}
	return insertPack(db, seats, StatusActive, "")
}

// InitValidated loads a seats YAML file, runs validate over every seat, and
// only then persists the pack as active. Validation (e.g. model settings
// against the models registry) therefore happens BEFORE any persistence or
// activation: a failed init writes no pack row and leaves no active pack
// behind. validate lives with the caller because the models registry
// imports this package (pack -> models would be an import cycle), and the
// CLI already owns the registry handle.
//
// A nil validate keeps the legacy load-then-persist behavior.
func InitValidated(db *store.DB, file string, validate func(SeatConfig) error) (*Pack, error) {
	seats, err := loadSeats(file)
	if err != nil {
		return nil, err
	}
	if validate != nil {
		for _, seat := range AllSeats {
			cfg, ok := seats[seat]
			if !ok {
				return nil, fmt.Errorf("pack: missing seat %q", seat)
			}
			if err := validate(cfg); err != nil {
				return nil, fmt.Errorf("pack: seat %q: %w", seat, err)
			}
		}
	}
	return insertPack(db, seats, StatusActive, "")
}

// Active returns the single active pack.
func Active(db *store.DB) (*Pack, error) {
	row, err := db.Active()
	if err != nil {
		return nil, fmt.Errorf("pack: active: %w", err)
	}
	return toPack(row)
}

// Publish creates a new active pack: the new pack becomes active, the
// previous active becomes previous, and any older previous becomes retired.
//
// The status swap and the new row happen in ONE store transaction via
// PublishAtomic, the same transaction the harness publish step reuses to
// add baselines and natural bundles atomically.
func Publish(db *store.DB, seats map[Seat]SeatConfig, fromRunID string) (*Pack, error) {
	if len(seats) != len(AllSeats) {
		return nil, fmt.Errorf("pack: publish needs %d seats, got %d", len(AllSeats), len(seats))
	}
	for _, seat := range AllSeats {
		if _, ok := seats[seat]; !ok {
			return nil, fmt.Errorf("pack: publish missing seat %q", seat)
		}
	}
	seatsJSON, err := json.Marshal(seats)
	if err != nil {
		return nil, fmt.Errorf("pack: encode seats: %w", err)
	}
	var runID *string
	if fromRunID != "" {
		runID = &fromRunID
	}
	res, err := db.PublishAtomic(store.PackTxSeed{
		NewStatus: StatusActive,
		NewSeats:  string(seatsJSON),
		NewHash:   prompts.PromptPackHash(),
		NewRunID:  runID,
	})
	if err != nil {
		return nil, err
	}
	row, err := db.GetPack(res.CreatedID)
	if err != nil {
		return nil, err
	}
	return toPack(row)
}

// Rollback swaps active and previous: previous becomes active, active
// becomes previous. It errors when there is no previous pack.
func Rollback(db *store.DB) (*Pack, error) {
	cur, err := db.Active()
	if err != nil {
		return nil, fmt.Errorf("pack: rollback: %w", err)
	}
	previous, err := db.PacksByStatus(StatusPrevious)
	if err != nil {
		return nil, err
	}
	if len(previous) == 0 {
		return nil, fmt.Errorf("pack: rollback: no previous pack")
	}
	prev := previous[0]
	if err := db.SetStatus(cur.ID, StatusPrevious); err != nil {
		return nil, err
	}
	if err := db.SetStatus(prev.ID, StatusActive); err != nil {
		_ = db.SetStatus(cur.ID, StatusActive)
		return nil, err
	}
	row, err := db.GetPack(prev.ID)
	if err != nil {
		return nil, err
	}
	return toPack(row)
}

// Show returns the pack by id, or the active pack when id == "".
func Show(db *store.DB, id string) (*Pack, error) {
	if id == "" {
		return Active(db)
	}
	row, err := db.GetPack(id)
	if err != nil {
		return nil, fmt.Errorf("pack: show: %w", err)
	}
	return toPack(row)
}
