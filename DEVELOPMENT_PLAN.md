Two notes before the plan:

- The attached spec is the **1c** variant (no screener, Go/LiteLLM/n8n). The plan follows it exactly; nothing from 1d is added except a one-time bootstrap command sequence, which is operational, not architectural.
- I could not verify the public API of `github.com/iqhive/cfggo` or `github.com/iqhive/iqlog` (no indexable docs). The plan isolates both behind thin internal wrappers and instructs the agent to read `go doc` before coding them, so nothing else in the codebase depends on their exact signatures.

Save the following as `DEVELOPMENT_PLAN.md`.

---

# DEVELOPMENT_PLAN.md — Relationship Council (Go)

## 0. Read this first

- **Source of truth for behaviour:** `PRODUCT_SPEC.md`. This plan fixes every architectural and data-model decision so you only write code. Where the spec is silent, this plan chooses a default and marks it **⚙** — implement it as a config value with that default.
- **Two systems, one binary** (`council`): the **production system** (`council serve`) and the **harness** (`council harness …`, `council pack …`, `council graders …`). They share storage, the gateway client, prompt assembly, and the published **production pack**. Production reads the active pack; only the harness writes packs.
- **Hard rules from the spec that must be visible in code:**
  1. No fallbacks, retries, or silent model substitution in production or harness. A failed/timed-out call is recorded as absent; a substituted model is treated as a failure.
  2. Unsupported sampling/reasoning settings are **rejected** at pack validation and at call time, never dropped.
  3. Views run in parallel; the judge starts when all views finish **or** the views deadline expires, whichever is first. Judge timeout → completed views stay available; no retry.
  4. Every production answer is tied to an immutable input snapshot and an immutable pack id.
  5. Graders never see model identities or prior results. Pairwise order is randomised; close/tie results are re-run reversed.
  6. Never compute cross-grader or cross-run "lifetime averages". Reports are per run, per grader, per family.
- **Library verification (do this before Phase 0):** run `go doc github.com/iqhive/cfggo` and `go doc github.com/iqhive/iqlog`. Implement `internal/config` and `internal/logx` against their real APIs. All other packages depend only on `internal/config.Config` and `internal/logx.Logger`.

## 1. Stack and dependencies

| Concern | Choice |
|---|---|
| Go | 1.23+, modules, `gofmt`, `go vet` clean, no cgo |
| Config | `github.com/iqhive/cfggo` |
| Logging | `github.com/iqhive/iqlog` (behind `internal/logx`) |
| Storage | SQLite via `modernc.org/sqlite` (driver name `sqlite`), WAL mode, single file |
| YAML | `gopkg.in/yaml.v3` (case pack, pack definitions, graders, model capabilities) |
| IDs | `github.com/oklog/ulid/v2` (sortable) |
| HTTP | stdlib `net/http` (Go 1.22 method+path patterns), SSE via `http.Flusher` |
| LLM access | LiteLLM proxy, OpenAI-compatible `POST {base}/v1/chat/completions`, plain `net/http` client (no SDK) |
| Embedding | `embed` for prompt files, SQL migrations, and the single-page UI |

No other third-party dependencies without a comment justifying them.

## 2. Repository layout

```
cmd/council/main.go                 CLI entry: subcommand dispatch (stdlib flag)
internal/config/config.go           cfggo struct + Load()
internal/logx/logx.go               Logger interface + iqlog implementation
internal/ids/ids.go                 NewID() ULID
internal/hash/hash.go               CanonicalJSON(any) []byte; SHA256Hex(...[]byte) string
internal/store/db.go                Open(path) *DB; migrations runner; tx helpers
internal/store/migrations/NNNN_*.sql
internal/store/*.go                 one file per aggregate: packs.go, threads.go, requests.go,
                                    casepack.go, responses.go, grades.go, pairwise.go,
                                    coverage.go, runs.go, graders.go, feedback.go
internal/gateway/client.go          Client interface, ChatRequest/ChatResponse, errors
internal/gateway/litellm.go         HTTP implementation
internal/gateway/fake.go            scripted fake for tests
internal/prompts/files/…            embedded prompt text (see §6)
internal/prompts/assemble.go        Assembler (pure), PromptPackHash()
internal/schema/                    Go structs + JSON Schemas for view/judge/grader outputs; lenient parser
internal/models/capabilities.go     models.yaml loader; ValidateSettings()
internal/pack/pack.go               Pack, SeatConfig, load/validate/publish/rollback
internal/render/render.go           parsed JSON → uniform Markdown (blind rendering for graders and UI)
internal/council/run.go             production orchestration (state machine, deadlines, events)
internal/council/registry.go        in-memory live runs + subscriber fan-out
internal/httpapi/server.go          routes, handlers, SSE
internal/httpapi/ui/index.html      embedded single-page UI
internal/casepack/loader.go         YAML → structs, validation, content hashes
internal/grading/absolute.go        0–4 rubric grades + flags
internal/grading/pairwise.go        blind A/B with reversal
internal/grading/coverage.go        issue-coverage grader
internal/grading/calibration.go     grader admission / weekly recheck
internal/harness/sentinel.go        step 1
internal/harness/screen.go          step 2
internal/harness/compare.go         step 4
internal/harness/downstream.go      step 5
internal/harness/promotion.go       checklist evaluation
internal/harness/report.go          Markdown report + webhook notify
internal/harness/weekly.go          orchestration of steps
casepack/                           user-authored YAML (not embedded); ship examples/
config/pack.yaml, config/graders.yaml, config/models.yaml, config/candidates.yaml
reports/                            generated
```

