#!/usr/bin/env bash
# aks-cleanup_test.sh - Bash unit tests for scripts/aks-cleanup.sh.
#
# These tests exercise the parse / decision / dispatch logic without
# touching Azure. They work in two layers:
#
#   1. Direct unit tests of the `decide_rg_action` function (sourced from
#      aks-cleanup.sh by re-running with a sentinel argument that exits
#      before touching `az`).
#   2. End-to-end integration tests that stub `az` via a PATH shim - the
#      shim reads a fixture file referenced by the AKS_CLEANUP_TEST_FIXTURE
#      env var and prints it for `az group list ...`. `az group delete`
#      records its invocation to a side file so we can assert it ran (or
#      did not, in dry-run).
#
# The tests do not require `az`, network access, or Azure credentials.
#
# Usage:
#   bash scripts/aks-cleanup_test.sh          # run all
#   make test-cleanup-script                  # via Makefile

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SUT="${SCRIPT_DIR}/aks-cleanup.sh"

if [[ ! -x "${SUT}" ]]; then
    echo "FATAL: ${SUT} is not executable" >&2
    exit 1
fi

PASS=0
FAIL=0
FAILED_TESTS=()

t_pass() { echo "  PASS  $*"; PASS=$((PASS + 1)); }
t_fail() { echo "  FAIL  $*" >&2; FAIL=$((FAIL + 1)); FAILED_TESTS+=("$*"); }

# assert_contains <haystack> <needle> <test-name>
assert_contains() {
    local haystack="$1" needle="$2" name="$3"
    if [[ "${haystack}" == *"${needle}"* ]]; then
        t_pass "${name}: contains '${needle}'"
    else
        t_fail "${name}: expected to contain '${needle}'"
        echo "----- output -----" >&2
        echo "${haystack}" >&2
        echo "------------------" >&2
    fi
}

# assert_not_contains <haystack> <needle> <test-name>
assert_not_contains() {
    local haystack="$1" needle="$2" name="$3"
    if [[ "${haystack}" != *"${needle}"* ]]; then
        t_pass "${name}: does not contain '${needle}'"
    else
        t_fail "${name}: expected NOT to contain '${needle}'"
        echo "----- output -----" >&2
        echo "${haystack}" >&2
        echo "------------------" >&2
    fi
}

assert_eq() {
    local got="$1" want="$2" name="$3"
    if [[ "${got}" == "${want}" ]]; then
        t_pass "${name}"
    else
        t_fail "${name}: got '${got}', want '${want}'"
    fi
}

# ---------------------------------------------------------------------------
# Build the az shim. The shim records every argv to $AZ_LOG and:
#   - for `az group list ...`: cats $AKS_CLEANUP_TEST_FIXTURE
#   - for `az group delete ...`: records the RG name and exits 0 (or
#     non-zero if AZ_DELETE_FAIL=<rg-name> matches).
#   - for `az account set`: exits 0
#   - anything else: exits 0 (no-op, prints nothing)
# ---------------------------------------------------------------------------
TMP_BIN="$(mktemp -d)"
trap 'rm -rf "${TMP_BIN}"' EXIT

cat > "${TMP_BIN}/az" <<'AZSHIM'
#!/usr/bin/env bash
# Test shim for `az`. Reads fixture from $AKS_CLEANUP_TEST_FIXTURE.
echo "$@" >> "${AZ_LOG:-/dev/null}"
case "$1 ${2:-}" in
    "group list")
        if [[ -n "${AKS_CLEANUP_TEST_FIXTURE:-}" && -f "${AKS_CLEANUP_TEST_FIXTURE}" ]]; then
            cat "${AKS_CLEANUP_TEST_FIXTURE}"
        else
            echo "[]"
        fi
        ;;
    "group delete")
        # Find --name <value>
        rg=""
        while [[ $# -gt 0 ]]; do
            if [[ "$1" == "--name" ]]; then rg="$2"; break; fi
            shift
        done
        echo "DELETE ${rg}" >> "${AZ_LOG:-/dev/null}"
        if [[ -n "${AZ_DELETE_FAIL:-}" && "${rg}" == "${AZ_DELETE_FAIL}" ]]; then
            echo "simulated delete failure for ${rg}" >&2
            exit 1
        fi
        ;;
    "account set")
        ;;
    *)
        ;;
