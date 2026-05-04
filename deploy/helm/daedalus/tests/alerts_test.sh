#!/usr/bin/env bash
# Helm chart tests for the Daedalus alerting templates.
#
# These tests are pure shell (POSIX-friendly bash) so they run without any
# extra toolchain beyond `helm` itself. They cover:
#   1. Default values render all 7 structural alerts.
#   2. alerting.enabled=false suppresses both the PrometheusRule and the
#      AlertmanagerConfig resources.
#   3. Switching receiver.type=mattermost renders a non-empty webhook config.
#
# Run from the repository root:
#     bash deploy/helm/daedalus/tests/alerts_test.sh

set -euo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
RELEASE="alerts-test"
PASS=0
FAIL=0

assert_eq() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    echo "  ok   $name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL $name (expected '$expected', got '$actual')"
    FAIL=$((FAIL + 1))
  fi
}

assert_match() {
  local name="$1" pattern="$2" haystack="$3"
  if printf '%s' "$haystack" | grep -qE "$pattern"; then
    echo "  ok   $name"
    PASS=$((PASS + 1))
  else
    echo "  FAIL $name (pattern '$pattern' not found)"
    FAIL=$((FAIL + 1))
  fi
}

echo "[test] default values render all 7 structural alerts"
out=$(helm template "$RELEASE" "$CHART_DIR")
count=$(printf '%s' "$out" | grep -cE '^[[:space:]]+- alert: ' || true)
assert_eq "seven alerts present" "7" "$count"

for name in WorkerImagePullBackOff WorkerCrashLoopBackOff NATSConsumerLagUnbounded \
            KEDAScalerError OTelCollectorDown OrchestratorDown NATSStreamUnhealthy; do
  assert_match "alert $name present" "alert: $name" "$out"
done

for anchor in worker-image-pull-backoff worker-crashloop nats-consumer-lag \
              keda-scaler-error otel-collector-down orchestrator-down nats-stream-unhealthy; do
  assert_match "runbook anchor #$anchor" "runbook.md#$anchor" "$out"
done

assert_match "PrometheusRule resource"     "kind: PrometheusRule"     "$out"
assert_match "AlertmanagerConfig resource" "kind: AlertmanagerConfig" "$out"

echo "[test] alerting.enabled=false produces no alerting resources"
out_off=$(helm template "$RELEASE" "$CHART_DIR" --set alerting.enabled=false)
rule_count=$(printf '%s' "$out_off" | grep -cE "^kind: (PrometheusRule|AlertmanagerConfig)" || true)
assert_eq "no alerting resources rendered" "0" "$rule_count"

echo "[test] alertmanagerConfig.enabled=false leaves PrometheusRule but suppresses AlertmanagerConfig"
out_amc_off=$(helm template "$RELEASE" "$CHART_DIR" --set alerting.alertmanagerConfig.enabled=false)
assert_match "PrometheusRule still renders" "kind: PrometheusRule" "$out_amc_off"
amc_count=$(printf '%s' "$out_amc_off" | grep -cE "^kind: AlertmanagerConfig" || true)
assert_eq "AlertmanagerConfig suppressed" "0" "$amc_count"

