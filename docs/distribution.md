# Distribution

CronGuard ships through three install paths so you can pick whichever fits your tooling.

## 1. Raw kubectl manifests

Each release attaches an `install.yaml` and the CRD as standalone files:

```bash
kubectl apply -f https://github.com/dmazhukov/cronguard/releases/download/v0.2.0/install.yaml
```

This is the lowest-dependency path — no Helm, no extra tooling. Suitable for clusters where Helm is not available or release operations are tightly controlled.

## 2. Helm chart via GitHub Pages

```bash
helm repo add cronguard https://dmazhukov.github.io/cronguard/
helm repo update
helm install cronguard cronguard/cronguard --version 0.2.0 \
  --namespace cronguard-system --create-namespace
```

Browser-friendly index at https://dmazhukov.github.io/cronguard/index.yaml.

## 3. Helm chart via OCI registry

Helm 3.8+ supports OCI registries natively. The chart is published alongside the operator image on GHCR:

```bash
helm install cronguard oci://ghcr.io/dmazhukov/charts/cronguard \
  --version 0.2.0 \
  --namespace cronguard-system --create-namespace
```

Same auth model as the operator image (`docker login ghcr.io` if needed for private clusters).

## 4. Artifact Hub

Once the GitHub Pages repository is registered at https://artifacthub.io, the chart is discoverable through Artifact Hub search. The repository ID UUID lives in `gh-pages/artifacthub-repo.yml`; until that file is populated, the chart is invisible to Artifact Hub but still installable through paths 1–3.

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

## CRD upgrades

Helm 3 installs the CronGuard CRD on `helm install` but does NOT modify it on `helm upgrade` — this is a deliberate Helm 3 design. To upgrade the CRD when the chart bumps it:

```bash
kubectl apply -f https://raw.githubusercontent.com/dmazhukov/cronguard/v0.2.0/charts/cronguard/crds/cronjobmonitors.yaml
```

## Verifying signatures (future)

Phase 3 may add cosign signing for the OCI artifacts. Until then, integrity rests on GitHub's `GITHUB_TOKEN` push provenance.