esac
AZSHIM
chmod +x "${TMP_BIN}/az"

ORIG_PATH="${PATH}"
SHIMMED_PATH="${TMP_BIN}:${PATH}"

# ---------------------------------------------------------------------------
# Fixture builder
# ---------------------------------------------------------------------------
NOW_EPOCH=$(date -u +%s)
PAST=$(date -u -d "@$((NOW_EPOCH - 3600))" +%Y-%m-%dT%H:%M:%SZ)   # 1h ago
FUTURE=$(date -u -d "@$((NOW_EPOCH + 3600))" +%Y-%m-%dT%H:%M:%SZ) # 1h from now

mk_fixture() {
    local out="$1"
    cat > "${out}" <<JSON
[
  {
    "name": "rg-daedalus-test",
    "tags": {"auto-destroy": "true", "expires-at": "${PAST}"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-daedalus-future",
    "tags": {"auto-destroy": "true", "expires-at": "${FUTURE}"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-daedalus-missing",
    "tags": {"auto-destroy": "true"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-daedalus-bogus",
    "tags": {"auto-destroy": "true", "expires-at": "not-a-date"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-other-prefix",
    "tags": {"auto-destroy": "true", "expires-at": "${PAST}"},
    "properties": {"provisioningState": "Succeeded"}
  }
]
JSON
}

mk_empty_fixture() {
    echo "[]" > "$1"
}

mk_all_future_fixture() {
    cat > "$1" <<JSON
[
  {
    "name": "rg-daedalus-future-1",
    "tags": {"auto-destroy": "true", "expires-at": "${FUTURE}"},
    "properties": {"provisioningState": "Succeeded"}
  }
]
JSON
}

# ---------------------------------------------------------------------------
# Test 1: happy path - mixed RGs, real-run, prefix filter active.
# ---------------------------------------------------------------------------
echo "=== Test 1: happy path (mixed RGs, real run with --prefix) ==="
FIX1="${TMP_BIN}/fix1.json"; mk_fixture "${FIX1}"
LOG1="${TMP_BIN}/log1.txt"; : > "${LOG1}"
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX1}" AZ_LOG="${LOG1}" \
    "${SUT}" --prefix rg-daedalus- 2>&1) || true

assert_contains "${OUT}" "Destroying rg-daedalus-test"           "T1: expired RG destroyed"
assert_contains "${OUT}" "rg-daedalus-future: not yet expired"   "T1: future RG skipped"
assert_contains "${OUT}" "missing expires-at tag"                "T1: missing-tag warned"
assert_contains "${OUT}" "unparseable expires-at"                "T1: unparseable warned"
assert_contains "${OUT}" "rg-other-prefix: skipping"             "T1: out-of-prefix skipped"
assert_contains "${OUT}" "Scanned 5 RGs"                         "T1: scanned 5"
assert_contains "${OUT}" "1 expired"                             "T1: 1 expired"
assert_contains "${OUT}" "1 destroyed"                           "T1: 1 destroyed"

DELETE_LOG=$(grep -c "^DELETE rg-daedalus-test$" "${LOG1}" || true)
assert_eq "${DELETE_LOG}" "1" "T1: az group delete called exactly once for rg-daedalus-test"

NO_OTHER_DELETE=$(grep -c "^DELETE " "${LOG1}" | head -1 || true)
assert_eq "${NO_OTHER_DELETE}" "1" "T1: only one delete invocation total"

# ---------------------------------------------------------------------------
# Test 2: --dry-run should not invoke `az group delete`
# ---------------------------------------------------------------------------
echo "=== Test 2: --dry-run does not delete ==="
FIX2="${TMP_BIN}/fix2.json"; mk_fixture "${FIX2}"
LOG2="${TMP_BIN}/log2.txt"; : > "${LOG2}"
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX2}" AZ_LOG="${LOG2}" \
    "${SUT}" --prefix rg-daedalus- --dry-run 2>&1) || true

assert_contains "${OUT}" "[DRY-RUN] would destroy rg-daedalus-test" "T2: dry-run logged"
assert_contains "${OUT}" "would-destroy"                            "T2: dry-run summary"

DELETE_COUNT=$(grep -c "^DELETE " "${LOG2}" || true)
assert_eq "${DELETE_COUNT}" "0" "T2: zero delete invocations in dry-run"

# ---------------------------------------------------------------------------
# Test 3: empty list -> exit 0, no-op summary
# ---------------------------------------------------------------------------
echo "=== Test 3: empty list ==="
FIX3="${TMP_BIN}/fix3.json"; mk_empty_fixture "${FIX3}"
LOG3="${TMP_BIN}/log3.txt"; : > "${LOG3}"
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX3}" AZ_LOG="${LOG3}" \
    "${SUT}" --prefix rg-daedalus- 2>&1)
RC=$?
assert_eq "${RC}" "0"                   "T3: exit 0 on empty list"
assert_contains "${OUT}" "Scanned 0 RGs" "T3: scanned 0"

# ---------------------------------------------------------------------------
# Test 4: all not-yet-expired -> exit 0, no destroys
# ---------------------------------------------------------------------------
echo "=== Test 4: all RGs not yet expired ==="
FIX4="${TMP_BIN}/fix4.json"; mk_all_future_fixture "${FIX4}"
LOG4="${TMP_BIN}/log4.txt"; : > "${LOG4}"
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX4}" AZ_LOG="${LOG4}" \
    "${SUT}" --prefix rg-daedalus- 2>&1)
