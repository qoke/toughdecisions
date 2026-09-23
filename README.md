# Tough Decisions Council

Two systems, one binary (`council`): the **production system** (`council serve`)
and the **harness** (`council harness …`, `council pack …`, `council graders …`).
They share storage, the gateway client, prompt assembly, and the published
**production pack**. Production reads the active pack; only the harness writes packs.

Hard rules (visible in code, not just docs):

1. No fallbacks, retries, or silent model substitution in production or harness.
   A failed/timed-out call is recorded as absent; a substituted model is a failure.
2. Unsupported sampling/reasoning settings are **rejected** at pack validation
   and at call time, never dropped.
3. Views run in parallel; the judge starts when all views finish **or** the views
   deadline expires, whichever is first. Judge timeout → completed views stay
   available; no retry.
4. Every production answer is tied to an immutable input snapshot and an
   immutable pack id.
5. Graders never see model identities or prior results.

## Bootstrap

```sh
make build
mkdir -p data                            # store directory must exist before migration
./bin/council db migrate                        # create/migrate the SQLite store
./bin/council pack init --file config/pack.yaml # load the initial production pack
./bin/council serve                             # production is usable from here
# --- Phase 3+ (deferred) ---
# author casepack/ and calibration.yaml
./bin/council cases load
./bin/council graders calibrate --all
./bin/council pack publish --baselines-only     # Phase 5: fill baselines for the active pack
# schedule: council harness weekly --notify
```

Alternative without building: `go run ./cmd/council …` (a `go run` naming an individual `.go` file compiles only that file — the package is nine files).

Exit codes: 0 ok, 1 error, 2 validation failure, 3 blocked
(grader drift / checklist fail).

## LiteLLM ops rules

LiteLLM proxy aliases used by the council must have:

- no `fallbacks`
- `num_retries: 0`
- `drop_params` disabled
- pinned provider snapshot ids in the alias mapping

The client never sends `fallbacks`, `num_retries`, or `drop_params`, and only
includes `temperature`/`top_p`/`reasoning_effort` when set. `max_completion_tokens`
is always set. LiteLLM returns a deployment hash (64-hex) in the
`x-litellm-model-id` response header; substitution detection ignores it and
reads the response body `model` field instead, so a hash there is expected,
not an error.

## Configuration

Config loads from `COUNCIL_`-prefixed env vars with these defaults:

| Key | Default |
|---|---|
| `server_listen` | `:8080` |
| `db_path` | `./data/council.db` |
| `gateway_base_url` | `http://localhost:4000` |
| `pack_file` | `./config/pack.yaml` |
| `graders_file` | `./config/graders.yaml` |
| `models_file` | `./config/models.yaml` |
| `candidates_file` | `./config/candidates.yaml` |
| `views_deadline` / `judge_deadline` | `30s` |

`gateway_base_url` (`COUNCIL_GATEWAY_BASE_URL`) is the LiteLLM proxy root
**without** `/v1`: canonical `http://127.0.0.1:4000` (the code default
`http://localhost:4000` is the local equivalent). The client appends
`/v1/chat/completions`, so a base ending in `/v1` or `/` is normalised away
but is not canonical.

Repo config: `config/pack.yaml` (4 seats: possibility, perspective,
stress_tester, judge; views 2000 tokens, judge 3000), `config/models.yaml`
(**a template, not a runnable config**), `config/graders.yaml` (two
admitted selection graders from different families, one screening grader,
one substitute grader), `config/candidates.yaml` (example view + judge
challengers, max 3; empty list skips screen/compare/downstream).

`config/models.yaml` must be adapted to the target proxy before use: each
`id` is sent **verbatim** as the request model (there is no alias
indirection), so every `id` must be a model that proxy actually serves, and
every `expected_response_model_prefixes` entry must match what that
deployment returns for that model.

## Harness ops

Cache keys (R-25 / §15) — changing the shared instructions invalidates the
response cache (`prompt_pack_hash` feeds `store.ResponseCacheKey`); changing
a grader (`config_hash`) or the rubric invalidates the grade cache while the
response cache still hits; `--fresh` inserts with `MaxRepetition()+1`, so it
always produces a new response key. A grader `config_hash` change starts
unadmitted — recalibrate before compare.

n8n wiring (§14): n8n is not in the live path. A Schedule node runs
`council harness weekly --notify`, and when `notify_webhook_url` is set the
report step `POST`s `{run_id, summary, report_markdown}` to that webhook
(n8n → email/chat). Promotion stays manual: `council pack publish
--run <id> --candidate <key>` (refuses on a failing checklist unless
`--force`). Optionally n8n polls `GET /api/metrics/summary`.

Full bootstrap (copy-paste; sentinel refuses until baselines exist, so the
baselines step must come before the first weekly run):

```sh
make build
mkdir -p data                            # store directory must exist before migration
./bin/council db migrate
./bin/council pack init --file config/pack.yaml
./bin/council serve                                    # production usable from here
# --- harness setup ---
# author casepack/ and casepack/calibration.yaml, then:
./bin/council cases validate
./bin/council cases load
./bin/council graders calibrate --all
./bin/council graders status                           # both selection graders admitted
./bin/council pack publish --baselines-only            # fill baselines for the active pack
./bin/council harness sentinel                         # fails naming --baselines-only if skipped
./bin/council harness weekly --notify                  # full run: sentinel,screen,compare,downstream,report
```

## What is implemented vs deferred

Implemented (Phases 0–2): store + migrations, gateway (real + fake), prompt
assembly, lenient schema parsing, model capabilities + pack init/show/rollback,
production pipeline, HTTP API + SSE + UI, `db migrate`, `pack init|show|rollback`,
`feedback summary`.

Deferred: none — Phases 3–6 are wired (`cases validate|load`,
`graders calibrate|status`, `flags …`, `harness weekly|sentinel|screen|
compare|downstream|report`, `pack publish` with checklist/force,
`db prune` with retention). `pack publish` exits 3 on a failing checklist
without `--force`; grader drift exits 3.
