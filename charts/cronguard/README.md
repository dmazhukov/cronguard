# CronGuard Helm chart

SLO-style observability operator for Kubernetes CronJobs.

The operator watches CronJobs and Jobs in the cluster (or a single namespace) and reconciles a `CronJobMonitor` custom resource that captures the SLO state — consecutive failures, missed runs, schedule drift, and recent execution history. Each monitor exports Prometheus metrics under `cronguard_*` so you can alert on schedule and execution health from a single source.

## Installing

### Via OCI registry (Helm 3.8+)

```bash
helm install cronguard oci://ghcr.io/dmazhukov/charts/cronguard --version 0.2.0 \
  --namespace cronguard-system --create-namespace
```

### Via GitHub Pages Helm repo

```bash
helm repo add cronguard https://dmazhukov.github.io/cronguard/
helm repo update
helm install cronguard cronguard/cronguard --version 0.2.0 \
  --namespace cronguard-system --create-namespace
```

### Via local chart checkout

```bash
helm install cronguard ./charts/cronguard \
  --namespace cronguard-system --create-namespace
```

## Uninstalling

```bash
helm uninstall cronguard -n cronguard-system
```

The CRD is **not** removed by `helm uninstall` (Helm 3 design). Drop it explicitly when you want to forget the operator:

```bash
kubectl delete crd cronjobmonitors.monitoring.cronguard.io
```

## Values

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `image.repository` | string | `ghcr.io/dmazhukov/cronguard` | Operator image repository. |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy. One of `Always`, `IfNotPresent`, `Never`. |
| `image.tag` | string | `""` | Image tag. Empty string defaults to `.Chart.AppVersion`. |
| `imagePullSecrets` | list | `[]` | Secret names for pulling private images. |
| `nameOverride` | string | `""` | Override the chart name component of resource names. |
| `fullnameOverride` | string | `""` | Override the entire resource name. |
| `replicaCount` | integer | `1` | Operator replicas. Set to `2` for HA — leader election keeps one reconciler active. |
| `namespace` | string | `""` | Restrict the operator to a single namespace. Empty means cluster-wide watch. |
| `leaderElection.enabled` | boolean | `true` | Toggle leader election. |
| `resources.requests.cpu` | string | `10m` | Container CPU request. |
| `resources.requests.memory` | string | `64Mi` | Container memory request. |
| `resources.limits.cpu` | string | `500m` | Container CPU limit. |
| `resources.limits.memory` | string | `256Mi` | Container memory limit. |
| `metrics.port` | integer | `8080` | Prometheus `/metrics` listener port. |
| `healthProbe.port` | integer | `8081` | Liveness and readiness probe port. |
| `serviceMonitor.enabled` | boolean | `false` | Render a `ServiceMonitor` for prometheus-operator. |
| `serviceMonitor.namespace` | string | `""` | Namespace for the `ServiceMonitor`. Empty defaults to release namespace. |
| `serviceMonitor.labels` | object | `{}` | Extra labels on the `ServiceMonitor`. |
| `serviceMonitor.interval` | string | `30s` | Scrape interval. |
| `serviceMonitor.scrapeTimeout` | string | `10s` | Scrape timeout. |
| `serviceMonitor.honorLabels` | boolean | `false` | Pass through `honorLabels` to scrape config. |
| `prometheusRule.enabled` | boolean | `false` | Render a `PrometheusRule` with five default CronGuard alerts. |
| `prometheusRule.namespace` | string | `""` | Namespace for the `PrometheusRule`. Empty defaults to release namespace. |
| `prometheusRule.labels` | object | `{}` | Extra labels on the `PrometheusRule`. |
| `prometheusRule.interval` | string | `30s` | Rule evaluation interval. |
| `prometheusRule.thresholds.scheduleMissed.for` | string | `5m` | `for:` duration for `CronGuardScheduleMissed`. |
| `prometheusRule.thresholds.scheduleMissed.severity` | string | `warning` | Severity label for `CronGuardScheduleMissed`. |
| `prometheusRule.thresholds.consecutiveFailures.for` | string | `5m` | `for:` duration for `CronGuardConsecutiveFailures`. |
| `prometheusRule.thresholds.consecutiveFailures.severity` | string | `warning` | Severity label for `CronGuardConsecutiveFailures`. |
| `prometheusRule.thresholds.durationExceeded.for` | string | `1m` | `for:` duration for `CronGuardDurationExceeded`. |
| `prometheusRule.thresholds.durationExceeded.severity` | string | `info` | Severity label for `CronGuardDurationExceeded`. |
| `prometheusRule.thresholds.notReady.for` | string | `10m` | `for:` duration for `CronGuardNotReady`. |
| `prometheusRule.thresholds.notReady.severity` | string | `critical` | Severity label for `CronGuardNotReady`. |
| `prometheusRule.thresholds.operatorDown.for` | string | `2m` | `for:` duration for `CronGuardOperatorDown`. |
| `prometheusRule.thresholds.operatorDown.severity` | string | `critical` | Severity label for `CronGuardOperatorDown`. |
| `crds.install` | boolean | `true` | Install the `CronJobMonitor` CRD on `helm install`. |
| `podAnnotations` | object | `{}` | Annotations applied to operator pods. |
| `podLabels` | object | `{}` | Extra labels applied to operator pods. |
| `podSecurityContext` | object | runAsNonRoot, runAsUser 65532, RuntimeDefault seccomp | Pod-level security context. |
| `securityContext` | object | drop ALL caps, no privilege escalation, read-only root FS | Container-level security context. |
| `nodeSelector` | object | `{}` | Node selector. |
| `tolerations` | list | `[]` | Tolerations. |
| `affinity` | object | `{}` | Affinity rules. |
| `priorityClassName` | string | `""` | Pod priority class. |
| `terminationGracePeriodSeconds` | integer | `30` | Pod termination grace period. |

