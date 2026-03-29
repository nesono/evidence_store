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
# Build the adapter with Bazel.
echo "==> Building evidence-bazel adapter..."
bazel build //adapters/bazel/cmd/evidence-bazel

ADAPTER_BIN="$(bazel cquery --output=files //adapters/bazel/cmd/evidence-bazel 2>/dev/null)"

# Run Bazel tests with a shared invocation ID.
INVOCATION_ID="$(uuidgen)"
echo "==> Running bazel test //... (invocation: $INVOCATION_ID)"
bazel test //... --invocation_id="$INVOCATION_ID" || true  # continue even if tests fail

# Determine testlogs path.
TESTLOGS_DIR="$(bazel info bazel-testlogs)"

# Ingest results.
echo "==> Ingesting test results..."
"$ADAPTER_BIN" \
    --api-url "$API_URL" \
    --testlogs-dir "$TESTLOGS_DIR" \
    --invocation-id "$INVOCATION_ID" \
    "${EXTRA_ARGS[@]+${EXTRA_ARGS[@]}}"
