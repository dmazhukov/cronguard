---
name: Bug report
about: Something does not work as expected
labels: bug
---

## Summary

<!-- One sentence. -->

## Environment

- CronGuard version: <!-- `kubectl get deployment -n cronguard-system -o jsonpath='{..image}'` -->
- Kubernetes version: <!-- `kubectl version --short` -->
- Cloud provider / distro:

## What happened

<!-- Steps to reproduce and actual behaviour. -->

## What you expected

## Relevant status/logs

```yaml
# kubectl get cjmon <name> -o yaml
```

```
# kubectl logs -n cronguard-system deploy/cronguard-controller-manager
```