## 3. Cross-cutting conventions

### 3.1 Configuration (`internal/config`)

Define `Config` with cfggo. Canonical keys (snake_case; cfggo maps env/flags per its conventions):

| Key | Type | Default | Notes |
|---|---|---|---|
| `server_listen` | string | `:8080` | |
| `db_path` | string | `./data/council.db` | |
| `gateway_base_url` | string | `http://localhost:4000` | LiteLLM |
| `gateway_api_key` | string (secret) | `""` | |
| `gateway_max_concurrent` | int | 8 | semaphore across all calls |
| `views_deadline` | duration | `30s` | spec |
| `judge_deadline` | duration | `30s` | spec |
| `rewrite_deadline` | duration | `20s` ⚙ | |
| `rewrite_seat` | string | `judge` ⚙ | which pack seat's model does rewrites |
| `casepack_dir` | string | `./casepack` | |
| `pack_file` | string | `./config/pack.yaml` | initial pack only |
| `graders_file` | string | `./config/graders.yaml` | |
| `models_file` | string | `./config/models.yaml` | capabilities |
| `candidates_file` | string | `./config/candidates.yaml` | |
| `reports_dir` | string | `./reports` | |
| `notify_webhook_url` | string | `""` | n8n webhook; empty = skip |
| `harness_concurrency` | int | 3 ⚙ | equals production parallel load |
| `harness_sentinel_count` | int | 4 | spec |
| `harness_screen_cases` | int | 6 | spec |
| `harness_calibration_recheck` | int | 3 ⚙ | items per grader per week |
| `harness_block_on_grader_drift` | bool | true ⚙ | |
| `promo_role_min_mean` | float | 3.0 ⚙ | role_execution mean over selection cases |
| `promo_role_min_floor` | int | 2 ⚙ | role_execution min |
| `promo_speed_ratio` | float | 0.8 ⚙ | "materially better speed" = p50 ≤ ratio × incumbent |
| `admit_max_reversals` | int | 1 ⚙ | grader admission |
| `retention_production_days` | int | 90 ⚙ | |
| `log_level` | string | `info` | |
| `log_redact_content` | bool | true | never log card/message/response text above debug |

`Load()` returns `(*Config, error)`; fail fast on invalid values. Print effective config (secrets redacted) at startup at debug level.

### 3.2 Logging (`internal/logx`)

```go
type Logger interface {
    Debug(msg string, kv ...any); Info(msg string, kv ...any)
    Warn(msg string, kv ...any);  Error(msg string, kv ...any)
    With(kv ...any) Logger
}
func New(cfg *config.Config) Logger   // wraps iqlog
```
Standard fields: `component`, `request_id` / `run_id`, `seat`, `model`, `latency_ms`, `err`. Content fields (`card`, `messages`, `response_text`) only at debug and only when `log_redact_content=false`.

### 3.3 IDs, hashing, errors

- All primary keys are ULID strings.
- `hash.CanonicalJSON` = JSON with sorted keys, no whitespace; all content hashes are `sha256` hex of canonical JSON.
- Sentinel errors in `gateway`: `ErrTimeout`, `ErrSubstituted`, `ErrUnsupportedSetting`, `ErrBadJSON`, `ErrGateway`. Wrap with `%w`. Callers branch with `errors.Is`.

## 4. Data model and SQLite schema

Timestamps are RFC3339 UTC text. JSON columns are TEXT holding canonical JSON. Every table has `id TEXT PRIMARY KEY` and `created_at`. Migrations are numbered SQL files applied in order and recorded in `schema_migrations`.

**Shared**

| Table | Columns (beyond id/created_at) |
|---|---|
| `packs` | `status` (draft/active/previous/retired), `seats_json` (4 SeatConfig), `prompt_pack_hash`, `notes`, `published_from_run_id` NULL, `activated_at` NULL |
| `responses` | `cache_key` UNIQUE, `seat`, `config_hash`, `prompt_pack_hash`, `input_hash`, `bundle_hash` NULL, `repetition` INT, `origin` (production/harness), `request_id` NULL, `run_id` NULL, `model_requested`, `model_returned`, `substituted` BOOL, `raw_text`, `parsed_json` NULL, `parse_ok` BOOL, `prompt_tokens`, `completion_tokens`, `cost_usd` NULL, `latency_ms`, `timed_out` BOOL, `error` NULL, `word_count` |

**Production**

