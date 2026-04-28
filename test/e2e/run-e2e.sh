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

log "Scraping /metrics"

# Two flake classes have surfaced here in nightly runs:
#
# 1. Endpoint-update lag: kubectl port-forward against a Service goes
#    through the EndpointSlice, which the endpoint controller updates
#    asynchronously after the pod becomes Ready. The lag can be
#    1-2 seconds. During that window port-forward sees no endpoint
#    or a stale pod with phase=Pending, and exits non-recoverably.
# 2. apiserver watch-cache stale read: even after Endpoint is correct,
#    the cached pod.status.phase the port-forward subprocess reads can
#    briefly lag the live state.
#
# Belt-and-suspenders: target the Deployment directly (kubectl resolves
# it to a Ready pod, no Endpoint indirection), and retry the port-forward
# startup itself if the background process exits within 2 seconds —
# that's the signature of "saw stale state, gave up".
PF_PID=""
for attempt in 1 2 3 4 5; do
  kubectl -n "$RELEASE_NS" port-forward deploy/cronguard 18080:8080 >/tmp/pf.log 2>&1 &
  PF_PID=$!
  sleep 2
  if kill -0 "$PF_PID" 2>/dev/null; then
    log "port-forward up (attempt $attempt, pid $PF_PID)"
    break
  fi
  log "port-forward attempt $attempt died, retrying"
  cat /tmp/pf.log >&2 || true
  PF_PID=""
done
if [ -z "$PF_PID" ]; then
  log "port-forward failed to stay up after 5 attempts"
  cat /tmp/pf.log >&2
  exit 1
fi
trap 'rc=$?; kill $PF_PID 2>/dev/null || true; cleanup $rc' EXIT
for _ in {1..15}; do
  if curl -sSf "http://localhost:18080/metrics" >/dev/null 2>&1; then break; fi
  sleep 1
done

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
