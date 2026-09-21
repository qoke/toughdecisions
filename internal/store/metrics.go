package store

import (
	"fmt"
)

// ViewStateCounts counts request_views rows created at or after since, by state.
func (db *DB) ViewStateCounts(since string) (map[string]int, error) {
	return countByState(db, `SELECT state, COUNT(*) FROM request_views WHERE created_at >= ? GROUP BY state`, since, "view state counts")
}

// JudgeStateCounts counts request_judge rows created at or after since, by state.
func (db *DB) JudgeStateCounts(since string) (map[string]int, error) {
	return countByState(db, `SELECT state, COUNT(*) FROM request_judge WHERE created_at >= ? GROUP BY state`, since, "judge state counts")
}

// TimeoutCounts counts timed-out responses created at or after since, by seat.
func (db *DB) TimeoutCounts(since string) (map[string]int, error) {
	rows, err := db.db.Query(`SELECT seat, COUNT(*) FROM responses WHERE created_at >= ? AND timed_out = 1 GROUP BY seat`, since)
	if err != nil {
		return nil, fmt.Errorf("store: timeout counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var seat string
		var n int
		if err := rows.Scan(&seat, &n); err != nil {
			return nil, fmt.Errorf("store: scan timeout count: %w", err)
		}
		out[seat] = n
	}
	return out, rows.Err()
}

// RequestTimings returns t_first_usable_view_ms and t_final_ms values for
// requests created at or after since (NULLs excluded), plus the request count.
func (db *DB) RequestTimings(since string) (firstUsable, finals []int64, count int, err error) {
	row := db.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE created_at >= ?`, since)
	if err := row.Scan(&count); err != nil {
		return nil, nil, 0, fmt.Errorf("store: request count: %w", err)
	}
	collect := func(col string) ([]int64, error) {
		rows, err := db.db.Query(`SELECT `+col+` FROM requests WHERE created_at >= ? AND `+col+` IS NOT NULL`, since)
		if err != nil {
			return nil, fmt.Errorf("store: request timings: %w", err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				return nil, fmt.Errorf("store: scan timing: %w", err)
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	firstUsable, err = collect("t_first_usable_view_ms")
	if err != nil {
		return nil, nil, 0, err
	}
	finals, err = collect("t_final_ms")
	if err != nil {
		return nil, nil, 0, err
	}
	return firstUsable, finals, count, nil
}

func countByState(db *DB, query, since, what string) (map[string]int, error) {
	rows, err := db.db.Query(query, since)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("store: scan %s: %w", what, err)
		}
		out[state] = n
	}
	return out, rows.Err()
}
