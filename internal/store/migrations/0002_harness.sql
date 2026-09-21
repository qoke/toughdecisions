CREATE TABLE families (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  family_key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL DEFAULT '',
  split TEXT NOT NULL DEFAULT '',
  tags_json TEXT NOT NULL DEFAULT '[]',
  acceptance_json TEXT NOT NULL DEFAULT '{}',
  planted_issues_json TEXT NOT NULL DEFAULT '[]',
  family_hash TEXT NOT NULL DEFAULT '',
  seats_relevant_json TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE cases (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  case_key TEXT NOT NULL UNIQUE,
  family_id TEXT NOT NULL REFERENCES families(id),
  variant TEXT NOT NULL DEFAULT 'base',
  input_json TEXT NOT NULL DEFAULT '{}',
  input_hash TEXT NOT NULL DEFAULT '',
  expected_change TEXT
);
CREATE TABLE bundles (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  bundle_key TEXT NOT NULL UNIQUE,
  case_id TEXT NOT NULL REFERENCES cases(id),
  kind TEXT NOT NULL DEFAULT 'natural',
  views_json TEXT NOT NULL DEFAULT '{}',
  manipulations_json TEXT NOT NULL DEFAULT '[]',
  bundle_hash TEXT NOT NULL DEFAULT '',
  source_pack_id TEXT
);
CREATE TABLE grader_configs (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  grader_key TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  family TEXT NOT NULL DEFAULT '',
  params_json TEXT NOT NULL DEFAULT '{}',
  rubric_hash TEXT NOT NULL DEFAULT '',
  config_hash TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL DEFAULT 'selection',
  admitted INTEGER NOT NULL DEFAULT 0,
  calibration_json TEXT,
  admitted_at TEXT
);
CREATE TABLE calibration_items (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  item_key TEXT NOT NULL UNIQUE,
  case_id TEXT NOT NULL REFERENCES cases(id),
  seat TEXT NOT NULL DEFAULT '',
  response_text TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT '',
  human_scores_json TEXT NOT NULL DEFAULT '{}',
  human_flags_json TEXT NOT NULL DEFAULT '[]',
  notes TEXT NOT NULL DEFAULT ''
);
CREATE TABLE grades (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  cache_key TEXT NOT NULL UNIQUE,
  response_id TEXT NOT NULL,
  grader_config_hash TEXT NOT NULL DEFAULT '',
  rubric_hash TEXT NOT NULL DEFAULT '',
  scores_json TEXT NOT NULL DEFAULT '{}',
  passages_json TEXT NOT NULL DEFAULT '{}',
  weakness_json TEXT,
  flags_json TEXT NOT NULL DEFAULT '[]',
  notes_check_json TEXT NOT NULL DEFAULT '{}',
  raw_json TEXT NOT NULL DEFAULT '{}',
  run_id TEXT
);
CREATE TABLE flags (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  grade_id TEXT NOT NULL,
  response_id TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  passage TEXT NOT NULL DEFAULT '',
  violated TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  resolution_note TEXT,
  run_id TEXT
);
CREATE TABLE pairwise (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  purpose TEXT NOT NULL DEFAULT '',
  case_id TEXT NOT NULL DEFAULT '',
  seat TEXT NOT NULL DEFAULT '',
  left_response_id TEXT NOT NULL DEFAULT '',
  right_response_id TEXT NOT NULL DEFAULT '',
  grader_config_hash TEXT NOT NULL DEFAULT '',
  left_shown_as TEXT NOT NULL DEFAULT 'A',
  verdict TEXT NOT NULL DEFAULT 'unable',
  margin TEXT NOT NULL DEFAULT 'close',
  verdict_left_right TEXT NOT NULL DEFAULT 'unable',
  consequential_difference TEXT NOT NULL DEFAULT '',
  reversal_of_id TEXT,
  final INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE issue_coverage (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  case_id TEXT NOT NULL DEFAULT '',
  council_label TEXT NOT NULL DEFAULT '',
  issue_id TEXT NOT NULL DEFAULT '',
  issue_text TEXT NOT NULL DEFAULT '',
  is_planted INTEGER NOT NULL DEFAULT 0,
  noticed_by_json TEXT NOT NULL DEFAULT '[]',
  unsupported_by_json TEXT NOT NULL DEFAULT '[]',
  judge_outcome TEXT NOT NULL DEFAULT 'na'
);
CREATE TABLE harness_runs (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT '',
  pack_id TEXT NOT NULL DEFAULT '',
  params_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'created',
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT,
  report_path TEXT,
  summary_json TEXT
);
CREATE TABLE sentinel_results (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  seat TEXT NOT NULL DEFAULT '',
  case_id TEXT NOT NULL DEFAULT '',
  fresh_response_id TEXT NOT NULL DEFAULT '',
  baseline_response_id TEXT NOT NULL DEFAULT '',
  verdicts_json TEXT NOT NULL DEFAULT '{}',
  latency_ms INTEGER NOT NULL DEFAULT 0,
  regression INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE baselines (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  pack_id TEXT NOT NULL DEFAULT '',
  seat TEXT NOT NULL DEFAULT '',
  case_id TEXT NOT NULL DEFAULT '',
  bundle_id TEXT,
  response_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE candidates (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  candidate_key TEXT NOT NULL DEFAULT '',
  seat TEXT NOT NULL DEFAULT '',
  config_json TEXT NOT NULL DEFAULT '{}',
  config_hash TEXT NOT NULL DEFAULT '',
  screen_result_json TEXT,
  finalist INTEGER NOT NULL DEFAULT 0,
  compare_result_json TEXT,
  downstream_result_json TEXT,
  promotion_json TEXT
);
CREATE INDEX idx_grades_cache_key ON grades(cache_key);
CREATE INDEX idx_pairwise_run_case ON pairwise(run_id, case_id);
CREATE INDEX idx_flags_status ON flags(status);