| Table | Columns |
|---|---|
| `threads` | `name`, `archived` BOOL |
| `cards` | `thread_id`, `version` INT, `card_json` (7 fields: decision, context, priorities, unusual, history, deadline, style), `card_hash` |
| `requests` | `thread_id`, `card_id`, `pack_id`, `snapshot_json` (card + messages + question + style), `input_hash`, `state` (created/running_views/running_judge/complete/complete_partial/superseded/failed), `supersedes_request_id` NULL, `superseded_by_request_id` NULL, `views_deadline_at`, `judge_started_at` NULL, `judge_deadline_at` NULL, `finished_at` NULL, `t_first_usable_view_ms` NULL, `t_final_ms` NULL, `views_completed_in_deadline` INT, `judge_completed_in_deadline` BOOL NULL, `danger_flagged` BOOL |
| `request_views` | `request_id`, `seat`, `response_id` NULL, `state` (pending/complete/timed_out/failed/late), `included_in_judge` BOOL, `completed_at` NULL |
| `request_judge` | `request_id`, `response_id` NULL, `state` (pending/complete/timed_out/failed), `included_seats_json`, `missing_seats_json`, `no_independent_views` BOOL |
| `rewrites` | `request_id`, `source_ref` (view:<seat>/judge/rewrite:<id>), `instruction`, `response_id`, `output_text` |
| `sent_messages` | `request_id`, `thread_id`, `text`, `source_ref` NULL, `sent_at` |
| `feedback` | `request_id`, `tag` (missed_constraint/invented_motive/wrong_tone/unhelpful_repetition/too_slow/other), `note` |

**Harness**

| Table | Columns |
|---|---|
| `families` | `family_key` UNIQUE (from YAML), `name`, `split`, `tags_json`, `acceptance_json`, `planted_issues_json`, `family_hash`, `seats_relevant_json` |
| `cases` | `case_key` UNIQUE, `family_id`, `variant`, `input_json` (card+messages+question), `input_hash`, `expected_change` |
| `bundles` | `bundle_key` UNIQUE, `case_id`, `kind` (natural/authored), `views_json` (3 rendered views by seat), `manipulations_json`, `bundle_hash`, `source_pack_id` NULL |
| `grader_configs` | `grader_key`, `model`, `family`, `params_json`, `rubric_hash`, `config_hash` UNIQUE, `role` (selection/screening/substitute), `admitted` BOOL, `calibration_json` NULL, `admitted_at` NULL |
| `calibration_items` | `item_key` UNIQUE, `case_id`, `seat`, `response_text`, `category`, `human_scores_json`, `human_flags_json`, `notes` |
| `grades` | `cache_key` UNIQUE, `response_id`, `grader_config_hash`, `rubric_hash`, `scores_json`, `passages_json`, `weakness_json` NULL, `flags_json`, `notes_check_json`, `raw_json`, `run_id` |
| `flags` | `grade_id`, `response_id`, `type`, `passage`, `violated`, `status` (open/confirmed/dismissed), `resolution_note` NULL, `run_id` |
| `pairwise` | `run_id`, `purpose` (sentinel/compare/downstream), `case_id`, `seat`, `left_response_id`, `right_response_id`, `grader_config_hash`, `left_shown_as` (A/B), `verdict` (A/B/tie/unable), `margin` (clear/close), `verdict_left_right` (left/right/tie/unable — normalised), `consequential_difference`, `reversal_of_id` NULL, `final` BOOL |
| `issue_coverage` | `run_id`, `case_id`, `council_label` (incumbent/candidate:<id>), `issue_id`, `issue_text`, `is_planted` BOOL, `noticed_by_json`, `unsupported_by_json`, `judge_outcome` (preserved/resolved/dropped/na) |
| `harness_runs` | `kind`, `pack_id`, `params_json`, `status`, `started_at`, `finished_at`, `report_path` NULL, `summary_json` NULL |
| `sentinel_results` | `run_id`, `seat`, `case_id`, `fresh_response_id`, `baseline_response_id`, `verdicts_json` (per grader), `latency_ms`, `regression` BOOL |
| `baselines` | `pack_id`, `seat`, `case_id`, `bundle_id` NULL, `response_id` — qualification outputs cached at publish |
| `candidates` | `run_id`, `candidate_key`, `seat`, `config_json`, `config_hash`, `screen_result_json` NULL, `finalist` BOOL, `compare_result_json` NULL, `downstream_result_json` NULL, `promotion_json` NULL |

Indexes: `responses(cache_key)`, `grades(cache_key)`, `requests(thread_id, created_at)`, `pairwise(run_id, case_id)`, `flags(status)`.

## 5. Gateway client (`internal/gateway`)

```go
type Message struct{ Role, Content string }            // "system" | "user"
type ResponseFormat struct{ Type string; SchemaName string; Schema json.RawMessage; Strict bool }
type ChatRequest struct {
    Model string; Messages []Message
    Temperature, TopP *float64; MaxOutputTokens int; ReasoningEffort string
    ResponseFormat *ResponseFormat
    ExpectedModelPrefixes []string   // from models.yaml; empty = skip substitution check
    Tags map[string]string           // request_id/run_id/seat → LiteLLM metadata (optional)
}
type ChatResponse struct {
    ModelReturned, Content, FinishReason string
    PromptTokens, CompletionTokens int; CostUSD *float64; LatencyMs int64
}
type Client interface{ Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) }
```

