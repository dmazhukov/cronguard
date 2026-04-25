#!/usr/bin/env bash
# CronGuard e2e: kind + helm + sample CJM + metrics scrape.
# Re-entrant: tears down on exit, cleans up partial state.
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-cronguard-e2e}"
NODE_IMAGE="${NODE_IMAGE:-kindest/node:v1.31.0}"
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
    kubectl -n "$RELEASE_NS" describe deploy --all || true
    kubectl -n "$RELEASE_NS" logs deploy/cronguard --tail=200 -c manager || true
    kubectl -n "$SAMPLE_NS" get cronjobmonitors -o yaml || true
  fi
  if kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
    log "Deleting kind cluster $CLUSTER_NAME"
    kind delete cluster --name "$CLUSTER_NAME"
  fi
  exit "$rc"
}
# Capture $? at trap-fire time, then call cleanup with that explicit rc.
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

log "Operator pods"
kubectl -n "$RELEASE_NS" get pods

log "Applying samples"
kubectl apply -f config/samples/cronjob_example.yaml -n "$SAMPLE_NS"
kubectl apply -f config/samples/monitoring_v1alpha1_cronjobmonitor.yaml -n "$SAMPLE_NS"

log "Waiting for CronJobMonitor Reconciled=True (initial reconcile may set Ready=Unknown until first run)"
# Reconciled=True is the first stable signal; Ready may stay Unknown until a Job runs.
for i in {1..30}; do
  if kubectl -n "$SAMPLE_NS" get cronjobmonitor nightly-settlement \
       -o jsonpath='{.status.conditions[?(@.type=="Reconciled")].status}' 2>/dev/null \
     | grep -q '^True$'; then
    log "Reconciled=True observed after ${i}x polls"
    break
  fi
  sleep 4
  if [[ "$i" -eq 30 ]]; then
    log "Timed out waiting for Reconciled=True"
    kubectl -n "$SAMPLE_NS" describe cronjobmonitor nightly-settlement
    exit 1
  fi
done

log "Status of CronJobMonitor"
kubectl -n "$SAMPLE_NS" get cronjobmonitor nightly-settlement -o yaml

log "Scraping /metrics"
kubectl -n "$RELEASE_NS" port-forward svc/cronguard-metrics 18080:8080 >/tmp/pf.log 2>&1 &
PF_PID=$!
# Re-arm trap to also kill the port-forward before cleanup. Capture $? first
# so the `kill || true` doesn't overwrite it.
trap 'rc=$?; kill $PF_PID 2>/dev/null || true; cleanup $rc' EXIT
sleep 3

if curl -sSf "http://localhost:18080/metrics" >/tmp/metrics.txt; then
  log "Got /metrics ($(wc -l </tmp/metrics.txt) lines)"
else
  log "curl /metrics failed"
  cat /tmp/pf.log >&2
  exit 1
fi

# Required metric families.
REQUIRED=(
  "cronguard_consecutive_failures"
  "cronguard_missed_runs"
  "cronguard_schedule_drift_seconds"
  "cronguard_condition"
  "cronguard_reconcile_total"
  "cronguard_build_info"
)
missing=0
for m in "${REQUIRED[@]}"; do
  if ! grep -q "^${m}\b" /tmp/metrics.txt; then
    log "MISSING metric: $m"
    missing=1
  fi
done
if [[ "$missing" -ne 0 ]]; then
  log "metric assertion failed"
  head -100 /tmp/metrics.txt >&2
  exit 1
fi

log "All required metrics present"

log "Uninstall"
helm uninstall cronguard --namespace "$RELEASE_NS"

# Cluster delete handled by trap.
log "PASSED"