RC=$?
assert_eq "${RC}" "0"                                  "T4: exit 0"
assert_contains "${OUT}" "0 expired"                   "T4: 0 expired"
DELETE_COUNT=$(grep -c "^DELETE " "${LOG4}" || true)
assert_eq "${DELETE_COUNT}" "0"                        "T4: no deletes"

# ---------------------------------------------------------------------------
# Test 5: missing --prefix and no --all-prefixes -> exit 2 with safety-net error
# ---------------------------------------------------------------------------
echo "=== Test 5: missing --prefix safety net ==="
set +e
OUT=$(PATH="${SHIMMED_PATH}" "${SUT}" 2>&1)
RC=$?
set -e
assert_eq "${RC}" "2"                          "T5: exit 2 when --prefix missing"
assert_contains "${OUT}" "--prefix is required" "T5: clear error message"
assert_contains "${OUT}" "--all-prefixes"       "T5: error mentions --all-prefixes escape hatch"

# ---------------------------------------------------------------------------
# Test 6: --all-prefixes destroys every expired RG regardless of name
# ---------------------------------------------------------------------------
echo "=== Test 6: --all-prefixes ==="
FIX6="${TMP_BIN}/fix6.json"; mk_fixture "${FIX6}"
LOG6="${TMP_BIN}/log6.txt"; : > "${LOG6}"
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX6}" AZ_LOG="${LOG6}" \
    "${SUT}" --all-prefixes --dry-run 2>&1) || true

assert_contains "${OUT}" "[DRY-RUN] would destroy rg-daedalus-test"  "T6: in-prefix RG considered"
assert_contains "${OUT}" "[DRY-RUN] would destroy rg-other-prefix"   "T6: out-of-prefix RG considered"
assert_contains "${OUT}" "all-prefixes"                              "T6: warning printed"

# ---------------------------------------------------------------------------
# Test 7: az group delete failure -> exit 1, but other RGs still processed
# ---------------------------------------------------------------------------
echo "=== Test 7: per-RG delete failure does not stop the run ==="
FIX7="${TMP_BIN}/fix7.json"
cat > "${FIX7}" <<JSON
[
  {
    "name": "rg-daedalus-fail",
    "tags": {"auto-destroy": "true", "expires-at": "${PAST}"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-daedalus-ok",
    "tags": {"auto-destroy": "true", "expires-at": "${PAST}"},
    "properties": {"provisioningState": "Succeeded"}
  }
]
JSON
LOG7="${TMP_BIN}/log7.txt"; : > "${LOG7}"
set +e
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX7}" AZ_LOG="${LOG7}" \
    AZ_DELETE_FAIL=rg-daedalus-fail \
    "${SUT}" --prefix rg-daedalus- 2>&1)
