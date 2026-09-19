#!/usr/bin/env bash
# CronGuard e2e: kind + helm + sample CJM + metrics scrape.
# Re-entrant: tears down on exit, cleans up partial state.
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-cronguard-e2e}"
NODE_IMAGE="${NODE_IMAGE:-kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5}"
IMG="${IMG:-cronguard:e2e}"
RELEASE_NS="${RELEASE_NS:-cronguard-system}"
SAMPLE_NS="${SAMPLE_NS:-default}"
TIMEOUT="${TIMEOUT:-180s}"

log() { printf '\n=== %s ===\n' "$*" >&2; }

cleanup() {
  # Accept the original exit code as $1 so chained traps can preserve it
  # past noisy `|| true` cleanup calls (which would otherwise reset $?).
  local rc="${1:-$?}"
  if [[ "$rc" -ne 0 ]]; then
    log "FAILURE (exit=$rc) — collecting diagnostics"
    kubectl -n "$RELEASE_NS" get pods -o wide || true
    kubectl -n "$RELEASE_NS" describe deploy || true
    kubectl -n "$RELEASE_NS" describe pods || true
    kubectl -n "$RELEASE_NS" logs deploy/cronguard --tail=200 -c manager || true
    kubectl -n "$SAMPLE_NS" get cronjobmonitors -o yaml || true
  fi
  if kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
    log "Deleting kind cluster $CLUSTER_NAME"
    kind delete cluster --name "$CLUSTER_NAME"
  fi
  exit "$rc"
}
trap 'cleanup $?' EXIT

log "Creating kind cluster $CLUSTER_NAME ($NODE_IMAGE)"
kind create cluster --name "$CLUSTER_NAME" --image "$NODE_IMAGE" --wait 60s

log "Building image $IMG"
docker build -t "$IMG" .

log "Loading image into kind"
kind load docker-image "$IMG" --name "$CLUSTER_NAME"

log "Installing CronGuard via Helm"
helm install cronguard ./charts/cronguard \
  --namespace "$RELEASE_NS" --create-namespace \
  --set image.repository="${IMG%:*}" \
  --set image.tag="${IMG##*:}" \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout "$TIMEOUT"

# `helm install --wait` returns when Deployment.status.readyReplicas matches
# spec.replicas, but the nightly run on 2026-04-28 caught a race where it
# returned with the pod still in ContainerCreating (helm v3.18.4). Belt and
# suspenders: explicitly wait for Available before doing anything that needs
# the pod to actually be running, e.g. port-forward.
log "Waiting for operator deployment Available"
kubectl -n "$RELEASE_NS" wait --for=condition=Available --timeout=120s deployment/cronguard

log "Operator pods"
kubectl -n "$RELEASE_NS" get pods

log "Applying samples"
kubectl apply -f config/samples/cronjob_example.yaml -n "$SAMPLE_NS"
kubectl apply -f config/samples/monitoring_v1alpha1_cronjobmonitor.yaml -n "$SAMPLE_NS"

log "Waiting for CronJobMonitor Reconciled=True"
if ! kubectl -n "$SAMPLE_NS" wait --for=condition=Reconciled --timeout=120s cronjobmonitor/nightly-settlement; then
  log "Timed out waiting for Reconciled=True"
  kubectl -n "$SAMPLE_NS" describe cronjobmonitor nightly-settlement
  exit 1
fi

log "Status of CronJobMonitor"
kubectl -n "$SAMPLE_NS" get cronjobmonitor nightly-settlement -o yaml

log "Applying scenario fixtures"
kubectl apply -n "$SAMPLE_NS" -f test/e2e/fixtures.yaml

# wait_reason MONITOR TYPE REASON [TIMEOUT]: wait until the monitor's
# condition TYPE carries REASON. kubectl wait accepts a JSONPath value match.
wait_reason() {
  local mon="$1" typ="$2" reason="$3" to="${4:-120s}"
  if ! kubectl -n "$SAMPLE_NS" wait "cronjobmonitor/$mon" --timeout="$to" \
      --for=jsonpath="{.status.conditions[?(@.type==\"$typ\")].reason}=$reason"; then
    log "$mon: condition $typ never reached reason $reason"
    kubectl -n "$SAMPLE_NS" get cronjobmonitor "$mon" -o yaml
    exit 1
  fi
}

log "@every on a CronJob is reported, not measured"
wait_reason e2e-every-mon Reconciled InvalidSchedule

log "A schedule with no calendar date is reported, not shown healthy"
wait_reason e2e-feb30-mon Reconciled UnsatisfiableSchedule

log "A named time zone resolves inside the distroless image"
wait_reason e2e-tz-mon Reconciled ReconcileSuccess
tz=$(kubectl -n "$SAMPLE_NS" get cronjobmonitor e2e-tz-mon -o jsonpath='{.status.resolvedTimeZone}')
next=$(kubectl -n "$SAMPLE_NS" get cronjobmonitor e2e-tz-mon -o jsonpath='{.status.nextExpectedTime}')
if [[ "$tz" != "Asia/Singapore" || -z "$next" ]]; then
  log "e2e-tz-mon: resolvedTimeZone=$tz nextExpectedTime=$next"
  exit 1
