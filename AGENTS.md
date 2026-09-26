# k8s-garden-client — Agent Build Guide

Reference for AI agents building, testing, and deploying this project.

## First Rule: Ask Sofia

Before debugging, before grepping, before guessing — call `ask_sofia` with
what you're investigating. She knows CF runtime internals, IaC patterns,
credential flows, deployment topology, and operational heuristics. When she
doesn't know something, that's a knowledge gap to document and fill later.

Use Sofia when:
- Moving to a new gate or task phase
- Hitting an error you don't immediately understand
- Needing to understand IaC credential flows, kubeconfig setup, lscrypt, etc.
- Debugging pod failures, deployment issues, or CF runtime behavior
- Uncertain about the right approach for anything in the CF/Gardener/IaC stack

## Prerequisites (macOS)

```bash
brew install go docker-buildx
```

Docker Desktop (or Colima) must be running. Add buildx plugin path to `~/.docker/config.json`:

```json
{ "cliPluginsExtraDirs": ["/opt/homebrew/lib/docker/cli-plugins"] }
```

Authenticate to ghcr.io:

```bash
echo "$GHCR_TOKEN" | docker login ghcr.io -u <user> --password-stdin
```

## Local Build & Test (do this first)

### Unit tests

```bash
go test -race -count=1 ./pkg/prometheusmetrics/
go vet ./pkg/prometheusmetrics/
```

### Cross-compile for linux/amd64

Upstream dependencies have stale `go 1.16` directives but use `any` keyword.
The `-gcflags=all=-lang=go1.26` flag overrides the language version for all
packages — this is the same workaround the Dockerfile uses with `go1.27`.

```bash
export BUILD_FLAGS='CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS="-gcflags=all=-lang=go1.26"'

eval $BUILD_FLAGS go build -ldflags "-w -s" -o /tmp/rep-linux-amd64     ./cmd/rep
eval $BUILD_FLAGS go build -ldflags "-w -s" -o /tmp/watcher-linux-amd64 ./cmd/watch
eval $BUILD_FLAGS go build -ldflags "-w -s" -o /tmp/untar-linux-amd64   ./cmd/untar
```

Validate with `file /tmp/rep-linux-amd64` — must show `ELF 64-bit LSB executable, x86-64, statically linked`.

### go vet for cmd/rep

`go vet ./cmd/rep/` will fail on upstream deps (guardian, executor, rep modules)
due to the go1.16 lang issue. This is expected and not caused by our code.
Only `go vet ./pkg/prometheusmetrics/` is meaningful.

## Docker Image Build (fast path)

**Do NOT use the full multi-stage `Dockerfile` for iterative development.**
It compiles Go inside Docker (30+ min on Docker Desktop) and builds a static
`tar` binary from source.

Instead, pre-build binaries locally (seconds), extract the static `tar` from
the baseline image, and use a runtime-only Dockerfile:

```bash
# 1. Extract static tar from baseline (one-time)
docker create --name tar-extract ghcr.io/vlast3k/k8s/rep:0.7.1-phase1.1 /bin/true
docker cp tar-extract:/bin/tar /tmp/tar-linux-amd64
docker rm tar-extract

# 2. Assemble build context
mkdir -p /tmp/rep-image-build
cp /tmp/rep-linux-amd64     /tmp/rep-image-build/rep
cp /tmp/watcher-linux-amd64 /tmp/rep-image-build/watcher
cp /tmp/untar-linux-amd64   /tmp/rep-image-build/untar
cp /tmp/tar-linux-amd64     /tmp/rep-image-build/tar

# 3. Write runtime-only Dockerfile
cat > /tmp/rep-image-build/Dockerfile <<'EOF'
FROM ubuntu:26.04
RUN apt-get update && apt-get install -y \
    ca-certificates tzdata && \
    update-ca-certificates && \
    rm -rf /var/lib/apt/lists/*
COPY rep     /bin/rep
COPY watcher /bin/watcher
COPY untar   /bin/untar
COPY tar     /bin/tar
EXPOSE 8080 443
ENTRYPOINT [ "/bin/rep" ]
EOF

# 4. Build (~90s vs 30+ min)
docker buildx build --platform linux/amd64 \
    -t ghcr.io/vlast3k/k8s/rep:0.8.0-phase4.1 \
    --load /tmp/rep-image-build/

# 5. Push
docker push ghcr.io/vlast3k/k8s/rep:0.8.0-phase4.1
```

## Full Dockerfile (CI / release builds only)

The repository `Dockerfile` is the canonical multi-stage build. It compiles
Go from source inside Docker and builds a static `tar` from GNU tar source.
Use this for final release images or when the CI system runs it. On a
developer Mac, always use the fast path above.

