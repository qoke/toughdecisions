CREATE TABLE packs (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  status TEXT NOT NULL,
  seats_json TEXT NOT NULL,
  prompt_pack_hash TEXT NOT NULL,
  notes TEXT NOT NULL DEFAULT '',
  published_from_run_id TEXT,
  activated_at TEXT
);
CREATE TABLE responses (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  cache_key TEXT NOT NULL UNIQUE,
  seat TEXT NOT NULL,
  config_hash TEXT NOT NULL,
  prompt_pack_hash TEXT NOT NULL,
  input_hash TEXT NOT NULL,
  bundle_hash TEXT,
  repetition INTEGER NOT NULL DEFAULT 0,
  origin TEXT NOT NULL,
  request_id TEXT,
  run_id TEXT,
  model_requested TEXT NOT NULL,
  model_returned TEXT NOT NULL DEFAULT '',
  substituted INTEGER NOT NULL DEFAULT 0,
  raw_text TEXT NOT NULL DEFAULT '',
  parsed_json TEXT,
  parse_ok INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  timed_out INTEGER NOT NULL DEFAULT 0,
  error TEXT,
  word_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  archived INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE cards (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  thread_id TEXT NOT NULL REFERENCES threads(id),
  version INTEGER NOT NULL,
  card_json TEXT NOT NULL,
  card_hash TEXT NOT NULL
);
CREATE TABLE requests (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  thread_id TEXT NOT NULL REFERENCES threads(id),
  card_id TEXT NOT NULL REFERENCES cards(id),
  pack_id TEXT NOT NULL REFERENCES packs(id),
  snapshot_json TEXT NOT NULL,
  input_hash TEXT NOT NULL,
  state TEXT NOT NULL,
  supersedes_request_id TEXT,
  superseded_by_request_id TEXT,
  views_deadline_at TEXT NOT NULL,
  judge_started_at TEXT,
  judge_deadline_at TEXT,
  finished_at TEXT,
  t_first_usable_view_ms INTEGER,
  t_final_ms INTEGER,
  views_completed_in_deadline INTEGER NOT NULL DEFAULT 0,
  judge_completed_in_deadline INTEGER,
  danger_flagged INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE request_views (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  request_id TEXT NOT NULL REFERENCES requests(id),
  seat TEXT NOT NULL,
  response_id TEXT,
  state TEXT NOT NULL,
  included_in_judge INTEGER NOT NULL DEFAULT 0,
  completed_at TEXT
);
CREATE TABLE request_judge (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  request_id TEXT NOT NULL REFERENCES requests(id),
  response_id TEXT,
  state TEXT NOT NULL,
  included_seats_json TEXT NOT NULL DEFAULT '[]',
  missing_seats_json TEXT NOT NULL DEFAULT '[]',
  no_independent_views INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE rewrites (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  request_id TEXT NOT NULL REFERENCES requests(id),
  source_ref TEXT NOT NULL,
  instruction TEXT NOT NULL,
  response_id TEXT NOT NULL,
  output_text TEXT NOT NULL
);
CREATE TABLE sent_messages (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  request_id TEXT NOT NULL REFERENCES requests(id),
  thread_id TEXT NOT NULL REFERENCES threads(id),
  text TEXT NOT NULL,
  source_ref TEXT,
  sent_at TEXT NOT NULL
);
CREATE TABLE feedback (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  request_id TEXT NOT NULL REFERENCES requests(id),
  tag TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_responses_cache_key ON responses(cache_key);
CREATE INDEX idx_requests_thread_created ON requests(thread_id, created_at);
CREATE INDEX idx_feedback_created ON feedback(created_at);
