#!/usr/bin/env bash
# Send a simulated GitHub push webhook to a running nomad-botherer instance.
# Useful for local development when you want to trigger a diff check without
# waiting for the poll interval or making an actual git push.
#
# Defaults are read from .env in the project root (if it exists), then from
# built-in values. Any option can be overridden with a flag.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

usage() {
    cat <<USAGE
Usage: $(basename "$0") [OPTIONS]

Simulate a GitHub push webhook and POST it to a nomad-botherer instance.

Options:
  -u URL      Target webhook URL  (default: http://localhost:<LISTEN_ADDR port>/webhook)
  -b BRANCH   Branch being pushed (default: \$GIT_BRANCH or "main")
  -c COMMIT   Commit SHA          (default: current HEAD, or a random SHA)
  -s SECRET   HMAC signing secret (default: \$WEBHOOK_SECRET)
  -r REPO     Repository name     (default: myorg/nomad-jobs)
  -h          Show this help

Examples:
  $(basename "$0")
  $(basename "$0") -b develop -c abc1234def5678
  $(basename "$0") -u http://nomad-botherer.internal/webhook -s mysecret
USAGE
    exit 0
}

# ── Load .env if present ──────────────────────────────────────────────────────
if [[ -f "$ROOT_DIR/.env" ]]; then
    set -a
    # shellcheck source=/dev/null
    source "$ROOT_DIR/.env"
    set +a
fi

# ── Defaults (evaluated after .env so its variables are visible) ──────────────
_default_port="${LISTEN_ADDR:-:8080}"
_default_port="${_default_port##*:}"   # strip everything up to and including ':'
URL="http://localhost:${_default_port}${WEBHOOK_PATH:-/webhook}"
SECRET="${WEBHOOK_SECRET:-}"
BRANCH="${GIT_BRANCH:-main}"
COMMIT="$(git -C "$ROOT_DIR" rev-parse HEAD 2>/dev/null || openssl rand -hex 20)"
REPO="myorg/nomad-jobs"

# ── Parse flags ───────────────────────────────────────────────────────────────
while getopts ":u:b:c:s:r:h" opt; do
    case "$opt" in
        u) URL="$OPTARG" ;;
        b) BRANCH="$OPTARG" ;;
        c) COMMIT="$OPTARG" ;;
        s) SECRET="$OPTARG" ;;
        r) REPO="$OPTARG" ;;
        h) usage ;;
        :) echo "error: -$OPTARG requires an argument" >&2; exit 1 ;;
       \?) echo "error: unknown option -$OPTARG" >&2; exit 1 ;;
    esac
done

# ── Build payload ─────────────────────────────────────────────────────────────
PAYLOAD="$(printf \
    '{"ref":"refs/heads/%s","before":"0000000000000000000000000000000000000000","after":"%s","repository":{"full_name":"%s"},"commits":[]}' \
    "$BRANCH" "$COMMIT" "$REPO")"

# ── Build curl arguments ──────────────────────────────────────────────────────
CURL_ARGS=(
    --silent
    --output /dev/null
    --write-out "%{http_code}"
    --request POST
    --header "Content-Type: application/json"
    --header "X-GitHub-Event: push"
    --header "X-GitHub-Delivery: local-$(date +%s)"
    --data "$PAYLOAD"
)

SIGNED="no"
if [[ -n "$SECRET" ]]; then
    if ! command -v openssl &>/dev/null; then
        echo "warning: openssl not found; sending without HMAC signature" >&2
    else
        SIG="$(printf '%s' "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $NF}')"
        CURL_ARGS+=(--header "X-Hub-Signature-256: sha256=$SIG")
        SIGNED="yes"
    fi
fi

# ── Send ──────────────────────────────────────────────────────────────────────
echo "Sending push webhook"
echo "  URL:    $URL"
echo "  Branch: $BRANCH"
echo "  Commit: $COMMIT"
echo "  Signed: $SIGNED"
echo

HTTP_STATUS="$(curl "${CURL_ARGS[@]}" "$URL")"

if [[ "$HTTP_STATUS" == "200" ]]; then
    echo "OK ($HTTP_STATUS)"
else
    echo "Failed ($HTTP_STATUS)" >&2
    exit 1
fi
