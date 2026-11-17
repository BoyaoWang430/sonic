#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ISSUE_DIR="${ROOT_DIR}/issue_test"
RACE_GO="${ISSUE_DIR}/race_test.go"
LOG_FILE="$(mktemp "${TMPDIR:-/tmp}/sonic_test_race.XXXXXX.log")"

cleanup() {
    rm -f "${RACE_GO}" "${LOG_FILE}"
}
trap cleanup EXIT

cp "${ISSUE_DIR}/race_test_go" "${RACE_GO}"

set +e
(
    cd "${ISSUE_DIR}"
    go test -v -run='^TestRaceEncode$' -race -count=100 .
) >"${LOG_FILE}" 2>&1
rc=$?
set -e

if [ "${rc}" -eq 0 ]; then
    echo "TEST FAILED: expected go test -race to exit non-zero on intentional data race"
    cat "${LOG_FILE}"
    exit 1
fi

if grep -qE '^FAIL.*\[(build|setup) failed\]|^panic:' "${LOG_FILE}"; then
    echo "TEST FAILED: unexpected build failure or panic during race test (exit=${rc})"
    cat "${LOG_FILE}"
    exit 1
fi

if ! grep -q "WARNING: DATA RACE" "${LOG_FILE}"; then
    echo "TEST FAILED: expected WARNING: DATA RACE in output (exit=${rc})"
    cat "${LOG_FILE}"
    exit 1
fi

if ! grep -q "github.com/bytedance/sonic" "${LOG_FILE}"; then
    echo "TEST FAILED: expected data race stack inside github.com/bytedance/sonic"
    cat "${LOG_FILE}"
    exit 1
fi

if grep -q "encoding/json.Marshal" "${LOG_FILE}"; then
    echo "TEST FAILED: race detection must not rely on secondary encoding/json.Marshal"
    cat "${LOG_FILE}"
    exit 1
fi

echo "PASS: TestRaceEncode detected real Sonic data race with single callback execution (exit=${rc})"
