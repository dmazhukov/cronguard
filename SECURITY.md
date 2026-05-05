# Security policy

## Reporting a vulnerability

If you discover a security vulnerability in CronGuard, please report it
privately rather than opening a public issue.

**Preferred:** open a private GitHub Security Advisory:
<https://github.com/dmazhukov/cronguard/security/advisories/new>

This gives us a private workspace to discuss the issue, draft a fix, and
coordinate disclosure before the patch ships.

**Fallback:** email dmitry0983@gmail.com if GitHub is unavailable or you
don't have a GitHub account.

Please include:
- A description of the issue and why you think it is security-relevant.
- Steps to reproduce, or a proof-of-concept.
- The CronGuard version (`kubectl get deployment -n cronguard-system -o jsonpath='{..image}'`).

I aim to acknowledge reports within one week and publish a fix within four weeks for confirmed issues. This is best-effort — I'm a solo maintainer.

## Supported versions

Only the latest minor release line receives security updates.
