# Walkthrough cast

`install.cast` is an asciinema v2 recording of the CronGuard install flow.

## Play locally

```bash
asciinema play install.cast
```

Or convert to SVG/GIF for documentation:

```bash
# SVG (recommended for README embeds)
agg install.cast install.svg

# Or use the asciinema web player anywhere it's hosted.
```

## Re-record

The current cast is hand-crafted from idealised output. To replace with a real
recording:

```bash
# Spin up a kind cluster and install CronGuard.
kind create cluster --name cronguard-demo
make docker-build IMG=cronguard:demo
kind load docker-image cronguard:demo --name cronguard-demo
helm install cronguard ./charts/cronguard \
  --set image.repository=cronguard --set image.tag=demo \
  --namespace cronguard-system --create-namespace --wait

# Start recording.
asciinema rec docs/cast/install.cast --overwrite \
  --title "CronGuard install walkthrough" \
  --command bash

# Inside the recording shell, follow the script:
#   kubectl apply -f config/samples/cronjob_example.yaml
#   kubectl apply -f config/samples/monitoring_v1alpha1_cronjobmonitor.yaml
#   kubectl get cronjobmonitors
#   kubectl describe cronjobmonitor nightly-settlement
#   kubectl -n cronguard-system port-forward svc/cronguard-metrics 8080:8080 &
#   curl -s localhost:8080/metrics | grep ^cronguard_ | head -10
# Then exit (ctrl-d) to stop recording.

# Optional: upload to asciinema.org for the README badge.
asciinema upload docs/cast/install.cast
# Returns a URL like https://asciinema.org/a/<id>.
# Update the README badge with the issued <id>.

# Tear down.
kind delete cluster --name cronguard-demo
```

## Embedding in the README

Once uploaded to asciinema.org, swap the README's "Walkthrough" code block for:

```markdown
[![asciicast](https://asciinema.org/a/<id>.svg)](https://asciinema.org/a/<id>)
```
