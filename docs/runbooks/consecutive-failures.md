# CronGuardConsecutiveFailures

## Symptom

`cronguard_condition{type="ExecutionHealthy"} == 0` for 5+ minutes — the CronJob has failed `maxConsecutiveFailures` times in a row.

## Likely cause

- Application bug introduced in a recent deploy
- External dependency (DB, API, queue) outage
- Misconfiguration: secrets/configmaps drifted, image pull failure, OOMKill

## Remediation

TODO — investigate Job logs, inspect the latest failed Job's pod logs, check recent deploys, validate dependencies.

## Related

- Metric: `cronguard_consecutive_failures`, `cronguard_last_failure_timestamp_seconds`
- Status condition: `ExecutionHealthy=False, reason=ConsecutiveFailures`
