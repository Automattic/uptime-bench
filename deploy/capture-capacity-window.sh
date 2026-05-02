#!/usr/bin/env bash
# Capture Jetmon v1/v2 Prometheus capacity metrics for a finished report run.
#
# Usage:
#   deploy/capture-capacity-window.sh reports/<run-tag> [output-dir]
#
# The script reads started_at_utc and finished_at_utc from run.meta.tsv and
# writes JSON and table summaries under <run-dir>/capacity by default.

set -euo pipefail

RUN_DIR="${1:-}"
OUT_DIR="${2:-}"

if [[ -z "$RUN_DIR" ]]; then
    echo "Usage: $0 <run-dir> [output-dir]" >&2
    exit 1
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CAPACITY_BIN="${CAPACITY_BIN:-${REPO_ROOT}/bin/uptime-bench-capacity}"
PROMETHEUS_URL="${PROMETHEUS_URL:-http://10.0.0.67:9091}"
CAPACITY_INSTANCES="${CAPACITY_INSTANCES:-jetmon-service-host-1,jetmon-service-host-2}"
CAPACITY_STEP="${CAPACITY_STEP:-15s}"
CAPACITY_RATE_WINDOW="${CAPACITY_RATE_WINDOW:-2m}"
CAPACITY_POSTRUN_DURATION="${CAPACITY_POSTRUN_DURATION:-15m}"

if [[ ! -f "${RUN_DIR}/run.meta.tsv" ]]; then
    echo "capacity-capture: ${RUN_DIR}/run.meta.tsv not found" >&2
    exit 1
fi

if [[ ! -x "$CAPACITY_BIN" ]]; then
    echo "capacity-capture: building ${CAPACITY_BIN}"
    make -C "$REPO_ROOT" "$CAPACITY_BIN"
fi

started_at="$(awk -F '\t' '$1 == "started_at_utc" { print $2 }' "${RUN_DIR}/run.meta.tsv")"
finished_at="$(awk -F '\t' '$1 == "finished_at_utc" { print $2 }' "${RUN_DIR}/run.meta.tsv")"
run_tag="$(awk -F '\t' '$1 == "run_tag" { print $2 }' "${RUN_DIR}/run.meta.tsv")"

if [[ -z "$started_at" || -z "$finished_at" ]]; then
    echo "capacity-capture: run.meta.tsv must include started_at_utc and finished_at_utc" >&2
    exit 1
fi

if [[ -z "$OUT_DIR" ]]; then
    OUT_DIR="${RUN_DIR}/capacity"
fi
mkdir -p "$OUT_DIR"

echo "capacity-capture: run=${run_tag:-$(basename "$RUN_DIR")}"
echo "capacity-capture: window=${started_at}..${finished_at}"
echo "capacity-capture: prometheus=${PROMETHEUS_URL}"

"$CAPACITY_BIN" \
    -prometheus-url="$PROMETHEUS_URL" \
    -instances="$CAPACITY_INSTANCES" \
    -start="$started_at" \
    -end="$finished_at" \
    -step="$CAPACITY_STEP" \
    -rate-window="$CAPACITY_RATE_WINDOW" \
    -format=json \
    > "${OUT_DIR}/prometheus-window.json"

"$CAPACITY_BIN" \
    -prometheus-url="$PROMETHEUS_URL" \
    -instances="$CAPACITY_INSTANCES" \
    -start="$started_at" \
    -end="$finished_at" \
    -step="$CAPACITY_STEP" \
    -rate-window="$CAPACITY_RATE_WINDOW" \
    -format=table \
    > "${OUT_DIR}/prometheus-window.txt"

if [[ "$CAPACITY_POSTRUN_DURATION" != "0" && "$CAPACITY_POSTRUN_DURATION" != "none" ]]; then
    "$CAPACITY_BIN" \
        -prometheus-url="$PROMETHEUS_URL" \
        -instances="$CAPACITY_INSTANCES" \
        -duration="$CAPACITY_POSTRUN_DURATION" \
        -step="$CAPACITY_STEP" \
        -rate-window="$CAPACITY_RATE_WINDOW" \
        -format=json \
        > "${OUT_DIR}/prometheus-postrun-${CAPACITY_POSTRUN_DURATION}.json"

    "$CAPACITY_BIN" \
        -prometheus-url="$PROMETHEUS_URL" \
        -instances="$CAPACITY_INSTANCES" \
        -duration="$CAPACITY_POSTRUN_DURATION" \
        -step="$CAPACITY_STEP" \
        -rate-window="$CAPACITY_RATE_WINDOW" \
        -format=table \
        > "${OUT_DIR}/prometheus-postrun-${CAPACITY_POSTRUN_DURATION}.txt"
fi

echo "capacity-capture: wrote ${OUT_DIR}"
