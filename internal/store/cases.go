package store

import (
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Family is a row in the families table.
type Family struct {
	ID                string
	CreatedAt         string
	FamilyKey         string
	Name              string
	Split             string
	TagsJSON          string
	AcceptanceJSON    string
	PlantedIssuesJSON string
	FamilyHash        string
	SeatsRelevantJSON string
}

const familyCols = `id, created_at, family_key, name, split, tags_json,
	acceptance_json, planted_issues_json, family_hash, seats_relevant_json`

// UpsertFamily inserts a family or updates the existing row with the same
// family_key. It returns the stored row.
func (db *DB) UpsertFamily(f *Family) (*Family, error) {
	if f.FamilyKey == "" {
		return nil, errors.New("store: upsert family: empty family_key")
	}
	existing, err := db.GetFamilyByKey(f.FamilyKey)
	if err == nil {
		_, err = db.db.Exec(
			`UPDATE families SET name=?, split=?, tags_json=?, acceptance_json=?,
			 planted_issues_json=?, family_hash=?, seats_relevant_json=? WHERE id=?`,
			orKeep(existing.Name, f.Name), orKeep(existing.Split, f.Split),
			orKeep(existing.TagsJSON, f.TagsJSON),
			orKeep(existing.AcceptanceJSON, f.AcceptanceJSON),
			orKeep(existing.PlantedIssuesJSON, f.PlantedIssuesJSON),
			orKeep(existing.FamilyHash, f.FamilyHash),
			orKeep(existing.SeatsRelevantJSON, f.SeatsRelevantJSON),
			existing.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("store: update family: %w", err)
		}
		return db.GetFamily(existing.ID)
	}
	row := &Family{
		ID:                ids.NewID(),
		CreatedAt:         nowUTC(),
		FamilyKey:         f.FamilyKey,
		Name:              f.Name,
		Split:             f.Split,
		TagsJSON:          orDefault(f.TagsJSON, "[]"),
		AcceptanceJSON:    orDefault(f.AcceptanceJSON, "{}"),
		PlantedIssuesJSON: orDefault(f.PlantedIssuesJSON, "[]"),
		FamilyHash:        f.FamilyHash,
		SeatsRelevantJSON: orDefault(f.SeatsRelevantJSON, "[]"),
	}
	if _, err := db.db.Exec(
		`INSERT INTO families (`+familyCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.FamilyKey, row.Name, row.Split, row.TagsJSON,
		row.AcceptanceJSON, row.PlantedIssuesJSON, row.FamilyHash, row.SeatsRelevantJSON,
	); err != nil {
		return nil, fmt.Errorf("store: insert family: %w", err)
	}
	return row, nil
}

// GetFamily selects a family by id.
func (db *DB) GetFamily(id string) (*Family, error) {
	row := db.db.QueryRow(`SELECT `+familyCols+` FROM families WHERE id = ?`, id)
	return scanFamily(row)
}

// GetFamilyByKey selects a family by family_key.
func (db *DB) GetFamilyByKey(key string) (*Family, error) {
	row := db.db.QueryRow(`SELECT `+familyCols+` FROM families WHERE family_key = ?`, key)
	return scanFamily(row)
}

// ListFamilies lists all families ordered by key.
func (db *DB) ListFamilies() ([]*Family, error) {
	rows, err := db.db.Query(`SELECT ` + familyCols + ` FROM families ORDER BY family_key`)
	if err != nil {
		return nil, fmt.Errorf("store: list families: %w", err)
	}
	defer rows.Close()
	var out []*Family
	for rows.Next() {
		var f Family
		if err := rows.Scan(&f.ID, &f.CreatedAt, &f.FamilyKey, &f.Name, &f.Split,
			&f.TagsJSON, &f.AcceptanceJSON, &f.PlantedIssuesJSON, &f.FamilyHash,
			&f.SeatsRelevantJSON); err != nil {
			return nil, fmt.Errorf("store: scan family: %w", err)
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

func scanFamily(row packRow) (*Family, error) {
	var f Family
	if err := row.Scan(&f.ID, &f.CreatedAt, &f.FamilyKey, &f.Name, &f.Split,
		&f.TagsJSON, &f.AcceptanceJSON, &f.PlantedIssuesJSON, &f.FamilyHash,
		&f.SeatsRelevantJSON); err != nil {
		return nil, fmt.Errorf("store: scan family: %w", err)
	}
	return &f, nil
}

func orKeep(old, next string) string {
	if next == "" {
		return old
	}
	return next
}

// Case is a row in the cases table.
type Case struct {
	ID             string
	CreatedAt      string
	CaseKey        string
	FamilyID       string
	Variant        string
	InputJSON      string
	InputHash      string
	ExpectedChange *string
}

const caseCols = `id, created_at, case_key, family_id, variant,
	input_json, input_hash, expected_change`

// UpsertCase inserts a case or updates the existing row with the same
// case_key. It returns the stored row.
func (db *DB) UpsertCase(c *Case) (*Case, error) {
	if c.CaseKey == "" {
		return nil, errors.New("store: upsert case: empty case_key")
	}
	existing, err := db.GetCaseByKey(c.CaseKey)
	if err == nil {
		_, err = db.db.Exec(
			`UPDATE cases SET family_id=?, variant=?, input_json=?, input_hash=?,
			 expected_change=? WHERE id=?`,
			orKeep(existing.FamilyID, c.FamilyID), orKeep(existing.Variant, c.Variant),
			orKeep(existing.InputJSON, c.InputJSON), orKeep(existing.InputHash, c.InputHash),
			orKeepPtr(existing.ExpectedChange, c.ExpectedChange), existing.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("store: update case: %w", err)
		}
		return db.GetCase(existing.ID)
	}
	row := &Case{
		ID:             ids.NewID(),
		CreatedAt:      nowUTC(),
		CaseKey:        c.CaseKey,
		FamilyID:       c.FamilyID,
		Variant:        orDefault(c.Variant, "base"),
		InputJSON:      orDefault(c.InputJSON, "{}"),
		InputHash:      c.InputHash,
		ExpectedChange: c.ExpectedChange,
	}
	if _, err := db.db.Exec(
		`INSERT INTO cases (`+caseCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.CaseKey, row.FamilyID, row.Variant,
		row.InputJSON, row.InputHash, row.ExpectedChange,
	); err != nil {
		return nil, fmt.Errorf("store: insert case: %w", err)
	}
	return row, nil
}

// GetCase selects a case by id.
func (db *DB) GetCase(id string) (*Case, error) {
	row := db.db.QueryRow(`SELECT `+caseCols+` FROM cases WHERE id = ?`, id)
	return scanCase(row)
}

// GetCaseByKey selects a case by case_key.
func (db *DB) GetCaseByKey(key string) (*Case, error) {
	row := db.db.QueryRow(`SELECT `+caseCols+` FROM cases WHERE case_key = ?`, key)
	return scanCase(row)
}

// ListCasesByFamily lists cases of one family ordered by key.
func (db *DB) ListCasesByFamily(familyID string) ([]*Case, error) {
	rows, err := db.db.Query(`SELECT `+caseCols+` FROM cases WHERE family_id = ? ORDER BY case_key`, familyID)
	if err != nil {
		return nil, fmt.Errorf("store: list cases: %w", err)
	}
	defer rows.Close()
	var out []*Case
	for rows.Next() {
		var c Case
		if err := rows.Scan(&c.ID, &c.CreatedAt, &c.CaseKey, &c.FamilyID,
			&c.Variant, &c.InputJSON, &c.InputHash, &c.ExpectedChange); err != nil {
			return nil, fmt.Errorf("store: scan case: %w", err)
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func scanCase(row packRow) (*Case, error) {
	var c Case
	if err := row.Scan(&c.ID, &c.CreatedAt, &c.CaseKey, &c.FamilyID,
		&c.Variant, &c.InputJSON, &c.InputHash, &c.ExpectedChange); err != nil {
		return nil, fmt.Errorf("store: scan case: %w", err)
	}
	return &c, nil
}

func orKeepPtr(old, next *string) *string {
	if next == nil {
		return old
	}
	return next
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
