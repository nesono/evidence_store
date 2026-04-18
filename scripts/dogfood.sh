#!/usr/bin/env bash
set -euo pipefail

# Dogfood script: run Bazel tests, then ingest results into the Evidence Store.
#
# Usage:
#   ./scripts/dogfood.sh                    # full run against localhost:8000
#   ./scripts/dogfood.sh --dry-run          # just show what would be uploaded
#   EVIDENCE_STORE_URL=https://... ./scripts/dogfood.sh  # custom endpoint

API_URL="${EVIDENCE_STORE_URL:-http://localhost:8000}"
EXTRA_ARGS=("$@")

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ADAPTER_DIR="$REPO_ROOT/adapters/bazel"

# Build the adapter binary from its own Bzlmod module.
echo "==> Building evidence-bazel adapter..."
(cd "$ADAPTER_DIR" && bazel build //cmd/evidence-bazel)
ADAPTER_BIN="$(cd "$ADAPTER_DIR" && bazel cquery --output=files //cmd/evidence-bazel 2>/dev/null)"
# cquery returns a path relative to the adapter workspace.
ADAPTER_BIN="$ADAPTER_DIR/$ADAPTER_BIN"

# Run Bazel tests in the root workspace with a shared invocation ID.
INVOCATION_ID="$(uuidgen)"
echo "==> Running bazel test //... (invocation: $INVOCATION_ID)"
(cd "$REPO_ROOT" && bazel test //... --invocation_id="$INVOCATION_ID") || true  # continue even if tests fail

# Determine testlogs path for the root workspace.
TESTLOGS_DIR="$(cd "$REPO_ROOT" && bazel info bazel-testlogs)"

# Ingest results.
echo "==> Ingesting test results..."
"$ADAPTER_BIN" \
    --api-url "$API_URL" \
    --testlogs-dir "$TESTLOGS_DIR" \
    --invocation-id "$INVOCATION_ID" \
    "${EXTRA_ARGS[@]+${EXTRA_ARGS[@]}}"