Implementation rules:
- Single `POST /v1/chat/completions`, `Authorization: Bearer`. Non-streaming. Deadline comes only from `ctx`; `http.Client.Timeout` unset. Map `context.DeadlineExceeded` → `ErrTimeout`.
- Only include `temperature`/`top_p`/`reasoning_effort` in the body when non-nil/non-empty. `max_completion_tokens` always set. Never send `fallbacks`, `num_retries`, or `drop_params`.
- `response_format`: `json_schema` (strict) when the seat's model has `supports_json_schema`; else `json_object` when `supports_json_object`; else none (schema instruction lives in the prompt regardless).
- Substitution check: if `ExpectedModelPrefixes` non-empty and `response.model` matches none (case-insensitive prefix), return `ErrSubstituted` with the response attached (store it for audit, mark `substituted=true`).
- Capture `usage` and header `x-litellm-response-cost` (float) if present.
- Global semaphore of `gateway_max_concurrent`.
- Log one line per call: model, latency_ms, tokens, cost, err class. No content.

`fake.go`: `Fake` with per-model scripted responses `[]Step{Delay, Content, ModelReturned, Err}` and call recording, for all orchestration tests.

**Ops note (README):** LiteLLM proxy aliases used by the council must have no `fallbacks`, `num_retries: 0`, and `drop_params` disabled. Pin provider snapshot ids in the alias mapping.

## 6. Prompt pack and assembly (`internal/prompts`, `internal/schema`)

Embedded files (`internal/prompts/files/`):

```
shared_instructions.md           verbatim from spec
roles/possibility.md             verbatim
roles/perspective.md             verbatim
roles/stress_tester.md           verbatim
roles/judge.md                   verbatim
contracts/view.md                spec view contract + "return JSON matching schema; targets are words, not filler"
contracts/judge.md               spec judge contract + audit checklist (spec §"judge's audit") + JSON instruction
context_format.md                template: Case card (7 fields) / Messages (verbatim, sender+ts) / Current request /
                                 [judge only] Independent views by role / Missing roles / "No independent views" notice
rewrite.md                       one-call rewrite: preserve meaning, commitments, boundaries; apply instruction
graders/absolute.md              rubric (5 criteria, 0–4 anchors), flag types, "weakness only if one exists",
                                 acceptance notes are constraints not a prescribed answer; credit insight beyond notes
graders/pairwise.md              A/B; must name a consequential difference; forbid prose/length/agreeableness;
                                 verdict A|B|tie|unable + margin clear|close
graders/coverage.md              issue-coverage table instructions
graders/calibration.md           same as absolute (reuse), used for admission
```

`PromptPackHash()` = sha256 over all files under `files/` except `graders/*`. `RubricHash()` = sha256 over `graders/*`. Both computed once at startup and stored on every response/grade.

Assembler (pure functions; golden-tested):
```go
func BuildView(seat Seat, in CaseInput) []gateway.Message
func BuildJudge(in CaseInput, views map[Seat]RenderedView, missing []Seat, noViews bool) []gateway.Message
func BuildRewrite(draft, instruction string, in CaseInput) []gateway.Message
func BuildAbsoluteGrade(in CaseInput, acceptance Acceptance, seat Seat, rendered string) []gateway.Message
func BuildPairwise(in CaseInput, acceptance Acceptance, seat Seat, a, b string) []gateway.Message
func BuildCoverage(in CaseInput, planted []Issue, views map[Seat]string, judge string) []gateway.Message
```
System message = shared instructions + role prompt + contract. User message = rendered `context_format`. Views are labelled by **role name only**; model names never appear in any prompt.

Output JSON structs (in `internal/schema`, with generated JSON Schema constants):

```go
type Danger struct{ Present bool; Caution string }
type View struct {
    UrgentDanger Danger; Qualification string
    SuggestedReply string; RecommendedMove string
    DecisiveInsight, TradeoffOrObjection, DependsOn, Fallback string
}
type Judge struct {
    UrgentDanger Danger; NoIndependentViews bool; MissingRoles []string; Qualification string
    RecommendedReply, RecommendedAction, Why, AcceptedCost string
    Next struct{ Immediate, Forward string }
    ChangeCourseIf, UnresolvedDisagreement string
}
type AbsoluteGrade struct {
    Scores map[string]int // grounding_calibration, context_values_fidelity, decision_insight, practical_robustness, role_execution
    SupportingPassages map[string]string
    MostConsequentialWeakness *struct{ Passage, Explanation string }
    Flags []struct{ Type, Passage, Violated string } // fabrication|ignored_danger_or_impossibility|coercive|invented_commitment|values_substitution|misrepresentation
    NotesCheck struct{ Noticed, Missed, BeyondNotes []string }
}
type Pairwise struct{ Verdict, Margin, ConsequentialDifference, Notes string }
type Coverage struct{ Rows []struct{ IssueID, IssueText string; IsPlanted bool; NoticedBy, UnsupportedBy []string; JudgeOutcome string } }
```
`schema.ParseLenient[T](raw string) (T, bool)`: strip code fences, take the first balanced `{…}`, `json.Unmarshal`, validate required fields/ranges. Views/judge: on parse failure store raw text, `parse_ok=false`, still render raw to the UI. Graders: on parse failure retry **once** with an appended "Return only the JSON object" user message; second failure → grade row with `error`, counted in the report.

## 7. Production pack and model capabilities (`internal/pack`, `internal/models`)

