# Distribution

CronGuard ships through three install paths so you can pick whichever fits your tooling.

## 1. Raw kubectl manifests

Each release attaches an `install.yaml` and the CRD as standalone files:

```bash
kubectl apply -f https://github.com/dmazhukov/cronguard/releases/download/v0.4.1/install.yaml
```

This is the lowest-dependency path — no Helm, no extra tooling. Suitable for clusters where Helm is not available or release operations are tightly controlled.

## 2. Helm chart via GitHub Pages

```bash
helm repo add cronguard https://dmazhukov.github.io/cronguard/
helm repo update
helm install cronguard cronguard/cronguard --version 0.4.1 \
  --namespace cronguard-system --create-namespace
```

Browser-friendly index at https://dmazhukov.github.io/cronguard/index.yaml.

## 3. Helm chart via OCI registry

Helm 3.8+ supports OCI registries natively. The chart is published alongside the operator image on GHCR:

```bash
helm install cronguard oci://ghcr.io/dmazhukov/charts/cronguard \
  --version 0.4.1 \
  --namespace cronguard-system --create-namespace
```

Same auth model as the operator image (`docker login ghcr.io` if needed for private clusters).

## 4. Artifact Hub

The chart is published at https://artifacthub.io/packages/helm/cronguard/cronguard with verified-owner status. Use Artifact Hub for discovery; install via paths 1–3 above.

## Configuration

All Helm install methods accept the standard chart values. See [`charts/cronguard/README.md`](../charts/cronguard/README.md) for the full reference.

Common overrides:

```bash
# Single-namespace watch
helm install cronguard cronguard/cronguard \
  --set namespace=finance

# Enable ServiceMonitor for prometheus-operator
helm install cronguard cronguard/cronguard \
  --set serviceMonitor.enabled=true

# Enable bundled alerts
helm install cronguard cronguard/cronguard \
  --set prometheusRule.enabled=true

# HA — two replicas, leader election picks the active one
helm install cronguard cronguard/cronguard \
  --set replicaCount=2
```

With `replicaCount > 1` AND `serviceMonitor.enabled=true`, the chart's ServiceMonitor adds a `relabelings: keep regex: leader` rule keyed on the `cronguard.io/role` pod label. Only the elected leader pod is scraped — without this, both pods would serve `/metrics` with identical labels and Prometheus would double-count every gauge. The standby pod is patched with `cronguard.io/role: standby` and remains visible in the Service's endpoints (for kubelet liveness/readiness traffic) but excluded from scrape.

## Operator flags

The Helm chart sets these from values; with the raw manifests, edit the `args` of the `manager` container.

| Flag | Default | Chart value | What it does |
|---|---|---|---|
| `--metrics-bind-address` | `:8080` | `metrics.port` | Where `/metrics` listens, plain HTTP. |
| `--health-probe-bind-address` | `:8081` | `healthProbe.port` | Where `/healthz` and `/readyz` listen. |
| `--leader-elect` | `true` | `leaderElection.enabled` | Leader election; required for more than one replica. |
| `--namespace` | empty | `namespace` | Watch one namespace instead of the whole cluster. |
| `--max-concurrent-reconciles` | `1` | `maxConcurrentReconciles` | Monitors reconciled in parallel. |

The `--zap-*` logging flags from controller-runtime are also accepted, for example `--zap-log-level=2` through the chart's `extraArgs`.

## Events

The operator emits Kubernetes events on the `CronJobMonitor`, only on transitions, so a monitor stuck in one state does not repeat them.

| Type | Reason | When |
|---|---|---|
| Warning | `CronJobNotFound`, `CronJobSuspended`, `InvalidSchedule`, `InvalidTimeZone`, `UnsatisfiableSchedule` | Measuring stopped; the same reason is on the `Reconciled` condition. |
| Warning | `ScheduleMismatch` | `spec.schedule` overrides the CronJob's own schedule; once per spec change. |
| Warning | `ScheduleMissed`, `ConsecutiveFailures`, `DurationExceeded` | An SLO axis turned False. |
| Normal | `ReconcileSuccess` | Measuring resumed after one of the first-row reasons. |

```bash
kubectl get events -n <ns> --field-selector involvedObject.kind=CronJobMonitor
```

## CRD upgrades

Helm installs the CronGuard CRD on `helm install` but does NOT modify it on `helm upgrade`, and leaves it in place on `helm uninstall`. That is Helm's design for the `crds/` directory, and Helm 4 keeps it: checked with Helm 4.2.3, where an upgrade that changed the chart's CRD schema left the cluster's CRD untouched, with and without `--server-side`. To upgrade the CRD when the chart bumps it:

```bash
kubectl apply -f https://raw.githubusercontent.com/dmazhukov/cronguard/v0.4.1/charts/cronguard/crds/cronjobmonitors.yaml
```

## Verifying artifacts

Every release publishes a GitHub build attestation (SLSA provenance, signed through Sigstore with the workflow's OIDC identity) for the container image and for the raw manifests, an SBOM attached to the image, and a `checksums.txt` on the GitHub release.

```bash
# Container image: provenance must name this repository's release workflow
gh attestation verify oci://ghcr.io/dmazhukov/cronguard:0.4.1 --owner dmazhukov

# Raw manifests: download, then check the attestation and the checksum
gh release download v0.4.1 --repo dmazhukov/cronguard --pattern 'install.yaml' --pattern 'checksums.txt'
gh attestation verify install.yaml --owner dmazhukov
sha256sum --check --ignore-missing checksums.txt
```

The image and the Helm OCI chart also carry cosign keyless signatures:

```bash
cosign verify \
  --certificate-identity-regexp '^https://github.com/dmazhukov/cronguard/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/dmazhukov/cronguard:0.4.1

cosign verify \
  --certificate-identity-regexp '^https://github.com/dmazhukov/cronguard/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/dmazhukov/charts/cronguard:0.4.1
```

The signatures are stored as OCI referrers in the Sigstore bundle format. cosign v3 finds them as shown; cosign 2.x needs `--new-bundle-format` (checked with 2.6.1) and without it reports `no signatures found`.

Releases before v0.4.1 have the attestation but no cosign signature.

Charts released after v0.4.1 also carry a Helm provenance file (`.prov`), signed with the project's PGP key: [`docs/chart-signing-key.asc`](chart-signing-key.asc), fingerprint `D8D2 3047 F2F6 6F57 F4E7 0514 006E 9901 74AB 7D61`, also served as `https://dmazhukov.github.io/cronguard/pgp_keys.asc`. `helm pull --verify` and `helm install --verify` check it against a keyring you pass, from the OCI registry and from the GitHub Pages repository alike. This is also what Artifact Hub reads to show the chart as signed.

```bash
curl -fsSL https://raw.githubusercontent.com/dmazhukov/cronguard/main/docs/chart-signing-key.asc | gpg --dearmor > cronguard-keyring.gpg
helm pull oci://ghcr.io/dmazhukov/charts/cronguard --version 0.4.1 --verify --keyring cronguard-keyring.gpg
```
