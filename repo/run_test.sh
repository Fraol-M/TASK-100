#!/usr/bin/env bash
# run_test.sh — build a CGO-enabled test image and run Go tests inside Docker.
#
# Usage:
#   ./run_test.sh                     # run the FULL suite (./...) — includes integration tests
#   ./run_test.sh --fast              # run fundamental tests only (./cmd/... ./internal/...)
#   ./run_test.sh --fundamental       # alias for --fast
#   ./run_test.sh --core              # alias for --fast
#   ./run_test.sh --full              # explicit full mode (alias of default)
#   ./run_test.sh --coverage          # add coverage instrumentation + print a
#                                     # per-package breakdown and total % after
#                                     # the run. Combines with --full/--fast.
#   ./run_test.sh ./internal/...      # run a specific package tree (custom mode)
#   ./run_test.sh -run TestName       # pass any 'go test' flags/patterns
#                                     # (custom mode keeps previous behavior)
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
# Default mode is "full" so the bare `./run_test.sh` invocation runs every
# package, including the integration suite under ./test/...
# Use --fast / --fundamental / --core to opt out of the integration flows.
# --coverage instruments the run and prints a summary after tests complete.
RUN_MODE="full"
COLLECT_COVERAGE=0
while [[ $# -gt 0 ]]; do
    case "${1}" in
        --full)
            RUN_MODE="full"; shift ;;
        --fast|--fundamental|--core)
            RUN_MODE="fundamental"; shift ;;
        --coverage|--cover)
            COLLECT_COVERAGE=1; shift ;;
        *)
            break ;;
    esac
done

if [[ $# -gt 0 ]]; then
    # Custom mode: preserve caller-provided args exactly.
    TEST_ARGS=("$@")
    NEEDS_MYSQL=1
    echo "==> Test mode: custom"
elif [[ "$RUN_MODE" == "full" ]]; then
    TEST_ARGS=("-v" "-count=1" "-failfast" "-timeout=8m" "./...")
    NEEDS_MYSQL=1
    echo "==> Test mode: full"
else
    # Fundamental mode excludes ./test integration flows.
    TEST_ARGS=("-v" "-count=1" "-failfast" "-timeout=8m" "./cmd/..." "./internal/...")
    NEEDS_MYSQL=0
    echo "==> Test mode: fundamental"
fi

# Coverage flags are injected here so they apply to any mode (full/fast/custom).
# -coverpkg=./internal/... ensures integration tests in ./test/... attribute
# their coverage to the internal packages they exercise (handlers, services,
# repositories), not to the test package itself.
COVERAGE_FILE="/tmp/propertyops-cov.out"
if [[ "${COLLECT_COVERAGE}" -eq 1 ]]; then
    TEST_ARGS+=("-coverpkg=./internal/...")
    TEST_ARGS+=("-coverprofile=${COVERAGE_FILE}")
    echo "==> Coverage: ENABLED (profile will be written to ${COVERAGE_FILE} inside container)"
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
    if [[ "${NEEDS_MYSQL}" -eq 1 ]]; then
        echo ""
        echo "==> Cleaning up test containers"
        docker rm -f  "${DB_CONTAINER}"   2>/dev/null || true
        docker network rm "${NETWORK_NAME}" 2>/dev/null || true
    fi
}
trap cleanup EXIT

if [[ "${NEEDS_MYSQL}" -eq 1 ]]; then
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
fi

# ── 5. Run the test suite ─────────────────────────────────────────────────────
# Filter out "[no test files]" lines for packages with no tests (cmd/api, etc.)
# so the output focuses on packages that actually ran something. awk always
# exits 0, so pipefail still reports a real go-test failure.
#
# When --coverage is set, run the tests AND go-tool-cover summary in a single
# container invocation so the profile (written inside the container) is still
# available when we format the summary.
echo "==> Running tests: go test ${TEST_ARGS[*]}"
build_container_cmd() {
    # Reassemble a shell-safe command string from TEST_ARGS so we can chain
    # commands inside sh -c when coverage is enabled.
    local quoted
    quoted="$(printf '%q ' "${TEST_ARGS[@]}")"
    if [[ "${COLLECT_COVERAGE}" -eq 1 ]]; then
        # Run tests; if they pass, print per-function + total coverage.
        # `tail -n 1` on the -func output yields the "total:" line.
        printf 'go test %s; rc=$?; if [ -f %q ]; then echo ""; echo "==> Coverage (per package, top 20):"; go tool cover -func=%q | grep -E "^\S" | tail -n 20; echo ""; echo -n "==> Total coverage: "; go tool cover -func=%q | tail -n 1; fi; exit $rc' \
            "$quoted" "$COVERAGE_FILE" "$COVERAGE_FILE" "$COVERAGE_FILE"
    else
        printf 'go test %s' "$quoted"
    fi
}
CONTAINER_CMD="$(build_container_cmd)"

if [[ "${NEEDS_MYSQL}" -eq 1 ]]; then
    docker run --rm \
        --network "${NETWORK_NAME}" \
        -e CGO_ENABLED=1 \
        -e TEST_MYSQL_DSN="${TEST_MYSQL_DSN}" \
        -e STORAGE_ROOT=/tmp/propertyops-test-storage \
        -e ENCRYPTION_KEY_DIR=/tmp/propertyops-test-keys \
        -e ENCRYPTION_ACTIVE_KEY_ID=1 \
        "${IMAGE_NAME}:latest" \
        sh -c "${CONTAINER_CMD}" | awk '!/\[no test files\]/'
else
    docker run --rm \
        -e CGO_ENABLED=1 \
        -e STORAGE_ROOT=/tmp/propertyops-test-storage \
        -e ENCRYPTION_KEY_DIR=/tmp/propertyops-test-keys \
        -e ENCRYPTION_ACTIVE_KEY_ID=1 \
        "${IMAGE_NAME}:latest" \
        sh -c "${CONTAINER_CMD}" | awk '!/\[no test files\]/'
fi

echo ""
echo "==> All tests passed."