```bash
# Only if you must — takes 30+ minutes on Docker Desktop
DOCKER_BUILDKIT=0 docker build --platform linux/amd64 \
    -t ghcr.io/vlast3k/k8s/rep:<tag> .
```

## Deployment to Gardener Shoot

After pushing the image, update the image tag + digest in the rendered manifest
and deploy via IaC:

```bash
# On the OCI container (landscape session)
# 1. Update image references in the rendered manifest:
sed -i "s|ghcr.io/vlast3k/k8s/rep:<old-tag>@sha256:<old>|ghcr.io/vlast3k/k8s/rep:<new-tag>@sha256:<new>|g" \
    /mnt/<landscape>/deployments/cf-on-k8s/gen/manifest.yml

# 2. Deploy (handles kubeconfig via AdminKubeconfigRequest internally):
iac -d cf-on-k8s action deploy

# 3. Verify rollout:
iac -d cf-on-k8s action verify
```

### Getting ad-hoc kubectl access to the Shoot

The `deploy`/`verify` actions handle kubeconfig automatically. For ad-hoc
`kubectl` from the OCI shell, you must extract it manually in two steps:

```bash
# Step 1: Get the Gardener SA kubeconfig from lscrypt (long-lived)
# NOTE: The deployment is cf-gardener-shoots, NOT cf-on-k8s
lscrypt read -d cf-gardener-shoots credentials.yml \
  | yq4 -r '.credentials.gardener_sa_kubeconfig' \
  > /mnt/<landscape>/gen/gardener-sa.kubeconfig

# Step 2: Request a 1-hour admin kubeconfig for the Shoot
kubectl --kubeconfig /mnt/<landscape>/gen/gardener-sa.kubeconfig \
  create --raw "/apis/core.gardener.cloud/v1beta1/namespaces/garden-i024148/shoots/cf-phase0/adminkubeconfig" \
  -f - <<'EOF' \
  | python3 -c "import sys,json,base64; print(base64.b64decode(json.load(sys.stdin)['status']['kubeconfig']).decode())" \
  > /mnt/<landscape>/gen/shoot.kubeconfig
{"apiVersion":"authentication.gardener.cloud/v1alpha1","kind":"AdminKubeconfigRequest","spec":{"expirationSeconds":3600}}
EOF

export KUBECONFIG=/mnt/<landscape>/gen/shoot.kubeconfig
kubectl get nodes
```

## Acceptance Test — Prometheus Metrics

**IMPORTANT: `curl` is NOT installed in the k8s-rep container** (Ubuntu 26.04
base image). Use `bash /dev/tcp` or `wget -qO-` instead.

```bash
# Using bash /dev/tcp (no external tools needed):
POD=$(kubectl -n cf-system get pod -l app=k8s-rep -o name | head -1)
kubectl -n cf-system exec "$POD" -c k8s-rep -- bash -c \
  'exec 3<>/dev/tcp/127.0.0.1/9090; printf "GET /metrics HTTP/1.0\r\nHost: localhost\r\n\r\n" >&3; cat <&3' \
  | grep cf_rep_

# Or via IaC action (handles kubeconfig automatically):
# Create a temp action at components/cf-on-k8s/actions/test-metrics
# that sources common, calls with_shoot_kubeconfig, then execs the above.

# Expected output (28 metric lines):
#   cf_rep_capacity_total_memory_bytes 3.209166848e+10
#   cf_rep_capacity_remaining_containers 91
#   cf_rep_containers{state="all"} 18
#   cf_rep_bulk_sync_duration_seconds_count 17
#   cf_rep_sample_errors_total 0
#   ...
```

### Debugging pods

```bash
# Startup errors:
kubectl logs -n cf-system <pod> -c k8s-rep | grep -iE 'error|fatal|panic|metrics|9090'

# Previous container (after restart):
kubectl logs -n cf-system <pod> --previous

# Pod events (OOM, image pull, probe failures):
kubectl describe pod -n cf-system <pod>

# Ephemeral debug sidecar (if you really need curl):
kubectl debug -it <pod> -n cf-system --image=curlimages/curl --target=k8s-rep \
  -- curl http://127.0.0.1:9090/metrics
```

## Telegraf Sidecar (Gate 2 — Monitoring Pipeline)

The Telegraf sidecar scrapes the loopback Prometheus endpoint and forwards
metrics to the landscape monitoring backend via the Riemann pipeline.

### Helm Values

```yaml
monitoring:
  enabled: true                      # enables sidecar + ConfigMap
  address: "10.0.67.6:8094"         # Riemann InfluxDB line protocol port
  telegrafImage: telegraf:1.33-alpine
```

When `monitoring.enabled` is `false` (default), no Telegraf container or
ConfigMap is rendered — the DaemonSet stays unchanged from Gate 1.

