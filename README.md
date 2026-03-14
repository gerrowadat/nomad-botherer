# nomad-botherer

[![Tests](https://github.com/gerrowadat/nomad-botherer/actions/workflows/test.yml/badge.svg)](https://github.com/gerrowadat/nomad-botherer/actions/workflows/test.yml)
[![Coverage](https://raw.githubusercontent.com/wiki/gerrowadat/nomad-botherer/coverage.svg)](https://raw.githack.com/wiki/gerrowadat/nomad-botherer/coverage.html)

Watches a remote git repo for Nomad job HCL definitions and continuously compares them against a live Nomad cluster. When drift is detected it logs, exposes Prometheus metrics, and reports details on `/healthz`.

Three kinds of drift are tracked:

| Diff type | Meaning |
|-----------|---------|
| `modified` | Job exists in both HCL and Nomad but the definitions differ (detected via `nomad job plan`) |
| `missing_from_nomad` | Job defined in HCL but not currently registered in Nomad (dead jobs count as missing by default) |
| `missing_from_hcl` | Job registered and running in Nomad but has no HCL file in the repo (dead jobs are excluded by default) |

---

## Contents

- [How it works](#how-it-works)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Webhooks](#webhooks)
- [Monitoring](#monitoring)
  - [`/healthz`](#healthz)
  - [`/metrics`](#metrics)
  - [Sample Prometheus configuration](#sample-prometheus-configuration)
- [Docker](#docker)
- [Development](#development)

---

## How it works

1. On startup, the repo is cloned entirely into memory using [go-git](https://github.com/go-git/go-git).
2. All `.hcl` files under `--hcl-dir` (default: repo root) are sent to Nomad's `/v1/jobs/parse` endpoint to produce canonical `Job` structs.
3. For each parsed job:
   - If the job is **not registered** in Nomad, or is registered but in **`dead` state** → `missing_from_nomad`
   - If the job **is registered and live**, `nomad job plan` is run → if the plan shows changes → `modified`
4. All jobs **currently running in Nomad** (non-dead) that have no corresponding HCL file → `missing_from_hcl`

   Dead jobs are excluded from both checks by default because a stopped job is expected state — it was intentionally halted. Pass `--include-dead-jobs` to treat dead jobs like running ones.
5. Results are stored in memory and exposed via `/healthz` (JSON) and `/metrics` (Prometheus).
6. The repo is re-checked on every `--poll-interval` (git fetch), on every `--diff-interval` (Nomad-side drift), and immediately on a webhook push event.

---

## Installation

### From source

Requires Go 1.22+.

```bash
git clone https://github.com/gerrowadat/nomad-botherer.git
cd nomad-botherer
make build
./nomad-botherer --help
```

### Docker

```bash
docker pull ghcr.io/gerrowadat/nomad-botherer:latest
```

Pre-built images are available for `linux/amd64` and `linux/arm64` (Raspberry Pi 4+).

---

## Quick start

**Public repo, Nomad without ACLs:**

```bash
./nomad-botherer \
  --repo-url https://github.com/myorg/nomad-jobs.git \
  --nomad-addr http://nomad.example.com:4646
```

**Private repo via GitHub PAT, Nomad with ACL token:**

```bash
export GIT_TOKEN=ghp_...
export NOMAD_TOKEN=...
./nomad-botherer \
  --repo-url https://github.com/myorg/nomad-jobs.git \
  --nomad-addr http://nomad.example.com:4646 \
  --hcl-dir jobs
```

**Private repo via SSH key:**

```bash
./nomad-botherer \
  --repo-url git@github.com:myorg/nomad-jobs.git \
  --git-ssh-key ~/.ssh/id_ed25519 \
  --nomad-addr http://nomad.example.com:4646
```

---

## Configuration

Every flag has a corresponding environment variable. Environment variables are read at startup; flags override them when explicitly passed.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--repo-url` | `GIT_REPO_URL` | *(required)* | Remote git repo URL |
| `--branch` | `GIT_BRANCH` | `main` | Branch to watch |
| `--poll-interval` | `POLL_INTERVAL` | `5m` | How often to poll git for changes |
| `--hcl-dir` | `HCL_DIR` | *(repo root)* | Subdirectory containing HCL job files |
| `--git-token` | `GIT_TOKEN` | | HTTP token for private repos (GitHub PAT etc.) |
| `--git-ssh-key` | `GIT_SSH_KEY` | | Path to SSH private key |
| `--git-ssh-key-password` | `GIT_SSH_KEY_PASSWORD` | | SSH key passphrase |
| `--nomad-addr` | `NOMAD_ADDR` | `http://127.0.0.1:4646` | Nomad API address |
| `--nomad-token` | `NOMAD_TOKEN` | | Nomad ACL token |
| `--nomad-namespace` | `NOMAD_NAMESPACE` | `default` | Nomad namespace |
| `--listen-addr` | `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `--webhook-secret` | `WEBHOOK_SECRET` | | GitHub webhook HMAC secret |
| `--webhook-path` | `WEBHOOK_PATH` | `/webhook` | Webhook endpoint path |
| `--diff-interval` | `DIFF_INTERVAL` | `1m` | Periodic Nomad-side drift check interval |
| `--include-dead-jobs` | `INCLUDE_DEAD_JOBS` | `false` | Treat dead Nomad jobs like running ones (by default dead jobs count as missing) |
| `--log-level` | `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |

Logs are written to stderr as JSON (structured via `log/slog`).

---

## Webhooks

Configuring a webhook removes the latency between a push to the repo and the next drift check — instead of waiting for `--poll-interval`, nomad-botherer fetches immediately on push.

### GitHub setup

1. Go to your repo → **Settings** → **Webhooks** → **Add webhook**
2. Set **Payload URL** to `https://your-host:8080/webhook`
3. Set **Content type** to `application/json`
4. Set **Secret** to the same value as `--webhook-secret` / `WEBHOOK_SECRET`
5. Under **Which events would you like to trigger this webhook?** choose **Just the push event**
6. Click **Add webhook**

The service handles `push` events (triggers a fetch + diff) and `ping` events (acknowledged, no action). All other event types are silently ignored with a `200 OK`.

If `--webhook-secret` is empty, signature verification is skipped. In production, always set a secret.

---

## Monitoring

### `/healthz`

Always returns **HTTP 200**. The JSON body describes the current drift state:

```json
{
  "status": "diffs_detected",
  "diff_count": 2,
  "diffs": [
    {
      "job_id": "api-server",
      "hcl_file": "jobs/api-server.hcl",
      "diff_type": "modified",
      "detail": "Nomad plan shows diff type \"Edited\""
    },
    {
      "job_id": "legacy-worker",
      "diff_type": "missing_from_hcl",
      "detail": "job is running in Nomad (status: running) but has no HCL definition in the repo"
    }
  ],
  "last_check": "2024-01-15T12:00:00Z",
  "git_commit": "abc1234def5678",
  "git_updated": "2024-01-15T11:59:50Z"
}
```

`"status"` is `"ok"` when there are no diffs, `"diffs_detected"` otherwise.

### `/metrics`

Standard Prometheus exposition endpoint. All metric names are prefixed with `nomad_botherer_`.

#### Drift state

These metrics describe the current relationship between the git repo and the live Nomad cluster. They are reset and recomputed on every diff check.

| Metric | Type | Labels | What it tells you |
|--------|------|--------|-------------------|
| `nomad_botherer_drifted_jobs` | Gauge | `diff_type` | Number of jobs currently in each drift state. The simplest signal for "is anything wrong?" — alert on `sum(nomad_botherer_drifted_jobs) > 0`. |
| `nomad_botherer_job_diffs` | Gauge | `job`, `diff_type` | 1 for every (job, diff_type) pair currently detected. Useful for per-job dashboards or filtering by job name. |
| `nomad_botherer_job_drift_first_seen_timestamp_seconds` | Gauge | `job`, `diff_type` | Unix timestamp of when drift was first detected for this job. Absent when no drift is present. `time() - metric` gives how long the job has been drifting — use this to distinguish a deploy in progress from a job that's been stuck for hours. |

#### Diff checks

These counters and timestamps describe the diff check loop itself — how often it runs and whether it is working correctly.

| Metric | Type | Labels | What it tells you |
|--------|------|--------|-------------------|
| `nomad_botherer_diff_checks_total` | Counter | — | Total diff checks run since startup. Use `rate()` to confirm the loop is running at the expected frequency. |
| `nomad_botherer_last_check_timestamp_seconds` | Gauge | — | Unix timestamp of the most recent completed diff check. Alert when `time() - metric` exceeds 2× `--diff-interval` to catch a stuck check loop. |
| `nomad_botherer_nomad_api_errors_total` | Counter | `op` (`info`, `plan`, `list`) | Nomad API call failures by operation. `info` = job lookup, `plan` = drift plan, `list` = listing all jobs. A rising count means drift results may be incomplete for that operation. |
| `nomad_botherer_hcl_parse_errors_total` | Counter | — | HCL files that failed to parse via the Nomad API. These files are skipped; the rest of the check continues. |
| `nomad_botherer_hcl_non_job_files_skipped_total` | Counter | — | HCL files that were skipped because they contain no `job` stanza (e.g. ACL policies, volumes). Expected and normal; a rising rate may indicate `--hcl-dir` is set too broadly. |

#### Git tracking

These metrics describe the in-memory git clone and polling loop.

| Metric | Type | Labels | What it tells you |
|--------|------|--------|-------------------|
| `nomad_botherer_git_fetches_total` | Counter | — | Total remote fetch/clone attempts. Each poll interval triggers one. |
| `nomad_botherer_git_fetch_errors_total` | Counter | — | Fetch/clone attempts that failed. A rising count means new commits are not being picked up; diff checks continue against the last known commit. |
| `nomad_botherer_git_last_update_timestamp_seconds` | Gauge | — | Unix timestamp of the last successful fetch. Alert when `time() - metric` is significantly larger than `--poll-interval` to catch a stuck git loop. |

#### Webhooks

These metrics describe incoming webhook events from GitHub.

| Metric | Type | Labels | What it tells you |
|--------|------|--------|-------------------|
| `nomad_botherer_webhook_events_total` | Counter | `event` (`push`, `ping`, `unknown`, `error`) | Webhook events received by type. `push` events trigger an immediate fetch. `error` events indicate a failed delivery (bad signature, parse error, etc.). |
| `nomad_botherer_last_webhook_success_timestamp_seconds` | Gauge | — | Unix timestamp of the last successfully processed webhook. Zero if no webhook has been received yet. |
| `nomad_botherer_last_webhook_failure_timestamp_seconds` | Gauge | — | Unix timestamp of the last failed webhook delivery. Zero if no failure has occurred. |

#### Service info

| Metric | Type | Labels | What it tells you |
|--------|------|--------|-------------------|
| `nomad_botherer_info` | Gauge | `version` | Always 1. The `version` label holds the build version string. Useful for tracking rollouts: `count by(version)(nomad_botherer_info)`. |

### Sample Prometheus configuration

The [`monitoring/`](monitoring/) directory contains ready-to-use configuration files:

| File | Contents |
|------|----------|
| [`monitoring/prometheus.yml`](monitoring/prometheus.yml) | Scrape configuration for nomad-botherer |
| [`monitoring/recording_rules.yml`](monitoring/recording_rules.yml) | Pre-aggregated series for dashboards and alerts |
| [`monitoring/alerts.yml`](monitoring/alerts.yml) | Alerting rules covering drift, service health, git, and webhooks |

The alerts cover:

- **NomadJobDrift** — any drift detected for more than 5 minutes
- **NomadJobModifiedPersistent** — a job's config has diverged from git for over 1 hour
- **NomadJobMissingFromNomad** — a git-defined job has been absent from Nomad for over 15 minutes
- **NomadJobMissingFromHCL** — a running Nomad job has no HCL file in the repo for over 1 hour
- **NomadBothererCheckStale** — no diff check has completed in over 5 minutes
- **NomadBothererGitFetchFailing** — git fetches have been failing for 10 minutes
- **NomadBothererGitStale** — the in-memory git clone has not refreshed in over 30 minutes
- **NomadBothererAPIErrors** — Nomad API calls are failing
- **NomadBothererDown** — Prometheus cannot reach the `/metrics` endpoint
- **NomadBothererWebhookErrors** — webhook deliveries are consistently failing

---

## Docker

### Run with HTTP token

```bash
docker run -d \
  -e GIT_REPO_URL=https://github.com/myorg/nomad-jobs.git \
  -e GIT_TOKEN=ghp_... \
  -e NOMAD_ADDR=http://nomad.example.com:4646 \
  -e NOMAD_TOKEN=... \
  -p 8080:8080 \
  ghcr.io/gerrowadat/nomad-botherer:latest
```

### Run with SSH key

```bash
docker run -d \
  -e GIT_REPO_URL=git@github.com:myorg/nomad-jobs.git \
  -e GIT_SSH_KEY=/run/secrets/ssh_key \
  -v /path/to/id_ed25519:/run/secrets/ssh_key:ro \
  -e NOMAD_ADDR=http://nomad.example.com:4646 \
  -p 8080:8080 \
  ghcr.io/gerrowadat/nomad-botherer:latest
```

Supported platforms: `linux/amd64`, `linux/arm64` (Raspberry Pi 4+).

### Available tags

| Tag | Description |
|-----|-------------|
| `latest` | Most recent release |
| `1`, `1.2`, `1.2.3` | Semver aliases, updated on each release |

---

## Development

### Local development with .env

Copy `.env.example` to `.env` and fill in your values. The binary loads `.env`
automatically on startup when the file is present, so you can iterate without
setting environment variables by hand each time.

```bash
cp .env.example .env
$EDITOR .env
make build
./nomad-botherer
```

`.env` is listed in `.gitignore` and will never be committed.

### Build and test

```bash
make build        # compile for the current platform
make test         # go test -race ./...
make test-cover   # run tests and open an HTML coverage report
make lint         # go vet ./...
make clean        # remove build artefacts
```

### Simulating a webhook

`scripts/send-webhook.sh` constructs a minimal GitHub push event payload and
POSTs it to a locally running instance. It reads defaults from `.env` (URL,
branch, secret) and accepts flags to override any of them.

```bash
# Push to whatever branch GIT_BRANCH is set to in .env (default: main)
scripts/send-webhook.sh

# Override branch and commit SHA
scripts/send-webhook.sh -b develop -c abc1234def5678

# Target a different host or port
scripts/send-webhook.sh -u http://nomad-botherer.internal/webhook

# See all options
scripts/send-webhook.sh -h
```

If `WEBHOOK_SECRET` is set in `.env`, the script signs the request with an
HMAC-SHA256 signature (using `openssl`). If no secret is set, the request is
sent unsigned.

### Release process

Releases use semver git tags. The Makefile handles tag creation:

```bash
make release-patch   # 1.2.3 → 1.2.4
make release-minor   # 1.2.3 → 1.3.0
make release-major   # 1.2.3 → 2.0.0
```

Each `make release-*` creates a signed annotated tag locally. Push it with:

```bash
git push origin <tag>   # e.g. git push origin v1.2.4
```

Then go to GitHub, find the tag under **Releases**, and **publish** it. Publishing triggers the Docker workflow, which builds and pushes `ghcr.io/gerrowadat/nomad-botherer:<tag>` for both `amd64` and `arm64`.

### Docker builds

```bash
make docker        # build multi-platform image locally (requires docker buildx)
make docker-push   # build and push to ghcr.io
```