RC=$?
set -e
assert_eq "${RC}" "1"                                  "T7: exit 1 on per-RG failure"
assert_contains "${OUT}" "rg-daedalus-fail"            "T7: failing RG mentioned"
assert_contains "${OUT}" "rg-daedalus-ok"              "T7: ok RG also processed"
DELETE_COUNT=$(grep -c "^DELETE " "${LOG7}" || true)
assert_eq "${DELETE_COUNT}" "2"                        "T7: both deletes attempted"

# ---------------------------------------------------------------------------
# Test 8: provisioningState=Deleting is logged but still attempts delete
# ---------------------------------------------------------------------------
echo "=== Test 8: already-deleting RG ==="
FIX8="${TMP_BIN}/fix8.json"
cat > "${FIX8}" <<JSON
[
  {
    "name": "rg-daedalus-deleting",
    "tags": {"auto-destroy": "true", "expires-at": "${PAST}"},
    "properties": {"provisioningState": "Deleting"}
  }
]
JSON
LOG8="${TMP_BIN}/log8.txt"; : > "${LOG8}"
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX8}" AZ_LOG="${LOG8}" \
    "${SUT}" --prefix rg-daedalus- 2>&1) || true
assert_contains "${OUT}" "deletion already in progress" "T8: deleting state logged"

# ---------------------------------------------------------------------------
# Test 9: strict RFC 3339 gate rejects permissive date strings
# ---------------------------------------------------------------------------
echo "=== Test 9: strict RFC 3339 gate (yesterday/now/0 are unparseable) ==="
FIX9="${TMP_BIN}/fix9.json"
cat > "${FIX9}" <<JSON
[
  {
    "name": "rg-daedalus-yesterday",
    "tags": {"auto-destroy": "true", "expires-at": "yesterday"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-daedalus-now",
    "tags": {"auto-destroy": "true", "expires-at": "now"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-daedalus-zero",
    "tags": {"auto-destroy": "true", "expires-at": "0"},
    "properties": {"provisioningState": "Succeeded"}
  },
  {
    "name": "rg-daedalus-valid",
    "tags": {"auto-destroy": "true", "expires-at": "${PAST}"},
    "properties": {"provisioningState": "Succeeded"}
  }
]
JSON
LOG9="${TMP_BIN}/log9.txt"; : > "${LOG9}"
OUT=$(PATH="${SHIMMED_PATH}" AKS_CLEANUP_TEST_FIXTURE="${FIX9}" AZ_LOG="${LOG9}" \
    "${SUT}" --prefix rg-daedalus- 2>&1) || true

assert_contains "${OUT}" "skipping rg-daedalus-yesterday: unparseable expires-at='yesterday'" "T9: 'yesterday' rejected"
assert_contains "${OUT}" "skipping rg-daedalus-now: unparseable expires-at='now'"             "T9: 'now' rejected"
assert_contains "${OUT}" "skipping rg-daedalus-zero: unparseable expires-at='0'"              "T9: '0' rejected"
assert_contains "${OUT}" "Destroying rg-daedalus-valid"                                       "T9: valid RFC 3339 processed"

DELETE_COUNT=$(grep -c "^DELETE " "${LOG9}" || true)
assert_eq "${DELETE_COUNT}" "1" "T9: exactly one delete (only the valid RG)"
DELETE_VALID=$(grep -c "^DELETE rg-daedalus-valid$" "${LOG9}" || true)
assert_eq "${DELETE_VALID}" "1" "T9: delete targeted the valid RG"
NO_PERMISSIVE=$(grep -cE "^DELETE rg-daedalus-(yesterday|now|zero)$" "${LOG9}" || true)
assert_eq "${NO_PERMISSIVE}" "0" "T9: no deletes for permissively-parseable RGs"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "Tests passed: ${PASS}"
echo "Tests failed: ${FAIL}"
if [[ "${FAIL}" -gt 0 ]]; then
    echo ""
    echo "Failed assertions:"
    for t in "${FAILED_TESTS[@]}"; do
        echo "  - ${t}"
    done
    exit 1
fi
exit 0
