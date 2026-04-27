# CronGuardOperatorDown

## Symptom

Alert fires when `up{job=~"cronguard.*"} == 0` for at least 2 minutes — Prometheus has not been able to scrape the CronGuard `/metrics` endpoint.

User-visible impact: CronGuard is no longer evaluating SLOs. Even if your CronJobs are running fine, no axis condition can update; existing conditions go stale, and a new failure on any monitored CronJob will not be detected until scraping resumes.

> Resource names below assume the default Helm install (`helm install cronguard ...`) into namespace `cronguard-system`. For the `kubectl apply -f install.yaml` install path, the Deployment is `cronguard-controller-manager` and the Service is `cronguard-controller-manager-metrics-service` — substitute accordingly.

## Why this matters

This is the platform-level alert. Every other CronGuard alert assumes that the operator is up and Prometheus can reach it. While this alert is firing, treat all `cronguard_*` metrics as untrusted — they reflect the last successful scrape, not the current state. Restoring scraping is the priority.

## Quick triage (~2 min)

1. Pod status — is the operator even running?
   ```bash
   kubectl -n cronguard-system get pods -l app.kubernetes.io/name=cronguard \
     -o custom-columns=NAME:.metadata.name,READY:.status.containerStatuses[*].ready,STATUS:.status.phase,RESTARTS:.status.containerStatuses[*].restartCount,AGE:.metadata.creationTimestamp
   ```
   Expect `READY=true`, `STATUS=Running`, low restart count.
2. If not running, why?
   ```bash
   kubectl -n cronguard-system describe pod -l app.kubernetes.io/name=cronguard \
     | sed -n '/Events:/,$p'
   ```
   Look for `OOMKilled`, `CrashLoopBackOff`, `ErrImagePull`, `FailedScheduling`.
3. Recent logs — panics, fatal errors, missing creds:
   ```bash
   kubectl -n cronguard-system logs deploy/cronguard --tail=200
   kubectl -n cronguard-system logs deploy/cronguard --previous --tail=200   # last crash
   ```
4. Service and endpoints — Prometheus uses these to find the pod:
   ```bash
   kubectl -n cronguard-system get svc cronguard-metrics -o yaml \
     | yq '.spec.ports, .spec.selector'
   kubectl -n cronguard-system get endpoints cronguard-metrics
   ```
   Endpoints with no addresses means the Service selector is not matching any pod.
5. Probe the metrics endpoint directly — does the pod itself serve metrics?
   ```bash
   kubectl -n cronguard-system port-forward svc/cronguard-metrics 8080:8080 &
   curl -sf http://localhost:8080/metrics | head -20
   kill %1
   ```
   If this works, the operator is healthy and the problem is between Prometheus and the Service.
6. ServiceMonitor sanity (Prometheus Operator deployments):
   ```bash
   kubectl get servicemonitors -A -l app.kubernetes.io/part-of=cronguard
   kubectl get servicemonitor -n <prom-ns> cronguard -o yaml \
     | yq '.spec.selector, .spec.endpoints, .spec.namespaceSelector'
   ```
   Selector labels must match the operator Service's labels; `namespaceSelector` must include `cronguard-system`.

## Common causes

- Operator pod is crashlooping (panic, OOM, image pull failure, bad config).
- Pod cannot be scheduled (node pool exhausted, PriorityClass evicted, taints).
- ServiceMonitor / Prometheus scrape config drifted — wrong selector, wrong port, wrong namespace.
- NetworkPolicy in `cronguard-system` is blocking ingress from the Prometheus namespace.
- Operator listening on the wrong port (Helm-values drift, e.g., `metrics.port` changed).
- Prometheus itself is down (rare — usually paged separately, but worth ruling out).

## Remediation

### Crashlooping pod

```bash
kubectl -n cronguard-system logs deploy/cronguard --previous --tail=300
kubectl -n cronguard-system describe pod -l app.kubernetes.io/name=cronguard \
  | grep -A4 "Last State:"
```

Common patterns:

- `panic:` in logs → code defect; capture the stack, restart to recover, file an issue:
  ```bash
  kubectl -n cronguard-system rollout restart deploy/cronguard
  ```