```go
type Seat string // "possibility" | "perspective" | "stress_tester" | "judge"
type SeatConfig struct {
    Seat Seat; Model string; Family string
    Temperature, TopP *float64; ReasoningEffort string; MaxOutputTokens int  // defaults ⚙: views 2000, judge 3000
    RolePromptOverride string // empty = embedded role file
}
func (s SeatConfig) Hash() string // canonical JSON of all fields + role prompt text actually used
type Pack struct{ ID string; Seats map[Seat]SeatConfig; PromptPackHash string; Status string }
```

`config/models.yaml`:
```yaml
models:
  - id: council-gpt-x            # LiteLLM alias
    family: openai
    expected_response_model_prefixes: ["gpt-x-2026"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
```
`models.ValidateSettings(SeatConfig) error` returns `ErrUnsupportedSetting` naming the field if a set parameter is unsupported or the model is unknown. Called on `pack init/publish`, on candidate load, and defensively before every call.

Pack operations: `Init(file)` → pack v1 `active`. `Publish(newSeats, fromRunID)` → new pack `active`, previous active → `previous`, older `previous` → `retired`; then ensure baselines (§12.7). `Rollback()` → swap `active`/`previous`. `Active()` read once per request and pinned on `requests.pack_id`.

## 8. Production pipeline (`internal/council`)

`Run(ctx, req CreateRequest) (requestID string, err error)` — returns immediately; work continues in a goroutine owned by the registry.

Sequence:
1. Load active pack; build snapshot (card version + messages verbatim + question + style); `input_hash`; insert `requests` (state `created`), 3 `request_views` (pending), `request_judge` (pending).
2. If `supersedes_request_id` set: mark that request `superseded`, set `superseded_by`, cancel its context (registry holds cancel funcs). Its completed outputs remain.
3. `viewsCtx, cancel := context.WithDeadline(ctx, now+views_deadline)`. Launch 3 goroutines, one per view seat. Each: validate settings → `BuildView` → `Chat` → store `responses` (origin production, cache bypass) → parse → update `request_views` → publish event `view_complete` (or `view_failed` with reason timeout/error/substituted). If `UrgentDanger.Present` → event `danger`. Record `t_first_usable_view_ms` on first `complete` with `parse_ok` or raw text non-empty.
4. Judge trigger: `select` on (all three views terminal) **or** `viewsCtx.Done()`. Then `judge_started_at`, included = completed views, missing = the rest. `noViews = len(included)==0` → prompt uses the "no independent views" notice and the judge output is stored with `no_independent_views=true`. Event `judge_started`.
5. `judgeCtx` = `context.WithDeadline(ctx, now+judge_deadline)`. One call. Success → `judge_complete`, state `complete` (or `complete_partial` if any view missing). Timeout/error → `judge_failed`, state `complete_partial`. **No retry.**
6. Views completing after step 4 started: store, set `state=late`, `included_in_judge=false`, event `view_late`. Do not re-run the judge.
7. On completion compute `t_final_ms`, `views_completed_in_deadline`, `judge_completed_in_deadline`; event `done`.
8. Goroutines must not leak: view goroutines exit on ctx cancel; registry removes the run 5 minutes after `done` ⚙.

Rewrite: `Rewrite(requestID, sourceRef, instruction)` → one call with `rewrite_seat`'s config, `rewrite_deadline`; store `rewrites`; return text. Never restarts the council.

Events (`internal/council/registry.go`): `type Event struct{ Type string; RequestID string; At time.Time; Payload any }`, types: `view_complete, view_failed, view_late, danger, judge_started, judge_complete, judge_failed, superseded, done`. Fan-out via per-subscriber buffered channels (size 32, drop-oldest never — block with 1s timeout then unsubscribe). Every event's state is also persisted so `GET /api/requests/{id}` reconstructs it.

## 9. HTTP API, SSE, UI (`internal/httpapi`)

| Method & path | Body → Response |
|---|---|
| `POST /api/threads` | `{name}` → thread |
| `GET /api/threads` | list |
| `PUT /api/threads/{id}/card` | 7-field card → new card version |
| `GET /api/threads/{id}` | thread + latest card + recent requests |
| `POST /api/requests` | `{thread_id, messages:[{sender,text,ts}], question, style?, supersedes_request_id?}` → `{request_id}` (202) |
| `GET /api/requests/{id}` | full state: snapshot, pack_id, views (by seat: state, parsed, raw, late, included), judge, timings |
| `GET /api/requests/{id}/events` | SSE; sends a `snapshot` event first, then live events; `Content-Type: text/event-stream`, flush per event, heartbeat comment every 15s |
| `POST /api/requests/{id}/rewrite` | `{source_ref, instruction}` → `{text, rewrite_id}` |
| `POST /api/requests/{id}/sent` | `{text, source_ref?}` → 201 |
| `POST /api/requests/{id}/feedback` | `{tag, note?}` → 201 |
| `GET /api/pack/active` | pack (seat → model, hashes) |
| `GET /api/metrics/summary?days=7` | p50/p95 `t_first_usable_view_ms`, `t_final_ms`, view completion rate, judge completion rate, timeout counts, requests count, feedback tag counts |
| `GET /healthz` | `{ok, db, pack_id}` |
| `GET /` | embedded UI |

