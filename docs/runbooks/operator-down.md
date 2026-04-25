# CronGuardOperatorDown

## Symptom

Prometheus cannot scrape CronGuard `/metrics` for 2+ minutes (`up{job=~"cronguard.*"} == 0`).

## Likely cause

- Operator pod crashlooping (OOM, panic, image pull failure)
- Network partition between Prometheus and the operator pod
- ServiceMonitor / scrape configuration drift
- Cluster-wide control plane outage

## Remediation

TODO — `kubectl -n cronguard-system get pods`, check operator logs, validate scrape config, verify image is reachable.

## Related

- Status: this is a platform-level alert about CronGuard itself, not the workloads it monitors
- Recovery: when scraping resumes, the alert clears within `for: 2m`
