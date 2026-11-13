# Fork: per-pod SM caps, scoped MPS mounts, correct GPU memory under Sysbox

Base: upstream **v0.1.6** (the version KAI-Scheduler v0.18.3 pins).
Branch: `sysbox-sm-limit`. Tested on one A100-SXM4-40GB, driver 615.71.09
(CUDA 13.4, MPS protocol 3), RKE2 v1.36.5 / containerd 2.3.4, Sysbox CE 0.7.1,
KAI-Scheduler v0.18.3 (`gpuSharingMode: NvFractions`).

Both new features are **off by default**; with them off the fork behaves like
upstream v0.1.6.

## What it adds

| Setting (`GpuFractioningConfig`) | Component | Behaviour |
|---|---|---|
| `spec.smLimit.enabled` | fractiond | New compute mode `sm-limit` becomes the default. Each fractional container gets its own MPS namespace on the shared server; only that namespace's pipe directory is mounted (at `/tmp/nvidia-mps`). The node's root MPS control socket, other tenants' sockets, the request dir and the mapping dir are not visible. |
| | mpsd | Creates / updates / deletes one namespace per container: active-thread % = `ceil(gpu-fraction × 100)` (or memory share for `gpu-memory` requests), pinned-memory limit = the allotment. Recreates them after a daemon restart. |
| | metricsd | Kills processes in sm-limit containers that run a native (non-MPS) CUDA context, i.e. that bypassed their namespace. |
| `spec.smLimit.enforce` | fractiond | Ignores the per-pod `gpu-compute.mode` annotation, so workloads cannot opt out to time-slicing. |
| `spec.memoryGuard.enabled` | metricsd | Keeps each fractional container's NVML GPU memory limit correct on its whole cgroup subtree, and resets stray limit entries on every other Kubernetes cgroup. |

Optional pod annotation `gpu-sm-percent: "<1-100>"` lowers the SM share below
the fraction (never raises it). It is pod-level because KAI's admission
rejects unknown keys under `nvidia.com/container.`.

Helm: `smLimit.enabled`, `smLimit.enforce`, `memoryGuard.enabled`.

## Why: what was wrong under upstream + Sysbox

1. **No compute limit.** NvFractions caps memory only. With time-slicing the
   driver splits the GPU evenly regardless of fraction (0.25 vs 0.75 got
   56 / 56 TFLOPS); with `sm-sharing` all pods share the 100 % `default`
   namespace.
2. **Whole MPS tree in every pod.** The time-slicing identity mount gives each
   tenant `/run/nvidia-mps`: the root control socket and every other
   namespace's socket. Mutating commands need real UID 0 (Sysbox user
   namespaces block them), but any tenant could join another namespace and
   read the full MPS state.
3. **Wrong GPU memory under Sysbox, and stray limits elsewhere.** The NVIDIA
   toolkit's `apply-cuda-memory-limits` hook sets an NVML per-cgroup limit on
   the container scope. NVML resolves a process's limit from the nearest
   cgroup with an entry. Sysbox runs workloads in `<scope>/init.scope`, which
   was seen carrying other pods' values (6 of 12 pods wrong in a fast churn,
   some over-allocated). Entries with fractional pods' values were also found
   on **unrelated** pods' cgroups (GPU feature discovery, mpsd, system pods).
   NVML reports memory capped by the caller's cgroup, so GFD published
   `nvidia.com/gpu.memory=30720` on a 40 GB card and KAI sized every fraction
   from it (0.25 → 7.5 GiB), and mpsd saw a 4 GiB GPU. The writer of those
   strays was not identified; they are now detected and removed.

## Design

```
pod (gpu-fraction: 0.25)                     node
  │ CreateContainer (NRI)
  ▼
fractiond ── writes request ──▶ /run/gpu-fractioning/mps-ns/<ns>.json ──▶ mpsd
  │   waits ≤1.5s for <pipe>/shared/<ns>/control  ◀── namespace create/set ──┘
  │   (fails the creation if missing; kubelet retries)
  ▼
container: /tmp/nvidia-mps = <pipe>/shared/<ns>  (nothing else of the MPS tree)
           NVIDIA_GPU_MEMORY_LIMIT → toolkit hook → NVML cgroup limit

metricsd (every 0.5s): assert limit on the container's cgroup subtree,
                       kill native CUDA contexts in sm-limit containers
         (every 10s):  reset limit entries on non-fractional kubepods cgroups
                       older than 30s
```

- Namespace name: `fc` + 28 hex of SHA-256(container ID). It is deterministic
  for fractiond, its audit and mpsd, but opaque to tenants, who can list the
  shared server's namespaces through their own socket.
- The retroactive-enforcement audit uses the same compute-mode resolver and
  container-aware mount, so correctly scoped containers are not stopped.
- mpsd talks to the control daemon only when the request directory changes,
  or every 10s (each command is written to the MPS log).

## Results on dev03 (v0.1.6-sm.3)

| Test | Result |
|---|---|
| 12 Sysbox pods, alternating 0.1 / 0.75 | 12/12 exact: 4.0 GiB / 10 SMs, 30.0 GiB / 80 SMs (upstream: up to 6/12 wrong) |
| 0.25 and 0.75 at the same time | 26 vs 80 SMs, 30.2 vs 91.6 TFLOPS (1 : 3) |
| Pod annotates `gpu-compute.mode: time-slicing` (enforce on) | still 26 SMs |
| `gpu-sm-percent: 20` on 0.5 / `90` on 0.25 | 20 SMs / still 26 SMs |
| Root in the pod skips MPS | process killed by metricsd; neighbour unaffected |
| Tenant view | only `/tmp/nvidia-mps/control`; no `/run/nvidia-mps`, request or map dir; `client list` empty; `namespace set` refused (UID) |
| MPS daemon restart with a capped pod running | namespace recreated; new GPU work in the same pod still 26 SMs / 10 GiB |
| Stray sweep | 25 stray entries reset; GFD back to 40960 MiB within one relabel |

## Known limits

- `namespace list` through a tenant's own socket still shows every
  namespace's SM % and memory limit (names are opaque). MPS answers it on any
  socket.
- A bypassing process runs for up to ~0.5 s before it is killed.
- The stray-limit writer is unknown; the sweep repairs entries every 10 s.
- Tested on one GPU per node only.

## Building on a node without a registry

`hack/fork/build-local.sh [TAG]` runs the upstream `make docker-build`
(needs the docker buildx plugin) and imports the four images into RKE2's
containerd. `hack/fork/nvml_*.py` are the read-only NVML inspection scripts
(and one setter) used in the investigation.
