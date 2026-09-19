#!/usr/bin/env bash
# Usage: hack/sign-chart.sh VERSION OUTDIR. Prints the signed package path.
# Fails unless the signature verifies against the key users download and was
# made by the key whose fingerprint Artifact Hub shows from Chart.yaml.
set -euo pipefail

version="$1" outdir="$2"
[ -n "${HELM_SIGNING_KEY:-}" ] || { echo "HELM_SIGNING_KEY is not set" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(umask 077; base64 -d <<<"$HELM_SIGNING_KEY" > "$tmp/secring.gpg")

mkdir -p "$outdir"
helm package charts/cronguard --version "$version" --app-version "$version" \
  --sign --key "CronGuard chart signing" --keyring "$tmp/secring.gpg" \
  -d "$outdir" >&2
pkg="$outdir/cronguard-$version.tgz"

gpg --dearmor < docs/chart-signing-key.asc > "$tmp/pubring.gpg"
out=$(helm verify "$pkg" --keyring "$tmp/pubring.gpg")
echo "$out" >&2
signed=$(sed -n 's/^Using Key With Fingerprint: //p' <<<"$out")
annotated=$(helm show chart "$pkg" | sed -n 's/^ *fingerprint: //p')
if [ -z "$signed" ] || [ "$signed" != "$annotated" ]; then
  echo "signed by '$signed', Chart.yaml artifacthub.io/signKey names '$annotated'" >&2
  exit 1
fi
echo "$pkg"
