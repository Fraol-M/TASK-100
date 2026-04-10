#!/usr/bin/env bash
# run_test.sh — build a CGO-enabled test image and run all Go tests inside Docker.
#
# Usage:
#   ./run_test.sh                  # run all tests
#   ./run_test.sh ./internal/...   # run a specific package tree
#   ./run_test.sh -run TestName    # pass any 'go test' flags/patterns
#
# Requirements: Docker (daemon must be running)
# Exit code mirrors the test suite result (0 = pass, non-zero = failure).

set -euo pipefail

IMAGE_NAME="propertyops-test"
DOCKERFILE="deploy/Dockerfile.test"

# ── 1. Resolve repo root (directory containing this script) ──────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# ── 2. Build the test image ───────────────────────────────────────────────────
echo "==> Building test image: ${IMAGE_NAME}"
# BuildKit in some environments strips directory prefixes from --file.
# Workaround: cd into the directory that contains the Dockerfile and pass
# just the filename; use the repo root as the build context via a relative path.
(cd "${SCRIPT_DIR}/deploy" && docker build \
    --file Dockerfile.test \
    --tag  "${IMAGE_NAME}:latest" \
    ..)

# ── 3. Determine test target / extra flags ───────────────────────────────────
# If the caller passes arguments use them; otherwise test everything.
if [[ $# -gt 0 ]]; then
    TEST_ARGS=("$@")
else
    TEST_ARGS=("-v" "-race" "-count=1" "./...")
fi

# ── 4. Start a test database if integration tests need one ───────────────────
# The integration tests in ./test/ look for TEST_DSN in the environment.
# We spin up a throwaway MySQL container, wait for it to be healthy, run
# tests, then tear it down — all transparently to the caller.

DB_CONTAINER="propertyops-test-db-$$"
NETWORK_NAME="propertyops-test-net-$$"
MYSQL_ROOT_PASSWORD="testpass"
MYSQL_DATABASE="propertyops_test"
MYSQL_PORT=3307   # use a non-standard port to avoid clashing with local MySQL

cleanup() {
    echo ""
    echo "==> Cleaning up test containers"
    docker rm -f  "${DB_CONTAINER}"   2>/dev/null || true
    docker network rm "${NETWORK_NAME}" 2>/dev/null || true
}
trap cleanup EXIT

echo "==> Creating isolated Docker network: ${NETWORK_NAME}"
docker network create "${NETWORK_NAME}" >/dev/null

echo "==> Starting ephemeral MySQL container: ${DB_CONTAINER}"
docker run -d \
    --name "${DB_CONTAINER}" \
    --network "${NETWORK_NAME}" \
    -e MYSQL_ROOT_PASSWORD="${MYSQL_ROOT_PASSWORD}" \
    -e MYSQL_DATABASE="${MYSQL_DATABASE}" \
    -p "127.0.0.1:${MYSQL_PORT}:3306" \
    --health-cmd="mysqladmin ping -uroot -p${MYSQL_ROOT_PASSWORD} --silent" \
    --health-interval=2s \
    --health-timeout=5s \
    --health-retries=20 \
    mysql:8.0.36 \
    --default-authentication-plugin=mysql_native_password \
    >/dev/null

echo "==> Waiting for MySQL to be ready..."
for i in $(seq 1 60); do
    STATUS=$(docker inspect --format='{{.State.Health.Status}}' "${DB_CONTAINER}" 2>/dev/null || echo "starting")
    if [[ "$STATUS" == "healthy" ]]; then
        echo "    MySQL is ready (after ${i}s)"
        break
    fi
    if [[ $i -eq 60 ]]; then
        echo "ERROR: MySQL did not become healthy within 60 seconds." >&2
        exit 1
    fi
    sleep 1
done

# DSN passed to test/setup_test.go via TEST_MYSQL_DSN (the exact env var name
# the test helper reads). Schema creation is handled inside each test via
# GORM AutoMigrate — no separate migration binary run is needed.
TEST_MYSQL_DSN="root:${MYSQL_ROOT_PASSWORD}@tcp(${DB_CONTAINER}:3306)/${MYSQL_DATABASE}?parseTime=true&multiStatements=true"

# ── 5. Run the test suite ─────────────────────────────────────────────────────
echo "==> Running tests: go test ${TEST_ARGS[*]}"
docker run --rm \
    --network "${NETWORK_NAME}" \
    -e CGO_ENABLED=1 \
    -e TEST_MYSQL_DSN="${TEST_MYSQL_DSN}" \
    -e STORAGE_ROOT=/tmp/propertyops-test-storage \
    -e ENCRYPTION_KEY_DIR=/tmp/propertyops-test-keys \
    -e ENCRYPTION_ACTIVE_KEY_ID=1 \
    "${IMAGE_NAME}:latest" \
    go test "${TEST_ARGS[@]}"

echo ""
echo "==> All tests passed."