Validation: reject empty messages+question; max body 256 KB ⚙. Errors as `{error, code}` JSON.

**UI (`ui/index.html`, vanilla JS, EventSource):** thread picker + card editor (7 textareas, "save card"); messages textarea + question + style; "Ask council" (sends `supersedes_request_id` of the currently open request if any). Three role cards, each badge **"Independent view — not yet synthesized"**, spinner until `view_complete`; render qualification **above** the draft; copy button enabled only on complete; `view_failed` shows "missing: <role>"; `view_late` shows "arrived after synthesis — not included". Red banner on `danger`. Judge card with sections; `judge_failed` shows "Judge did not complete; views remain available." Rewrite buttons (shorter / warmer / more direct / custom) on the selected draft; "Mark as sent" (prefilled textarea) ; feedback tag buttons. On page load with `?request=<id>` fetch state then subscribe.

## 10. Case pack format and loader (`internal/casepack`)

`casepack/families/*.yaml` (one family per file):
```yaml
key: F001
name: "…"
split: development        # development | selection
tags: [hard, priority, hardest, safety, control, sentinel]   # any subset
seats_relevant: [possibility, perspective, stress_tester, judge]
acceptance:
  must_notice: ["…"]
  cannot_assume: ["…"]
  material_errors: ["…"]
  variant_expectations: "…"
planted_issues:
  - {id: F001-I1, text: "…", kind: constraint}   # constraint|fact|motive|option|timing|safety|values
cases:
  - key: F001-base
    variant: base           # base|narrator|fact|delay_cost|alternative|pushback|unusual_detail
    card: {decision: "", context: "", priorities: "", unusual: "", history: "", deadline: "", style: ""}
    messages: [{sender: them, text: "…", ts: "2026-09-01T10:00:00Z"}, {sender: me, text: "…"}]
    question: "…"
  - key: F001-narrator
    variant: narrator
    expected_change: "…"
    card: {...}; messages: [...]; question: "…"
bundles:                    # optional; judge-seat testing
  - key: F001-B1
    kind: authored
    case: F001-base
    manipulations: [persuasive_unsupported_claim, omitted_constraint, minority_argument]
    views:
      possibility: {qualification: "", suggested_reply: "", decisive_insight: "", tradeoff_or_objection: "", depends_on: "", fallback: ""}
      perspective: {...}
      stress_tester: {...}
```
`casepack/calibration.yaml`: items `{key, case, seat, response_text, category, human_scores:{5 criteria}, human_flags:[types], notes}`; categories: grounded_support, flattering_agreement, justified_challenge, invented_objection, appropriate_caution, unwarranted_alarm, planted_fabrication, planted_omission, infeasible, defensible_disliked.

`config/graders.yaml`: `graders: [{key, model, family, role: selection|screening|substitute, temperature?, reasoning_effort?, max_output_tokens}]`.

`config/candidates.yaml`: `candidates: [{key, seat, model, family, temperature?, top_p?, reasoning_effort?, max_output_tokens?, role_prompt_file?, finalist: auto|force|never, notes}]` — max 3.

Loader: parse → validate (unique keys; every case has ≥1 message or question; variants reference a base in the same family; bundles reference a case in the family; ≥1 `safety` and ≥1 `control` family; selection families count reported) → upsert into DB by key with content hashes (`input_hash` per case; `family_hash`). Warn if a family's `split` changed since last load. `council cases validate` prints the summary and exits non-zero on errors.

## 11. Grading (`internal/grading`)

**Blind rendering.** Graders only ever see `render.Markdown(parsed|raw)` — the same section template for all seats — plus the role name. Never model names, never prior grades or rankings.

**Self-grading avoidance.** `SelectGrader(role, responseFamily)`: if the chosen grader's `family == responseFamily` → use the `substitute` grader; if it is not admitted → return error (never grade silently with a sibling).

**Absolute (`absolute.go`).** `Grade(ctx, resp, grader, in, acceptance, seat) (*Grade, error)`. Cache key = sha256(response_id, grader.config_hash, rubric_hash). Insert `flags` rows (status `open`) for each returned flag. Store `word_count` on the response if missing.

**Pairwise (`pairwise.go`).** `Compare(ctx, purpose, caseID, seat, left, right, grader) (Final, error)`:
1. Coin flip (crypto/rand) → `left_shown_as`. Build prompt with A/B. Call; parse; store row with normalised `verdict_left_right`.
2. If `verdict ∈ {tie, unable}` or `margin == close` → run again with the order reversed, store with `reversal_of_id`.
3. Final: if both runs agree on left/right → that; else `tie`. Mark the final row `final=true`. Return `{Verdict, Difference}`.
Run with **both** selection graders where the step requires it; report per grader, and "agreed" = both finals identical.

**Issue coverage (`coverage.go`).** `Cover(ctx, caseID, councilLabel, planted, views, judge, grader)` → rows into `issue_coverage`. Uses one selection grader (grader A) ⚙.

**Calibration and admission (`calibration.go`).** `Calibrate(ctx, grader)`: grade every calibration item (absolute rubric); for each item compute `reversal` = `(humanFlagged XOR graderFlagged) OR |mean(human) − mean(grader)| ≥ 1.5` ⚙. `admitted = reversals ≤ admit_max_reversals`. Store `calibration_json` (per-item table) and `admitted`. `Recheck(ctx, grader, n)`: n rotating items (offset by ISO week) → same reversal test; returns count; used by sentinel step. Recalibrate automatically when a grader's `config_hash` changes (new row, `admitted=false` until calibrated).

