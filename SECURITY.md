# Security policy

## Reporting a vulnerability

If you discover a security vulnerability in CronGuard, please report it
privately rather than opening a public issue.

**Email:** dmitry0983@gmail.com

Please include:
- A description of the issue and why you think it is security-relevant.
- Steps to reproduce, or a proof-of-concept.
- The CronGuard version (`kubectl get deployment -n cronguard-system -o jsonpath='{..image}'`).

I will acknowledge your report within 72 hours and aim to publish a fix
within two weeks for confirmed issues.

## Supported versions

Only the latest minor release line receives security updates.