## Upgrading

`helm upgrade` rolls Deployment, Service, RBAC, and ServiceMonitor templates. The CRD is **not** touched on upgrade — Helm 3 only installs CRDs on `helm install`. To upgrade the schema:

```bash
kubectl apply -f charts/cronguard/crds/cronjobmonitors.yaml
helm upgrade cronguard ./charts/cronguard -n cronguard-system
```

## CRD management notes

The chart ships the CRD in `crds/cronjobmonitors.yaml`. Helm 3 treats `crds/` specially: contents are installed once on `helm install` and ignored on `helm upgrade`. Set `crds.install=false` if you manage CRDs externally — for example, a separate `kubectl apply` step in your delivery pipeline.

## Examples

### Single-namespace install

```bash
helm install cronguard ./charts/cronguard \
  -n cronguard-system --create-namespace \
  --set namespace=production
```

The operator only watches CronJobs and Jobs in the `production` namespace.

### HA install

```bash
helm install cronguard ./charts/cronguard \
  -n cronguard-system --create-namespace \
  --set replicaCount=2
```

Two replicas with leader election (default-on). Only one is active at a time; failover is automatic.

### ServiceMonitor enabled

```bash
helm install cronguard ./charts/cronguard \
  -n cronguard-system --create-namespace \
  --set serviceMonitor.enabled=true \
  --set serviceMonitor.labels.release=prometheus
```

The `release: prometheus` label is what the kube-prometheus-stack chart selects on by default.

## PrometheusRule (alerts)

Set `prometheusRule.enabled: true` to ship five default alerts as a `PrometheusRule` resource. Requires prometheus-operator CRDs in the cluster.

```yaml
prometheusRule:
  enabled: true
  thresholds:
    scheduleMissed:
      for: 5m
      severity: warning
```

Override individual thresholds via `--set prometheusRule.thresholds.scheduleMissed.for=10m`.

The standalone manifest at `config/prometheus/rules.yaml` is the same set of rules without the chart's templating, suitable for users not on Helm.

## Compatibility

- Kubernetes `>= 1.28`
- Helm `>= 3.8` for OCI installs

## License

Apache-2.0