fi

log "A Job seen running and then failed counts as a failure"
# The scenario is only meaningful if the operator saw the Job running first.
if ! kubectl -n "$SAMPLE_NS" wait cronjobmonitor/e2e-fail-mon --timeout=150s \
    --for=jsonpath='{.status.recentExecutions[0].phase}'=Running; then
  log "e2e-fail-mon never recorded a running Job"
  kubectl -n "$SAMPLE_NS" get cronjobmonitor e2e-fail-mon -o yaml
  exit 1
fi
wait_reason e2e-fail-mon ExecutionHealthy ConsecutiveFailures 240s

log "Missed runs accumulate"
wait_reason e2e-missed-mon ScheduleHealthy ScheduleMissed 180s

# Scrape from inside the cluster, through the metrics Service by its DNS
# name. This exercises the Service and its named port, which a port-forward
# to the Deployment bypasses, and it avoids the port-forward flake classes
# (EndpointSlice lag, stale pod phase) the nightly runs used to hit.
METRICS_URL="http://cronguard-metrics.${RELEASE_NS}.svc:8080/metrics"
scrape() {
  # A pod that runs to completion, then its logs: `kubectl run --rm -i`
  # attaches to a container that may already be writing and can lose the
  # start of the output.
  local pod="e2e-scrape-$RANDOM"
  kubectl -n "$SAMPLE_NS" run "$pod" --image=busybox:1.36 --restart=Never \
    --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"s","image":"busybox:1.36","command":["wget","-qO-","'"$METRICS_URL"'"],"securityContext":{"allowPrivilegeEscalation":false,"readOnlyRootFilesystem":true,"capabilities":{"drop":["ALL"]}}}]}}' \
    >/dev/null
  kubectl -n "$SAMPLE_NS" wait "pod/$pod" --for=jsonpath='{.status.phase}'=Succeeded --timeout=60s >/dev/null || true
  kubectl -n "$SAMPLE_NS" logs "pod/$pod" 2>/dev/null
  kubectl -n "$SAMPLE_NS" delete "pod/$pod" --wait=false >/dev/null 2>&1 || true
}

# Every family the operator can emit. last_duration appears once a Job
# finished with a completionTime (the sample succeeds every two minutes);
# missed_runs_total once a miss has been counted.
REQUIRED=(
  cronguard_last_success_timestamp_seconds
  cronguard_last_failure_timestamp_seconds
  cronguard_last_schedule_timestamp_seconds
  cronguard_next_expected_timestamp_seconds
  cronguard_consecutive_failures
  cronguard_missed_runs
  cronguard_schedule_drift_seconds
  cronguard_last_duration_seconds
  cronguard_running_jobs
  cronguard_condition
  cronguard_missed_runs_total
  cronguard_reconcile_total
  cronguard_reconcile_duration_seconds
  cronguard_build_info
)
metrics=""
for attempt in $(seq 1 24); do
  metrics="$(scrape || true)"
  missing=()
  for m in "${REQUIRED[@]}"; do
    # Histograms appear only as _bucket/_sum/_count series.
    grep -Eq "^${m}(_bucket|_sum|_count)?[{ ]" <<<"$metrics" || missing+=("$m")
  done
  [[ ${#missing[@]} -eq 0 ]] && break
  log "attempt $attempt: waiting for ${missing[*]}"
  sleep 10
done
if [[ ${#missing[@]} -ne 0 ]]; then
  log "MISSING metric families: ${missing[*]}"
  head -120 <<<"$metrics" >&2
  exit 1
fi
log "All ${#REQUIRED[@]} metric families present via the Service ($(wc -l <<<"$metrics") lines)"

grep -q '^cronguard_condition{[^}]*name="e2e-feb30-mon"' <<<"$metrics" \
  || { log "no series at all for e2e-feb30-mon; the absence check below would pass for nothing"; exit 1; }
if grep -q '^cronguard_next_expected_timestamp_seconds{[^}]*name="e2e-feb30-mon"' <<<"$metrics"; then
  log "next_expected is published for a schedule that never fires"
  exit 1
fi
grep -q '^cronguard_next_expected_timestamp_seconds{[^}]*name="e2e-tz-mon"' <<<"$metrics" \
  || { log "next_expected missing for e2e-tz-mon"; exit 1; }
failures=$(grep '^cronguard_consecutive_failures{[^}]*name="e2e-fail-mon"' <<<"$metrics" | awk '{print $2}')
if [[ -z "$failures" || "$failures" == "0" ]]; then
  log "cronguard_consecutive_failures for e2e-fail-mon is '${failures}'"
  exit 1
fi

log "Uninstall"
helm uninstall cronguard --namespace "$RELEASE_NS"

# Cluster delete handled by trap.
log "PASSED"
