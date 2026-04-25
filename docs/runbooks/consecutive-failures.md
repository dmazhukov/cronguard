# CronGuardConsecutiveFailures

## Symptom

Alert fires when `cronguard_condition{type="ExecutionHealthy"} == 0` for at least 5 minutes. The CronJob has produced `consecutiveFailures >= maxConsecutiveFailures` failed runs in a row with no intervening success.

User-visible impact: the workload's output is stale and getting staler with every slot. Alerts that depend on this Job's freshness will start to fire downstream.

## Why this matters

A single failure is noise; repeated failures across consecutive slots indicate a real defect — bad code, bad config, bad credentials, or a sick dependency. The streak counter exists so the alert is robust to flaky one-offs but pages humans on a real outage. By the time you see this, retries have not helped.

## Quick triage (~2 min)

1. Find the recent Jobs and their status:
   ```bash
   kubectl get jobs -n <ns> --sort-by=.metadata.creationTimestamp \
     -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
     -o custom-columns=NAME:.metadata.name,SUCCESS:.status.succeeded,FAILED:.status.failed,START:.status.startTime
   ```
   Identify the latest failed Job's name; call it `<job>`.
2. Latest pod logs — almost always tell you the failure mode:
   ```bash
   kubectl logs -n <ns> -l job-name=<job> --tail=200 --all-containers
   ```
3. Pod status reasons (look for `OOMKilled`, `Error`, `ImagePullBackOff`, `CreateContainerConfigError`):
   ```bash
   kubectl describe pod -n <ns> -l job-name=<job> \
     | grep -E "Reason:|Exit Code:|Last State:"
   ```
4. Compare images across recent runs — a recent deploy is the most common cause:
   ```bash
   kubectl get jobs -n <ns> -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
     -o jsonpath='{range .items[*]}{.metadata.name}{"  "}{.spec.template.spec.containers[*].image}{"\n"}{end}' \
     | sort
   ```
5. Check the CronJob spec and any annotations that hint at a recent rollout:
   ```bash
   kubectl get cronjob <ref-name> -n <ns> -o yaml \
     | yq '.metadata.annotations, .spec.jobTemplate.spec.template.spec.containers[].image'
   ```
6. Look at CronGuard's view of the streak:
   ```bash
   kubectl get cronjobmonitor <name> -n <ns> -o yaml \
     | yq '.status | {consecutiveFailures, lastFailureTime, recentExecutions, conditions}'
   ```

## Common causes

- A recent deploy of the Job's image regressed the workload.
- An external dependency (DB, internal API, queue, third-party partner) is degraded or down.
- Secret or ConfigMap drift — wrong credentials, missing env var, expired token.
- OOMKill: pod's memory limit is too low for current workload.
- Image pull failure: registry rate limiting, deleted tag, expired pull secret.
- Schema or migration mismatch: DB schema changed but the Job's code did not (or vice versa).

## Remediation

### Recent regression — roll back

If the failure window aligns with an image bump, restore the previous image. Find the last successful Job's image:

```bash
kubectl get jobs -n <ns> -l "batch.kubernetes.io/cronjob-name=<ref-name>" \
  --field-selector=status.successful=1 -o jsonpath='{range .items[*]}{.metadata.name}{"  "}{.spec.template.spec.containers[*].image}{"\n"}{end}' \
  | sort | tail -5
```

Re-apply the prior CronJob spec from your manifest source of truth (Argo CD, Flux, Helm, kustomize), or pin the image directly as a stop-gap:

```bash
kubectl set image cronjob/<ref-name> -n <ns> <container-name>=<previous-image>
```

Then trigger a one-off run to validate:

```bash
kubectl create job <ref-name>-manual-$(date +%s) -n <ns> --from=cronjob/<ref-name>
```

### Dependency outage

Reproduce against the dependency from inside the cluster — same network namespace, same SA — to confirm:

```bash
kubectl run -n <ns> debug --rm -it --restart=Never \
  --image=curlimages/curl --serviceaccount=<sa-name> \
  -- curl -v http://<dependency>:<port>/health
```

If the dependency owner is on call, hand off; otherwise temporarily silence this alert in Alertmanager until they recover.

### Credential / config drift

Inspect what the pod actually got:

```bash
kubectl get pod -n <ns> -l job-name=<job> -o jsonpath='{.items[0].spec.containers[0].env}' | jq
kubectl get secret -n <ns> <secret-name> -o jsonpath='{.data}' | jq 'keys'
```

Rotate the offending secret and trigger a retry:

```bash
kubectl create secret generic <secret-name> -n <ns> \
  --from-literal=API_TOKEN=<new-token> \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl create job <ref-name>-manual-$(date +%s) -n <ns> --from=cronjob/<ref-name>
```

### OOMKill

Confirm:

```bash
kubectl get pod -n <ns> -l job-name=<job> -o jsonpath='{.items[*].status.containerStatuses[*].lastState.terminated.reason}'
```

Bump memory in the CronJob's pod template:

```bash
kubectl patch cronjob <ref-name> -n <ns> --type=json -p='[
  {"op":"replace","path":"/spec/jobTemplate/spec/template/spec/containers/0/resources/limits/memory","value":"<new-limit>"},
  {"op":"replace","path":"/spec/jobTemplate/spec/template/spec/containers/0/resources/requests/memory","value":"<new-request>"}
]'
```

### Image pull failure

```bash
kubectl describe pod -n <ns> -l job-name=<job> | grep -A3 ImagePull
```

For private-registry pull failures, refresh the imagePullSecret:

```bash
kubectl create secret docker-registry <pullsecret> -n <ns> \
  --docker-server=<server> --docker-username=<user> --docker-password=<pw> \
  --dry-run=client -o yaml | kubectl apply -f -
```

### Schema / migration mismatch

If the dependency is your DB and the failure log shows column-not-found / type-mismatch errors, the Job and the schema are at different versions. Either roll the schema forward or roll the Job's image back; do not leave them divergent.

## Related signals

- Metrics: `cronguard_consecutive_failures`, `cronguard_last_failure_timestamp_seconds`, `cronguard_last_success_timestamp_seconds`.
- Status condition: `ExecutionHealthy=False, reason=ConsecutiveFailures`.
- Adjacent runbook: [duration-exceeded.md](duration-exceeded.md) — sometimes a Job is failing because it is being killed past its deadline rather than erroring outright.

## Appendix

Time since last success across the fleet (descending — most-stale first):

```promql
topk(10, time() - cronguard_last_success_timestamp_seconds)
```

Failure-streak leaderboard:

```promql
topk(10, cronguard_consecutive_failures)
```

Stream all pods for a Job in one shot, including init containers:

```bash
kubectl logs -n <ns> -l job-name=<job> --all-containers --prefix --tail=-1
```

Diff two Jobs' specs:

```bash
diff <(kubectl get job <good-job> -n <ns> -o yaml) <(kubectl get job <bad-job> -n <ns> -o yaml)
```
