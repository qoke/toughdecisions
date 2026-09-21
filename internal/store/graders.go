package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// GraderConfig is a row in the grader_configs table.
type GraderConfig struct {
	ID              string
	CreatedAt       string
	GraderKey       string
	Model           string
	Family          string
	ParamsJSON      string
	RubricHash      string
	ConfigHash      string
	Role            string
	Admitted        bool
	CalibrationJSON *string
	AdmittedAt      *string
}

const graderCols = `id, created_at, grader_key, model, family, params_json,
	rubric_hash, config_hash, role, admitted, calibration_json, admitted_at`

// UpsertGraderConfig inserts a grader config or updates the row with the same
// config_hash (a new config_hash means a new grader row that starts
// unadmitted). It returns the stored row.
func (db *DB) UpsertGraderConfig(g *GraderConfig) (*GraderConfig, error) {
	if g.ConfigHash == "" {
		return nil, errors.New("store: upsert grader config: empty config_hash")
	}
	existing, err := db.GetGraderConfig(g.ConfigHash)
	if err == nil {
		_, err = db.db.Exec(
			`UPDATE grader_configs SET grader_key=?, model=?, family=?, params_json=?,
			 rubric_hash=?, role=? WHERE id=?`,
			orKeep(existing.GraderKey, g.GraderKey), orKeep(existing.Model, g.Model),
			orKeep(existing.Family, g.Family), orKeep(existing.ParamsJSON, g.ParamsJSON),
			orKeep(existing.RubricHash, g.RubricHash), orKeep(existing.Role, g.Role),
			existing.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("store: update grader config: %w", err)
		}
		return db.GetGraderConfig(g.ConfigHash)
	}
	row := &GraderConfig{
		ID:         ids.NewID(),
		CreatedAt:  nowUTC(),
		GraderKey:  g.GraderKey,
		Model:      g.Model,
		Family:     g.Family,
		ParamsJSON: orDefault(g.ParamsJSON, "{}"),
		RubricHash: g.RubricHash,
		ConfigHash: g.ConfigHash,
		Role:       orDefault(g.Role, "selection"),
	}
	if _, err := db.db.Exec(
		`INSERT INTO grader_configs (`+graderCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, NULL)`,
		row.ID, row.CreatedAt, row.GraderKey, row.Model, row.Family,
		row.ParamsJSON, row.RubricHash, row.ConfigHash, row.Role,
	); err != nil {
		return nil, fmt.Errorf("store: insert grader config: %w", err)
	}
	return row, nil
}

// GetGraderConfig selects a grader config by config_hash.
func (db *DB) GetGraderConfig(configHash string) (*GraderConfig, error) {
	row := db.db.QueryRow(`SELECT `+graderCols+` FROM grader_configs WHERE config_hash = ?`, configHash)
	return scanGrader(row)
}

// ListGraderConfigs lists all grader configs ordered by key.
func (db *DB) ListGraderConfigs() ([]*GraderConfig, error) {
	rows, err := db.db.Query(`SELECT ` + graderCols + ` FROM grader_configs ORDER BY grader_key, config_hash`)
	if err != nil {
		return nil, fmt.Errorf("store: list grader configs: %w", err)
	}
	defer rows.Close()
	var out []*GraderConfig
	for rows.Next() {
		var g GraderConfig
		var admitted int
		if err := rows.Scan(&g.ID, &g.CreatedAt, &g.GraderKey, &g.Model, &g.Family,
			&g.ParamsJSON, &g.RubricHash, &g.ConfigHash, &g.Role, &admitted,
			&g.CalibrationJSON, &g.AdmittedAt); err != nil {
			return nil, fmt.Errorf("store: scan grader config: %w", err)
		}
		g.Admitted = admitted != 0
		out = append(out, &g)
	}
	return out, rows.Err()
}

// SetGraderCalibration stores calibration_json and the admission decision.
func (db *DB) SetGraderCalibration(configHash, calibrationJSON string, admitted bool, admittedAt *string) error {
	res, err := db.db.Exec(
		`UPDATE grader_configs SET calibration_json=?, admitted=?, admitted_at=? WHERE config_hash=?`,
		calibrationJSON, boolInt(admitted), admittedAt, configHash,
	)
	if err != nil {
		return fmt.Errorf("store: set grader calibration: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set grader calibration rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: grader config %s not found: %w", configHash, sql.ErrNoRows)
	}
	return nil
}

// CalibrationItem is a row in the calibration_items table.
type CalibrationItem struct {
	ID              string
	CreatedAt       string
	ItemKey         string
	CaseID          string
	Seat            string
	ResponseText    string
	Category        string
	HumanScoresJSON string
	HumanFlagsJSON  string
	Notes           string
}

const calibrationCols = `id, created_at, item_key, case_id, seat, response_text,
	category, human_scores_json, human_flags_json, notes`

// UpsertCalibrationItem inserts an item or updates the row with the same
// item_key. It returns the stored row.
func (db *DB) UpsertCalibrationItem(it *CalibrationItem) (*CalibrationItem, error) {
	if it.ItemKey == "" {
		return nil, errors.New("store: upsert calibration item: empty item_key")
	}
	existing, err := db.GetCalibrationItemByKey(it.ItemKey)
	if err == nil {
		_, err = db.db.Exec(
			`UPDATE calibration_items SET case_id=?, seat=?, response_text=?, category=?,
			 human_scores_json=?, human_flags_json=?, notes=? WHERE id=?`,
			orKeep(existing.CaseID, it.CaseID), orKeep(existing.Seat, it.Seat),
			orKeep(existing.ResponseText, it.ResponseText),
			orKeep(existing.Category, it.Category),
			orKeep(existing.HumanScoresJSON, it.HumanScoresJSON),
			orKeep(existing.HumanFlagsJSON, it.HumanFlagsJSON),
			orKeep(existing.Notes, it.Notes), existing.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("store: update calibration item: %w", err)
		}
		return db.GetCalibrationItem(existing.ID)
	}
	row := &CalibrationItem{
		ID:              ids.NewID(),
		CreatedAt:       nowUTC(),
		ItemKey:         it.ItemKey,
		CaseID:          it.CaseID,
		Seat:            it.Seat,
		ResponseText:    it.ResponseText,
		Category:        it.Category,
		HumanScoresJSON: orDefault(it.HumanScoresJSON, "{}"),
		HumanFlagsJSON:  orDefault(it.HumanFlagsJSON, "[]"),
		Notes:           it.Notes,
	}
	if _, err := db.db.Exec(
		`INSERT INTO calibration_items (`+calibrationCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.ItemKey, row.CaseID, row.Seat, row.ResponseText,
		row.Category, row.HumanScoresJSON, row.HumanFlagsJSON, row.Notes,
	); err != nil {
		return nil, fmt.Errorf("store: insert calibration item: %w", err)
	}
	return row, nil
}

// GetCalibrationItem selects an item by id.
func (db *DB) GetCalibrationItem(id string) (*CalibrationItem, error) {
	row := db.db.QueryRow(`SELECT `+calibrationCols+` FROM calibration_items WHERE id = ?`, id)
	return scanCalibrationItem(row)
}

// GetCalibrationItemByKey selects an item by item_key.
func (db *DB) GetCalibrationItemByKey(key string) (*CalibrationItem, error) {
	row := db.db.QueryRow(`SELECT `+calibrationCols+` FROM calibration_items WHERE item_key = ?`, key)
	return scanCalibrationItem(row)
}

// ListCalibrationItems lists all items ordered by key.
func (db *DB) ListCalibrationItems() ([]*CalibrationItem, error) {
	rows, err := db.db.Query(`SELECT ` + calibrationCols + ` FROM calibration_items ORDER BY item_key`)
	if err != nil {
		return nil, fmt.Errorf("store: list calibration items: %w", err)
	}
	defer rows.Close()
	var out []*CalibrationItem
	for rows.Next() {
		var it CalibrationItem
		if err := rows.Scan(&it.ID, &it.CreatedAt, &it.ItemKey, &it.CaseID, &it.Seat,
			&it.ResponseText, &it.Category, &it.HumanScoresJSON, &it.HumanFlagsJSON,
			&it.Notes); err != nil {
			return nil, fmt.Errorf("store: scan calibration item: %w", err)
		}
		out = append(out, &it)
	}
	return out, rows.Err()
}

func scanGrader(row packRow) (*GraderConfig, error) {
	var g GraderConfig
	var admitted int
	if err := row.Scan(&g.ID, &g.CreatedAt, &g.GraderKey, &g.Model, &g.Family,
		&g.ParamsJSON, &g.RubricHash, &g.ConfigHash, &g.Role, &admitted,
		&g.CalibrationJSON, &g.AdmittedAt); err != nil {
		return nil, fmt.Errorf("store: scan grader config: %w", err)
	}
	g.Admitted = admitted != 0
	return &g, nil
}

func scanCalibrationItem(row packRow) (*CalibrationItem, error) {
	var it CalibrationItem
	if err := row.Scan(&it.ID, &it.CreatedAt, &it.ItemKey, &it.CaseID, &it.Seat,
		&it.ResponseText, &it.Category, &it.HumanScoresJSON, &it.HumanFlagsJSON,
		&it.Notes); err != nil {
		return nil, fmt.Errorf("store: scan calibration item: %w", err)
	}
	return &it, nil
}