echo "[test] receiver.type=mattermost renders a webhook receiver"
out_mm=$(helm template "$RELEASE" "$CHART_DIR" \
  --set alerting.alertmanagerConfig.receiver.type=mattermost \
  --set alerting.alertmanagerConfig.receiver.config.webhook=https://mm.example.com/hooks/abc)
assert_match "mattermost webhook url"    "https://mm.example.com/hooks/abc" "$out_mm"
assert_match "mattermost webhookConfigs" "webhookConfigs:" "$out_mm"

echo "[test] receiver.type=pagerduty renders a pagerduty receiver"
out_pd=$(helm template "$RELEASE" "$CHART_DIR" \
  --set alerting.alertmanagerConfig.receiver.type=pagerduty \
  --set alerting.alertmanagerConfig.receiver.config.routingKeySecret.name=pagerduty-routing-key)
assert_match "pagerdutyConfigs block"  "pagerdutyConfigs:" "$out_pd"
assert_match "pagerduty secret name"   'name: "pagerduty-routing-key"' "$out_pd"

echo "[test] receiver.type=github renders a webhook receiver"
out_gh=$(helm template "$RELEASE" "$CHART_DIR" \
  --set alerting.alertmanagerConfig.receiver.type=github \
  --set alerting.alertmanagerConfig.receiver.config.webhook=http://alertmanager-github-receiver.monitoring.svc.cluster.local:8080/v1/webhook)
assert_match "github AlertmanagerConfig resource" "kind: AlertmanagerConfig" "$out_gh"
assert_match "github webhookConfigs block"        "webhookConfigs:"          "$out_gh"
assert_match "github webhook url"                 "http://alertmanager-github-receiver\.monitoring\.svc\.cluster\.local:8080/v1/webhook" "$out_gh"

echo "[test] receiver.type=bogus is rejected at template time"
bogus_output=""
bogus_rc=0
bogus_output=$(helm template "$RELEASE" "$CHART_DIR" \
  --set alerting.alertmanagerConfig.receiver.type=bogus 2>&1) || bogus_rc=$?
if [ "$bogus_rc" -ne 0 ]; then
  echo "  ok   bogus receiver type exits non-zero"
  PASS=$((PASS + 1))
else
  echo "  FAIL bogus receiver type exits non-zero (got 0)"
  FAIL=$((FAIL + 1))
fi
assert_match "bogus error mentions 'is not supported'" "is not supported" "$bogus_output"
assert_match "bogus error names the bad value"         "bogus"            "$bogus_output"

echo "[test] empty releaseLabel is rejected at template time"
empty_rl_output=""
empty_rl_rc=0
empty_rl_output=$(helm template "$RELEASE" "$CHART_DIR" \
  --set alerting.prometheusOperator.releaseLabel="" 2>&1) || empty_rl_rc=$?
if [ "$empty_rl_rc" -ne 0 ]; then
  echo "  ok   empty releaseLabel exits non-zero"
  PASS=$((PASS + 1))
else
  echo "  FAIL empty releaseLabel exits non-zero (got 0)"
  FAIL=$((FAIL + 1))
fi
assert_match "empty releaseLabel error mentions the value name" "releaseLabel must be a non-empty string" "$empty_rl_output"

echo "[test] OrchestratorDown carries namespace label on absent() leg and as static label"
out_default=$(helm template "$RELEASE" "$CHART_DIR")
assert_match "OrchestratorDown absent() carries namespace matcher" \
  "absent\(kube_deployment_status_replicas_available\{namespace=\"default\", deployment=\"$RELEASE-daedalus-orchestrator\"\}\)" \
  "$out_default"
assert_match "OrchestratorDown ==0 leg carries namespace matcher" \
  "kube_deployment_status_replicas_available\{namespace=\"default\", deployment=\"$RELEASE-daedalus-orchestrator\"\} == 0" \
  "$out_default"

echo "[test] NATSStreamUnhealthy uses surveyor-real metrics (no fictional jetstream_stream_*)"
assert_match "consumer alert uses nats_consumer_num_pending (not jetstream_)" \
  'delta\(nats_consumer_num_pending\{stream="AGENT_TASKS"\}\[10m\]\) > 0' "$out_default"
assert_match "consumer alert description references consumer_name label" \
  '\$labels\.consumer_name' "$out_default"
assert_match "stream alert uses nats_stream_consumer_count" \
  'nats_stream_consumer_count\{stream="AGENT_TASKS"\} == 0' "$out_default"
assert_match "stream alert uses nats_stream_total_messages" \
  'nats_stream_total_messages\{stream="AGENT_TASKS"\} > 0' "$out_default"

echo "[test] NATS alerts are scoped to the daedalus task stream (CC3 finding 2)"
assert_match "consumer-lag pending leg has stream= filter" \
  'nats_consumer_num_pending\{stream="AGENT_TASKS"\} > 100' "$out_default"
assert_match "stream-unhealthy total-messages leg has stream= filter" \
  'nats_stream_total_messages\{stream="AGENT_TASKS"\} > 0' "$out_default"

echo "[test] KEDAScalerError is scoped to the release namespace"
assert_match "KEDAScalerError carries namespace filter" \
  'rate\(keda_scaler_errors_total\{namespace="default"\}\[5m\]\) > 0' "$out_default"

echo "[test] OTelCollectorDown covers absent collector and carries namespace label"
assert_match "otel alert has absent() leg" \
  'absent\(up\{job="otel-collector"\}\)' "$out_default"
# Negative: the expr selector must NOT carry a namespace= matcher
# (CC3 finding 1: the OTel collector typically runs in `monitoring`,
# not the chart release namespace, so `namespace=` would never match
# and the alert would fire permanently). The static rule label below
# handles AMC routing.
if printf '%s' "$out_default" | grep -E 'up\{job="otel-collector",[[:space:]]*namespace=' >/dev/null; then
  echo "  FAIL OTelCollectorDown expr selector still carries namespace= matcher"
  FAIL=$((FAIL + 1))
else
  echo "  ok   OTelCollectorDown expr selector has no namespace= matcher"
  PASS=$((PASS + 1))
fi
assert_match "otel alert has static namespace label" \
  'namespace: "default"' "$out_default"

echo "[test] every alert carries a static namespace label"
# Defense-in-depth regression guard for CC2-1 / its NATS in-loop follow-up:
# the AlertmanagerConfig sub-route matches on namespace=<release-ns>, so any
# alert whose source metrics do not carry a release-namespace label must pin
# it as a static rule label or the alert escapes the chart's AMC. Asserting
# every rendered alert has one closes the door on future regressions.
total_alerts=$(printf '%s' "$out_default" | grep -cE '^[[:space:]]+- alert: ' || true)
namespace_alerts=$(printf '%s' "$out_default" | awk '
  /^[[:space:]]+- alert:/ {alert=1; ns=0; next}
  alert && /^[[:space:]]+annotations:/ { if (ns) count++; alert=0; ns=0; next }
  alert && /^[[:space:]]+namespace:[[:space:]]/ {ns=1}
  END {print count+0}
')
assert_eq "every alert has namespace label" "$total_alerts" "$namespace_alerts"

# Negative: ensure no fictional nats-surveyor metrics are USED (not just
# mentioned in comments) in the rendered chart. Filter out comment lines.
if printf '%s' "$out_default" | grep -vE '^\s*#' | grep -qE 'nats_jetstream_stream_(messages_lost|max_bytes|storage_bytes)|nats_jetstream_consumer_num_pending'; then
  echo "  FAIL fictional nats-surveyor metric used outside comments"
  FAIL=$((FAIL + 1))
else
  echo "  ok   no fictional nats-surveyor metric used outside comments"
  PASS=$((PASS + 1))
fi

echo
echo "results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