- `OOMKilled` in `Last State` → bump memory limit:
  ```bash
  kubectl -n cronguard-system set resources deploy/cronguard \
    --limits=memory=<new-limit> --requests=memory=<new-request>
  ```
- `ErrImagePull` / `ImagePullBackOff` → fix image tag or pull secret, then restart:
  ```bash
  kubectl -n cronguard-system describe pod -l app.kubernetes.io/name=cronguard | grep -A3 ImagePull
  ```

### Pod cannot schedule

```bash
kubectl -n cronguard-system describe pod -l app.kubernetes.io/name=cronguard \
  | grep -E "FailedScheduling|insufficient"
kubectl describe nodes | grep -E "Taints:|Allocatable:" -A1
kubectl get pdb -n cronguard-system
```

Scale the node pool, relax the operator's tolerations/affinity, or temporarily lower a competing PDB if it is blocking eviction.

### ServiceMonitor / scrape config mismatch

Re-apply the chart with the official ServiceMonitor enabled:

```bash
helm upgrade cronguard <chart> -n cronguard-system \
  --reuse-values --set serviceMonitor.enabled=true
```

Verify Prometheus actually picked it up (Prometheus UI → Status → Targets, or directly):

```bash
kubectl -n <prom-ns> exec -ti prometheus-<sts>-0 -c prometheus -- \
  wget -qO- http://localhost:9090/api/v1/targets \
  | jq '.data.activeTargets[] | select(.labels.job | test("cronguard")) | {health, lastError, scrapeUrl}'
```

If a target appears under `droppedTargets`, the relabeling rules excluded it — inspect ServiceMonitor labels vs. Prometheus's `serviceMonitorSelector`.

### NetworkPolicy blocking scrape

```bash
kubectl -n cronguard-system get networkpolicies
```

If a default-deny is in place, add an explicit allow from the Prometheus namespace:

```yaml
# scrape-allow.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-prometheus-scrape
  namespace: cronguard-system
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: cronguard
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: <prom-ns>
      ports:
        - port: 8080
          protocol: TCP
```

```bash
kubectl apply -f scrape-allow.yaml
```

### Wrong port

If `port-forward` to the Service worked but `curl` did not return Prometheus exposition, the operator is listening on the wrong port. Check the deployment vs. the Service:

```bash
kubectl -n cronguard-system get deploy cronguard \
  -o jsonpath='{.spec.template.spec.containers[*].ports}'
kubectl -n cronguard-system get svc cronguard-metrics \
  -o jsonpath='{.spec.ports}'
```

Both must agree on the metrics port (default `8080`). Re-apply chart values to align:

```bash
helm upgrade cronguard <chart> -n cronguard-system --reuse-values \
  --set metrics.port=8080
```

### Prometheus itself is down

Cross-check with Prometheus's own self-monitoring (`prometheus_build_info`, the Alertmanager `Watchdog` heartbeat). If Prometheus is the problem, escalate to the platform team — this alert is a symptom, not the cause.

## Related signals

- Metrics: `up{job=~"cronguard.*"}`, `cronguard_build_info`.
- Other CronGuard alerts (`CronGuardScheduleMissed`, etc.) cannot be trusted while this is firing — they will silence themselves once `up == 0` because their expressions evaluate to `no data`, but they will also miss real failures during the outage.

## Appendix

Verify the pod's service account has any cluster-wide RBAC it needs:

```bash
kubectl auth can-i list cronjobmonitors --as=system:serviceaccount:cronguard-system:cronguard
kubectl auth can-i watch cronjobs --as=system:serviceaccount:cronguard-system:cronguard
```

Force a fresh scrape attempt from Prometheus side:

```bash
kubectl -n <prom-ns> exec -ti prometheus-<sts>-0 -c prometheus -- \
  wget -qO- http://localhost:9090/api/v1/targets/metadata?match_target='{job=~"cronguard.*"}'
```

Operator health endpoints (if `--health-probe-bind-address` is enabled):

```bash
kubectl -n cronguard-system port-forward deploy/cronguard 8081:8081 &
curl -sf http://localhost:8081/healthz && curl -sf http://localhost:8081/readyz
kill %1
```