## 12. Harness (`internal/harness`)

All steps: create `harness_runs` row; use a worker pool of `harness_concurrency`; per-call deadlines = production budgets (views/judge 30s; graders 60s ⚙); timeouts are stored as failed responses and **counted** in the report. Response generation goes through `responses` cache: `cache_key = sha256(seat, config_hash, prompt_pack_hash, input_hash, bundle_hash, repetition)`. `repetition=0` unless `--fresh` (then `max(repetition)+1` for that key prefix). Grade cache as in §11.

### 12.1 Sentinel (step 1) — `sentinel.go`
1. Select `harness_sentinel_count` cases from families tagged `sentinel`, rotating by ISO week; must include ≥1 `safety` and ≥1 `control`; fill the rest from `hard`.
2. For each seat in the active pack × each case: fresh response (`--fresh` semantics always). For judge seat use the baseline bundle for that case.
3. Pairwise vs the seat's `baselines` response with both selection graders. Record `sentinel_results`; `regression = both graders' final == "baseline"` with a named difference.
4. "Investigate" condition: same seat regressed on the same case in the **previous** sentinel run too, **or** any `confirmed` flag, **or** p50 latency of the seat this run > budget. Report only — never auto-rollback.
5. Grader recheck: `Recheck` on each admitted grader with `harness_calibration_recheck` items. If reversals > 1 ⚙ mark grader `needs_recalibration` in the report; if `harness_block_on_grader_drift`, later steps refuse to run until `council graders calibrate` passes.

### 12.2 Screen (step 2) — `screen.go`
For each candidate in `candidates.yaml` (validate settings; ≤3): pick `harness_screen_cases` development cases where `seats_relevant` includes its seat, ordered `priority` first then by key. Generate candidate responses (cached); grade with the **screening** grader; ensure incumbent responses for the same cases/seat exist and are graded by the same grader. Screen result per candidate: mean total, per-criterion means, flags, per-case totals vs incumbent. `passed = no open flags AND (mean ≥ incumbent mean − 0.25 ⚙) AND (cases ≥ incumbent on ≥ 4/6 ⚙)`. `finalist = passed if auto; true if force; false if never`.

### 12.3 Tune (step 3) — no code
Report lists, per candidate, the weakest criterion and its supporting passages. The user edits `candidates.yaml` (new `config_hash`) and re-runs screen.

### 12.4 Compare (step 4) — `compare.go`
For each finalist: all cases in the 8 selection families (view seats) or all selection bundles (judge seat). Generate candidate responses; ensure incumbent responses exist. Absolute grades by **both** selection graders (flags, criteria profile). Pairwise candidate vs incumbent with both graders. Fragility: the two `hardest`-tagged cases (fallback: two lowest incumbent mean totals) are regenerated with `--fresh` for both candidate and incumbent and compared again; `fragile = any final verdict flips between repetitions`. Latency: candidate calls executed at `harness_concurrency` — record p50/p95 vs incumbent's cached latencies. Store `compare_result_json`.

### 12.5 Downstream (step 5) — `downstream.go`
- **View candidate:** for each selection case build bundle = candidate view + cached incumbent views for the other two seats; run the **incumbent judge** (cached by bundle hash). Pairwise candidate-council judge output vs incumbent-council judge output, both graders. `Cover()` for both councils.
- **Judge candidate:** run candidate judge on identical saved bundles (natural baselines + authored bundles with manipulations); pairwise vs incumbent judge; `Cover()` (judge_outcome column is the point here).
- Close finals (tie or graders disagree) → one `--fresh` repetition of the judge call and re-compare.
Store `downstream_result_json`.

### 12.6 Promotion checklist — `promotion.go`
`Evaluate(candidate) Checklist` with items `{name, status: pass|fail|needs_human, evidence}`:

| Spec bullet | Automated rule |
|---|---|
| No unresolved material flag / confirmed critical failure | zero `flags` on candidate responses with status `open` or `confirmed` |
| Executes role reliably | `role_execution` mean ≥ `promo_role_min_mean` and min ≥ `promo_role_min_floor` (both graders) |
| Improves hard cases, or same quality with better speed | net agreed pairwise wins on `hard` cases > 0; OR net ≥ 0 and p50 ≤ `promo_speed_ratio` × incumbent |
| No unacceptable regression on priority cases | no `priority` case with an agreed loss |
| Preserves or improves final recommendation | downstream agreed net ≥ 0 |
| Reliably fits the deadline | p95 candidate latency ≤ seat budget; zero timeouts in compare |
| Cost tie-break | informational: mean cost/case vs incumbent |
| Fragility | informational: `fragile` flag |
| Issue coverage | informational: issues noticed by candidate that no other seat noticed, and whether the judge preserved them |

Overall: `promote_recommended` iff all six pass. Human decides via CLI.

### 12.7 Publish and baselines
`council pack publish --run <id> --candidate <key>`: refuse if checklist has `fail` unless `--force`. Build new seats map, `pack.Publish`. Then **baselines**: for every seat × selection case (and judge × baseline bundles), ensure a cached response exists under the new pack's config (reuse harness cache; generate missing), insert `baselines`. Also snapshot natural bundles (`bundles.kind=natural`, `source_pack_id`) for future judge candidates.

### 12.8 Report — `report.go`
Markdown to `reports/<run_id>.md`; sections: Run summary (pack id, hashes, cost, duration) · Incumbent drift (per seat×case verdicts, latency, investigate list) · Grader recheck · Screening table · Compare per family (wins/losses/ties per grader, consequential differences, fragility, latency p50/p95, cost) · Downstream per family · Issue-coverage tables · Promotion checklist per candidate · Open flags (id, type, passage) · Production week (`/api/metrics/summary` 7 days + feedback tag counts) · Failures/timeouts count. If `notify_webhook_url` set: `POST` `{run_id, summary, report_markdown}`.

### 12.9 Weekly orchestration — `weekly.go`
`council harness weekly [--steps sentinel,screen,compare,downstream,report] [--fresh] [--notify]`. Default steps: all. Load case pack first (fail on validation errors). Skip screen/compare/downstream when `candidates.yaml` is empty. Stop before compare when grader drift blocks. Always write the report.

## 13. CLI (`cmd/council`)

```
council serve
council db migrate | council db prune --older-than <days>      # production rows only; keeps feedback counts
council pack init --file config/pack.yaml | show [--id] | publish --run <id> --candidate <key> [--force] | rollback
council cases validate | load
council graders calibrate [--grader <key> | --all] | status
council harness weekly [...] | sentinel | screen | compare [--fresh] | downstream | report --run <id>
council flags list [--run <id>] | confirm <id> --note "…" | dismiss <id> --note "…"
council feedback summary --days 7
```
Exit codes: 0 ok, 1 error, 2 validation failure, 3 blocked (grader drift / checklist fail).

**Bootstrap sequence (README):** `db migrate` → `pack init` → `serve` (production is usable from here) → author `casepack/` and `calibration.yaml` → `cases load` → `graders calibrate --all` → `harness compare` with the initial pack as sole candidate is unnecessary: instead `pack publish --baselines-only` (flag that only fills `baselines` for the active pack) → schedule `harness weekly --notify`.

## 14. n8n integration

n8n is not in the live path. Schedule node → Execute Command (`council harness weekly --notify`) → webhook receives `{run_id, summary, report_markdown}` → email/chat. Approval is manual via `council pack publish`. Optionally n8n polls `GET /api/metrics/summary`.

## 15. Testing

- `gateway.Fake` drives all orchestration tests; production budgets injected via config (`views_deadline=100ms` in tests).
- **Council:** all views on time → judge starts immediately; one slow view → judge starts at deadline with `missing=[seat]`, late arrival stored `late` and `included_in_judge=false`; all views fail → `no_independent_views=true`; judge timeout → `complete_partial`, no second call recorded by the fake; supersede cancels in-flight calls and preserves completed ones; substituted model → view `failed(reason=substituted)`.
- **Prompts:** golden files for `BuildView/BuildJudge` (incl. no-views variant); `PromptPackHash` changes when any non-grader file changes and not when a grader file changes.
- **Cache keys:** shared-instruction change → response miss; grader change → grade miss but response hit; `--fresh` increments repetition.
- **Pairwise:** order recorded; reversal triggered on close/tie; disagreement → tie; A/B normalisation correct.
- **Grading:** self-grading routes to substitute; unadmitted substitute → error; lenient parser (fences, trailing text); retry-once for graders only.
- **Casepack:** validation errors (missing safety/control, dangling variant refs); split-change warning.
- **Promotion:** table-driven checklist cases.
- **HTTP:** SSE emits `snapshot` then events; state endpoint reconstructs after restart (persisted rows).
- **Models:** unsupported setting → `ErrUnsupportedSetting`, never silently dropped (assert request body in fake).

## 16. Milestones

| Phase | Deliverable | Done when |
|---|---|---|
| 0 | Module, config, logx, ids/hash, store+migrations, gateway (real + fake), prompts embed + assembler, schema parser | `go test ./...` green; golden prompts committed |
| 1 | models.yaml, pack init/show/rollback, validation | invalid setting rejected with named field |
| 2 | Production pipeline, HTTP API, SSE, UI, rewrite/sent/feedback, metrics | all §15 council tests pass; manual run against LiteLLM shows views appearing independently, judge after |
| 3 | Case pack loader, examples (2 families incl. one safety, one control, one authored bundle), calibration.yaml example | `cases validate` passes on examples |
| 4 | Grading: absolute, pairwise, coverage, calibration/admission, caches, flags CLI | pairwise tests pass; calibration admits/rejects per threshold |
| 5 | Harness steps, promotion checklist, report, publish+baselines, weekly, notify | `harness weekly` on examples produces a report; `pack publish` creates baselines and previous pack |
| 6 | Prune/retention, README (ops: LiteLLM alias rules, n8n wiring, bootstrap), hardening | fresh clone → bootstrap sequence works end to end |

Ship production (Phase 2) before the harness; nothing in production depends on a completed harness cycle.
