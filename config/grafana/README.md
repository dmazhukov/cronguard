# Grafana dashboards

## CronGuard overview

`cronguard-dashboard.json` is a 6-panel overview that turns CronGuard metrics into an at-a-glance view:

| Panel | Type | What it shows |
|---|---|---|
| All CronJobMonitors | Table | Every monitor with Ready, last success, failures, missed, drift |
| Time since last success | Stat | Per-monitor staleness, threshold-coloured |
| Schedule drift | Heatmap | Distribution of `actualStart - expectedStart` |
| Last duration | Timeseries | Most recent Job wall-clock duration |
| Conditions grid | State timeline | Per-condition True / False / Unknown over time |
| Reconcile rate | Timeseries | `cronguard_reconcile_total` rate by result + p95 latency |

## Importing

### Grafana OSS

1. Grafana → Dashboards → New → Import
2. Upload `cronguard-dashboard.json` or paste its contents
3. Select your Prometheus datasource for `DS_PROMETHEUS`

### Grafana Cloud

Same steps via the Cloud UI. The dashboard uses standard Prometheus queries that work against any Prometheus-compatible source.

### Provisioning

For GitOps-style provisioning, drop the JSON into your Grafana provisioning directory:

```yaml
# /etc/grafana/provisioning/dashboards/cronguard.yaml
apiVersion: 1
providers:
  - name: cronguard
    folder: CronGuard
    type: file
    options:
      path: /var/lib/grafana/dashboards/cronguard
```

Then place `cronguard-dashboard.json` in `/var/lib/grafana/dashboards/cronguard/`.

## Variables

- `namespace`: multi-select, drives downstream queries
- `cronjob`: multi-select, scoped to the selected namespace(s)

## Compatibility

- Grafana >= 10.0
- Schema version: 39
- Datasource: Prometheus (or any PromQL-compatible source)
