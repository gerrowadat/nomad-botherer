#!/usr/bin/env bash
# Outputs workspace status variables consumed by Bazel stamping.
# Invoked by 'bazel build --config=release' via .bazelrc.
# Stable variables (STABLE_*) invalidate cached outputs when they change.
set -euo pipefail

echo "STABLE_GIT_COMMIT $(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
echo "STABLE_VERSION $(git describe --tags --always --dirty 2>/dev/null || echo dev)"
echo "BUILD_TIMESTAMP $(date -u +%Y-%m-%dT%H:%M:%SZ)"
