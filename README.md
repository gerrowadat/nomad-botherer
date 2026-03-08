# nomad-botherer

[![Tests](https://github.com/gerrowadat/nomad-botherer/actions/workflows/test.yml/badge.svg)](https://github.com/gerrowadat/nomad-botherer/actions/workflows/test.yml)
[![Coverage](https://codecov.io/gh/gerrowadat/nomad-botherer/graph/badge.svg)](https://codecov.io/gh/gerrowadat/nomad-botherer)

Watches a remote git repo for Nomad job HCL definitions and continuously compares them against a live Nomad cluster. When drift is detected it logs, exposes Prometheus metrics, and reports details on `/healthz`.

Three kinds of drift are tracked:

| Diff type | Meaning |
|-----------|---------|
| `modified` | Job exists in both HCL and Nomad but the definitions differ (detected via `nomad job plan`) |
| `missing_from_nomad` | Job defined in HCL but not currently registered in Nomad |
| `missing_from_hcl` | Job registered in Nomad but has no HCL file in the repo |

---

## Contents

- [How it works](#how-it-works)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Webhooks](#webhooks)
- [Monitoring](#monitoring)
- [Docker](#docker)
- [Development](#development)

---

## How it works

1. On startup, the repo is cloned entirely into memory using [go-git](https://github.com/go-git/go-git).
2. All `.hcl` files under `--hcl-dir` (default: repo root) are sent to Nomad's `/v1/jobs/parse` endpoint to produce canonical `Job` structs.
3. For each parsed job:
   - If the job is **not registered** in Nomad → `missing_from_nomad`
   - If the job **is registered**, `nomad job plan` is run → if the plan shows changes → `modified`
4. All jobs **currently running in Nomad** that have no corresponding HCL file → `missing_from_hcl`
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

Standard Prometheus endpoint. Key metrics:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nomad_botherer_job_diffs` | Gauge | `job`, `diff_type` | 1 for each active drift entry |
| `nomad_botherer_last_check_timestamp_seconds` | Gauge | — | Unix time of last diff check |
| `nomad_botherer_git_last_update_timestamp_seconds` | Gauge | — | Unix time of last git fetch |
| `nomad_botherer_info` | Gauge | `version` | Build version info |

**Example Prometheus alert:**

```yaml
groups:
  - name: nomad-botherer
    rules:
      - alert: NomadJobDrift
        expr: sum(nomad_botherer_job_diffs) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Nomad job definitions have drifted from git"
          description: "{{ $value }} job(s) differ between the git repo and the running cluster."

      - alert: NomadBothererStale
        expr: time() - nomad_botherer_last_check_timestamp_seconds > 300
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "nomad-botherer has not run a diff check recently"
```

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

### Build and test

```bash
make build        # compile for the current platform
make test         # go test -race ./...
make test-cover   # run tests and open an HTML coverage report
make lint         # go vet ./...
make clean        # remove build artefacts
```

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
