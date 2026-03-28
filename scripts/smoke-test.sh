#!/usr/bin/env bash
set -euo pipefail
set -x

BASE_URL="${EVIDENCE_BASE_URL:-http://localhost:8000}"

# Colors for output.
green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }

check() {
    local description="$1" expected_status="$2"
    shift 2
    local response status body
    response=$(curl -s -w '\n%{http_code}' "$@")
    status=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [ "$status" = "$expected_status" ]; then
        green "✓ $description (HTTP $status)" >&2
    else
        red "✗ $description (expected $expected_status, got $status)" >&2
        echo "$body" | jq . 2>/dev/null || echo "$body" >&2
        exit 1
    fi
    echo "$body" | jq . 2>/dev/null || echo "$body" >&2
    echo >&2
    # Return raw body on stdout for capture.
    echo "$body"
}

# -------------------------------------------------------------------
bold "=== Health Check ==="
check "Health check" 200 "$BASE_URL/healthz" > /dev/null

# -------------------------------------------------------------------
bold "=== Single Ingest ==="
SINGLE=$(check "Create single evidence" 201 \
    -X POST "$BASE_URL/api/v1/evidence" \
    -H 'Content-Type: application/json' \
    -d '{
        "repo": "nesono/firmware",
        "branch": "main",
        "rcs_ref": "abc123",
        "procedure_ref": "//pkg:smoke_test",
        "evidence_type": "bazel",
        "source": "jdoe",
        "result": "PASS",
        "finished_at": "2026-03-28T10:00:00Z",
        "metadata": {"duration_s": 2.5, "tags": ["smoke"]}
    }')
EVIDENCE_ID=$(echo "$SINGLE" | jq -r '.id')

# -------------------------------------------------------------------
bold "=== Get by ID ==="
check "Get evidence by ID" 200 "$BASE_URL/api/v1/evidence/$EVIDENCE_ID" > /dev/null

# -------------------------------------------------------------------
bold "=== Batch Ingest ==="
check "Batch ingest (all valid)" 201 \
    -X POST "$BASE_URL/api/v1/evidence/batch" \
    -H 'Content-Type: application/json' \
    -d '{
        "records": [
            {"repo":"nesono/firmware","branch":"main","rcs_ref":"abc123","procedure_ref":"//pkg:unit_test","evidence_type":"bazel","source":"jdoe","result":"PASS","finished_at":"2026-03-28T10:01:00Z","metadata":{"duration_s":1.2}},
            {"repo":"nesono/firmware","branch":"main","rcs_ref":"abc123","procedure_ref":"//pkg:integration_test","evidence_type":"bazel","source":"jdoe","result":"FAIL","finished_at":"2026-03-28T10:02:00Z","metadata":{"duration_s":5.8}},
            {"repo":"nesono/firmware","branch":"main","rcs_ref":"abc123","procedure_ref":"//lib:math_test","evidence_type":"bazel","source":"jdoe","result":"ERROR","finished_at":"2026-03-28T10:03:00Z"}
        ]
    }' > /dev/null

check "Batch ingest (partial failure → 207)" 207 \
    -X POST "$BASE_URL/api/v1/evidence/batch" \
    -H 'Content-Type: application/json' \
    -d '{
        "records": [
            {"repo":"nesono/firmware","branch":"main","rcs_ref":"abc123","procedure_ref":"//pkg:good_test","evidence_type":"bazel","source":"jdoe","result":"PASS","finished_at":"2026-03-28T10:04:00Z"},
            {"repo":"","branch":"","rcs_ref":"","procedure_ref":"","evidence_type":"","source":"","result":"","finished_at":"0001-01-01T00:00:00Z"}
        ]
    }' > /dev/null

# -------------------------------------------------------------------
bold "=== Query / Filters ==="
check "List all for repo" 200 "$BASE_URL/api/v1/evidence?repo=nesono/firmware" > /dev/null
check "Filter by result=FAIL" 200 "$BASE_URL/api/v1/evidence?repo=nesono/firmware&result=FAIL" > /dev/null
check "Filter by result=FAIL,ERROR" 200 "$BASE_URL/api/v1/evidence?repo=nesono/firmware&result=FAIL,ERROR" > /dev/null
check "Filter by procedure_ref prefix" 200 "$BASE_URL/api/v1/evidence?repo=nesono/firmware&procedure_ref=//pkg:*" > /dev/null

# -------------------------------------------------------------------
bold "=== Pagination ==="
PAGE1=$(check "Page 1 (limit=2)" 200 "$BASE_URL/api/v1/evidence?repo=nesono/firmware&limit=2")
CURSOR=$(echo "$PAGE1" | jq -r '.next_cursor // empty')
if [ -n "$CURSOR" ]; then
    check "Page 2 (cursor)" 200 "$BASE_URL/api/v1/evidence?repo=nesono/firmware&limit=2&cursor=$CURSOR" > /dev/null
else
    bold "  (no second page needed)"
fi

# -------------------------------------------------------------------
bold "=== Inheritance ==="
check "Create inheritance declaration" 201 \
    -X POST "$BASE_URL/api/v1/inheritance" \
    -H 'Content-Type: application/json' \
    -d '{
        "repo": "nesono/firmware",
        "source_rcs_ref": "abc123",
        "target_rcs_ref": "def456",
        "scope": ["//pkg:*"],
        "justification": "Only README changed, no code diffs in pkg/",
        "created_by": "jdoe"
    }' > /dev/null

check "Query inherited evidence (include_inherited=true)" 200 \
    "$BASE_URL/api/v1/evidence?repo=nesono/firmware&rcs_ref=def456" > /dev/null

check "Query without inheritance (include_inherited=false)" 200 \
    "$BASE_URL/api/v1/evidence?repo=nesono/firmware&rcs_ref=def456&include_inherited=false" > /dev/null

check "List inheritance declarations" 200 \
    "$BASE_URL/api/v1/inheritance?repo=nesono/firmware" > /dev/null

# -------------------------------------------------------------------
bold "=== Validation Errors ==="
check "Missing required fields → 422" 422 \
    -X POST "$BASE_URL/api/v1/evidence" \
    -H 'Content-Type: application/json' \
    -d '{"result": "PASS"}' > /dev/null

check "Invalid evidence_type → 422" 422 \
    -X POST "$BASE_URL/api/v1/evidence" \
    -H 'Content-Type: application/json' \
    -d '{
        "repo":"r","branch":"b","rcs_ref":"c","procedure_ref":"p",
        "evidence_type":"INVALID-TYPE!","source":"s","result":"PASS",
        "finished_at":"2026-03-28T10:00:00Z"
    }' > /dev/null

check "Not found → 404" 404 \
    "$BASE_URL/api/v1/evidence/00000000-0000-0000-0000-000000000000" > /dev/null

# -------------------------------------------------------------------
green ""
green "=== All smoke tests passed! ==="
