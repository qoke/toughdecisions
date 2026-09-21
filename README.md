# Relationship Council

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
council db migrate                        # create/migrate the SQLite store
council pack init --file config/pack.yaml # load the initial production pack
council serve                             # production is usable from here
# --- Phase 3+ (deferred) ---
# author casepack/ and calibration.yaml
council cases load
council graders calibrate --all
council pack publish --baselines-only     # Phase 5: fill baselines for the active pack
# schedule: council harness weekly --notify
```

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
is always set.

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

Repo config: `config/pack.yaml` (4 seats: possibility, perspective,
stress_tester, judge; views 2000 tokens, judge 3000), `config/models.yaml`
(capabilities for every referenced model), `config/graders.yaml` (placeholder
selection/screening/substitute graders), `config/candidates.yaml` (empty).

## What is implemented vs deferred

Implemented (Phases 0–2): store + migrations, gateway (real + fake), prompt
assembly, lenient schema parsing, model capabilities + pack init/show/rollback,
production pipeline, HTTP API + SSE + UI, `db migrate`, `pack init|show|rollback`,
`feedback summary`.

Deferred: Phase 3 (`cases validate|load`, casepack authoring), Phase 4
(`graders calibrate|status`, `flags …`, grading), Phase 5 (`harness …`,
`pack publish`), Phase 6 (`db prune`, retention). Their CLI entries print
`not implemented until Phase N`; `pack publish` exits 3 (blocked: requires
harness run).