### Pipeline Architecture

```
k8s-rep (:9090/metrics)
  → Telegraf sidecar (inputs.prometheus, 15s interval)
    → socket_writer (InfluxDB line protocol, tcp://Riemann:8094)
      → Riemann (event processing)
        → InfluxDB (measurement: "prometheus", fields: cf_rep_*)
```

### Data Shape in InfluxDB

- **Measurement**: `prometheus` (Telegraf `metric_version=2` behavior)
- **Tags**: `deployment=cf-on-k8s`, `host=<pod-name>`, `job=k8s-rep`,
  `le` (histogram buckets), `state` (container state), `url`
- **Fields**: All `cf_rep_*` metrics as columns (13 families, ~28 series)

Query example:
```sql
SELECT mean("cf_rep_capacity_total_memory_bytes")
  FROM "prometheus"
  WHERE "deployment" = 'cf-on-k8s'
  GROUP BY time(1m), "host"
```

### Patching the Rendered Manifest

The Helm chart is the source of truth, but the rendered manifest at
`/mnt/<landscape>/deployments/cf-on-k8s/gen/manifest.yml` must also be
patched when enabling monitoring (the IaC context pipeline doesn't run
`helm template` — it generates the manifest from its own templates).

Three additions to the rendered manifest:
1. **ConfigMap** `k8s-rep-telegraf-config` — Telegraf TOML config
2. **Container** `telegraf` — sidecar in the k8s-rep DaemonSet
3. **Volume** `telegraf-config` — mounts the ConfigMap into the sidecar

### Memory Limits

Telegraf 1.33 with the Prometheus input plugin needs **256Mi** minimum.
At 128Mi it OOMKills on ~30% of nodes during steady-state scraping (the
Prometheus plugin allocates buffers proportional to metric cardinality).

```yaml
resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    cpu: 200m
    memory: 256Mi
```

### Acceptance Test — End-to-End Pipeline

```bash
# 1. Verify Telegraf sidecar is running (3/3 containers):
iac -d cf-on-k8s action diagnose   # custom action, shows per-pod status

# 2. Verify Telegraf logs (no errors):
iac -d cf-on-k8s action telegraf-logs

# 3. Query InfluxDB via Grafana API (from local Mac or MCP):
#    measurement "prometheus", field "cf_rep_capacity_total_memory_bytes",
#    tag deployment=cf-on-k8s
#    Expect data points from all 11 hosts within last 5 minutes.

# 4. Quick Telegraf health check from inside the pod:
POD=$(kubectl -n cf-system get pod -l app=k8s-rep -o name | head -1)
kubectl -n cf-system logs "${POD#pod/}" -c telegraf --tail=5
# Should show "Loaded inputs: prometheus" and "Loaded outputs: socket_writer"
# with no error lines.
```

### Custom IaC Diagnostic Actions

These actions were created during Gate 2 development at
`components/cf-on-k8s/actions/` on the OCI container. They are not
committed to git — recreate as needed.

| Action | Purpose |
|--------|---------|
| `diagnose` | DaemonSet status + per-pod container readiness + warning events |
| `telegraf-detail` | Telegraf exit codes, signals, restart counts for all pods |
| `telegraf-logs` | Telegraf container logs from crashing and working pods |
| `telegraf-check` | Telegraf logs + metrics endpoint test |

## Image Tag Convention

```
<major>.<minor>.<patch>-phase<N>.<gate>
```

- `0.7.1-phase1.1` — Phase 1 baseline
- `0.8.0-phase4.1` — Phase 4 Gate 1 (Prometheus endpoint)
- `0.8.0-phase4.2` — Phase 4 Gate 2 (Telegraf sidecar — no image change, Helm only)

## Key Architecture Notes

- **Prometheus endpoint** is loopback-only (`127.0.0.1:9090`). No Service,
  Ingress, hostPort, or containerPort is exposed. Only the Telegraf sidecar
  (same pod) scrapes it.
- **IngressClientWrapper** intercepts existing Loggregator metric calls —
  no second executor polling loop.
- **Partial tick protection**: capacity gauges are only published after all
  12 metrics from one reporter cycle arrive.
- **`REP_METRICS_ADDRESS`** env var controls the listen address (default
  `127.0.0.1:9090`). Set via Helm `metrics.address`.
- **Telegraf sidecar** is gated on `monitoring.enabled`. When disabled,
  the DaemonSet is identical to Gate 1 (2 containers: k8s-rep + watcher).
- **Riemann address** is currently hardcoded per-landscape in the rendered
  manifest. For product integration (Gate 5), it should come from IaC
  monitoring imports (`riemann.host` + `riemann.influxdb_line_port`).
