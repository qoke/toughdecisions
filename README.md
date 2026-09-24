# Tough Decisions Council

`council` is two systems in one binary: a **production API server**
(`council serve`) and an **evaluation harness** (`council harness …`,
`council pack …`, `council graders …`). They share storage, the gateway
client, and the published production pack. Production reads the active
pack; only the harness writes packs. There are no fallbacks, retries, or
silent model substitutions anywhere: a failed call is recorded as absent,
and a substituted model is a failure.

## Quickstart

Build the binary, provision the store directory, migrate, load the
shipped pack, then serve:

```sh
make build
mkdir -p data
./bin/council db migrate
./bin/council pack init --file config/pack.yaml
export COUNCIL_GATEWAY_API_KEY='<your-gateway-api-key>'
./bin/council serve
```

`serve` refuses without a key (exit 1, no network), so export it first
(see [API keys](#api-keys-and-the-fail-fast-gate)). Make a first request (the server listens on loopback by default, so no
token is needed from the same machine):

```sh
curl -s -X POST <your-host>/api/requests \
  -H 'Content-Type: application/json' \
  -d '{"card":{"situation":"...","options":["a","b"],"stakes":"..."}}'
```

Every request fans out to 3 parallel views plus 1 judge call (see
[token spend](#token-spend) below).

## Configuration

Every setting is a `COUNCIL_`-prefixed environment variable. Set them
with `export` in the shell:

```sh
export COUNCIL_GATEWAY_API_KEY='<your-gateway-api-key>'
export COUNCIL_GATEWAY_BASE_URL='http://<your-litellm-host>:4000'
```

or in Docker with `-e`:

```sh
docker run -e COUNCIL_GATEWAY_API_KEY='<your-gateway-api-key>' \
  -e COUNCIL_GATEWAY_BASE_URL='http://<your-litellm-host>:4000' …
```

The full key list with defaults and help text is authoritative in the
binary itself — do not trust a hand-copied table:

```sh
make build
./bin/council config reference   # every key, its env name, default
./bin/council config diagnose    # effective values plus where each came from (env vs default)
./bin/council config show        # effective values (secrets masked as ****)
```

Key settings: `COUNCIL_GATEWAY_BASE_URL` (LiteLLM proxy root, without
`/v1`; the client appends `/v1/chat/completions`),
`COUNCIL_GATEWAY_API_KEY` (secret, sent only in the `Authorization`
header), `COUNCIL_DB_PATH` (default `./data/council.db`),
`COUNCIL_SERVER_LISTEN` (default loopback `127.0.0.1:8080`),
`COUNCIL_SERVER_TOKEN` (secret; required for any non-loopback bind, and
then required on every API route except `GET /healthz`, `GET /`, and
`POST /api/session`).

## API keys and the fail-fast gate

Set the gateway key before running anything that spends tokens:

```sh
export COUNCIL_GATEWAY_API_KEY='<your-gateway-api-key>'
```

Any spending command run without a key exits 1 immediately, with no
network and no work done:

```sh
make build
./bin/council harness sentinel
# harness sentinel: missing COUNCIL_GATEWAY_API_KEY: run `council config diagnose` to inspect config, then export COUNCIL_GATEWAY_API_KEY='your-key'
```

The key itself is never printed, logged, or echoed back — only this
actionable message naming the variable. One exception is fully offline:
`council harness weekly --steps report` (report-only) exits 0 with no
key. Check reachability with a single opt-in live call:

```sh
make build
./bin/council config check --live
# config check: ok (live call to "<model>" succeeded)
```

## Command reference

Run `council` with no arguments for the command list, or ask any
command or group for its own help:

```sh
make build
./bin/council
./bin/council serve --help
./bin/council harness --help
./bin/council harness weekly --help
./bin/council help pack publish
```

An unknown subcommand exits 2 and points at help:

```sh
make build
./bin/council bogus
# unknown subcommand "bogus" (want one of: serve db pack cases graders harness flags config feedback help; run `council help` for help)
```

Spend per command lives in the [token-spend table](#token-spend)
below — the in-code table is the only source, so it is not restated
here. Framing worth knowing: `council serve` spends 4 model calls per
request (3 views + 1 judge) with no cache reuse, plus 1 per rewrite;
`council harness weekly --steps report` is the only harness subcommand
that spends nothing. The spending commands are `council serve`,
`council harness sentinel`, `council harness screen`,
`council harness compare`, `council harness downstream`,
`council harness weekly`, `council graders calibrate`,
`council pack publish`, and `council config check --live` — every one
of them refuses without a key (exit 1, no network). Everything else —
`db migrate|prune`, `pack init|show|rollback`, `cases validate|load`,
`graders status`, `flags list|confirm|dismiss`, `feedback summary`,
`harness report`, `config show|reference|diagnose`,
`harness weekly --steps report` — spends nothing and needs no key.

## Token spend

The table below is rendered from the in-code spend table; the code is
the source of truth. `no` means the command makes no model calls.

| Command | Spends | Calls | Per run |
| --- | --- | --- | --- |
| serve | yes | 4 model calls per request (3 views + 1 judge), 1 per rewrite | per POST /api/requests; no cache reuse |
| harness sentinel | yes | 4 seats x harness_sentinel_count generations + drift rechecks (1 call per recheck item per admitted grader) + 1 pairwise comparison per seat x case (2 grader calls each, +1 reversal call each on tie/unable/close) | default 16 generations + rechecks + 16 comparisons |
| harness screen | yes | per candidate: harness_screen_cases x (2 generations + 2 grades) | default 6 cases x (2 generations + 2 grades) per candidate |
| harness compare | yes | per candidate: drift rechecks + all selection cases x (2 generations + 4 absolute grades + 1 pairwise comparison of 2 grader calls, +1 reversal call each on tie/unable/close) + up to 2 fragility picks x (2 fresh generations + 1 repeat comparison) | per candidate over all selection cases |
| harness downstream | yes | per case: drift rechecks (once per run) + 3 cached incumbent views + 1 candidate view + 2 judge calls + 1 pairwise comparison (2 grader calls, +1 reversal call each on tie/unable/close) + up to 2 cover calls + 2 fresh judge calls and 1 repeat comparison when close | per case |
| harness weekly | yes | sum of the selected steps | per --steps selection (default all) |
| graders calibrate | yes | 1 call per calibration item per grader (+1 retry on parse failure) | per calibration item per grader |
| pack publish | yes | 4 cached generations per selection case, on both branches | per publish (baselines generation happens either way) |
| config check --live | yes | exactly 1 gateway call | opt-in only, never called by any other path |
| serve startup | no |  |  |
| db migrate | no |  |  |
| db prune | no |  |  |
| pack init | no |  |  |
| pack show | no |  |  |
| pack rollback | no |  |  |
| cases validate | no |  |  |
| cases load | no |  |  |
| graders status | no |  |  |
| flags list | no |  |  |
| flags confirm | no |  |  |
| flags dismiss | no |  |  |
| feedback summary | no |  |  |
| harness report | no |  |  |
| config show | no |  |  |
| config reference | no |  |  |
| config diagnose | no |  |  |
| config check | no |  |  |

## Troubleshooting

The errors users actually hit:

- **No key**: `harness sentinel: missing COUNCIL_GATEWAY_API_KEY:
  run `council config diagnose` to inspect config, then export
  COUNCIL_GATEWAY_API_KEY='your-key'` — export the key; nothing was
  spent and nothing was sent.
- **Nothing to check**: `config check: nothing to check without --live`
  — `config check` needs the `--live` flag to make its one call.
- **Unknown command**: `unknown subcommand "bogus" (want one of: serve
  db pack cases graders harness flags config feedback help; run
  `council help` for help)` (exit 2) — check the name, or run
  `council help`.
- **Bad flag**: exit 2 from the flag parser (e.g. `harness weekly:
  harness: unknown weekly step "bogus" (want
  sentinel,screen,compare,downstream,report)`) — fix the flag value.
- **Non-loopback without token**: `serve: non-loopback listen "…"
  requires server_token (COUNCIL_SERVER_TOKEN)` — set
  `COUNCIL_SERVER_TOKEN` or bind loopback.
- **Sentinel before baselines**: `harness sentinel: … pack publish
  --baselines-only` — run `council pack publish --baselines-only`
  first.
- **Blocked (exit 3)**: grader drift or a failing promotion checklist
  (e.g. `council pack publish` refusing without `--force`) — inspect
  the report, do not retry blindly; there are no automatic retries.
- **Bad config value**: `serve: validation failed for 'log_level'
  (value=bogus, from env): value must be one of [debug info warn
  error]` — fix the env var.

## Serve and UI

```sh
make build
mkdir -p data
./bin/council db migrate
export COUNCIL_GATEWAY_API_KEY='<your-gateway-api-key>'
./bin/council serve
```

`council serve` refuses without a key (exit 1) and binds loopback-only by default. Binding any
non-loopback address refuses to start unless `COUNCIL_SERVER_TOKEN` is
set; when set, the API requires it on every route except
`GET /healthz`, the static UI shell (`GET /`), and `POST /api/session`.
Clients obtain an HttpOnly `SameSite=Strict` cookie from
`POST /api/session` with body `{"token": "…"}` (or send
`Authorization: Bearer <token>`); query-string tokens are never
accepted. Do not expose the service to an untrusted network without
the token configured.

Harness ops: `council harness weekly --notify` runs the weekly steps
and, when `notify_webhook_url` is set, `POST`s the report to that
webhook (e.g. n8n → email/chat). Promotion stays manual:
`council pack publish --run <id> --candidate <key>` refuses on a
failing checklist unless `--force`. Full harness setup, copy-paste:

```sh
make build
mkdir -p data
./bin/council db migrate
./bin/council pack init --file config/pack.yaml
./bin/council cases validate
./bin/council cases load
export COUNCIL_GATEWAY_API_KEY='<your-gateway-api-key>'
./bin/council graders calibrate --all
./bin/council graders status
./bin/council pack publish --baselines-only
./bin/council harness sentinel
./bin/council harness weekly --notify
```

Exit codes: 0 ok, 1 error, 2 validation failure, 3 blocked
(grader drift / checklist fail).
