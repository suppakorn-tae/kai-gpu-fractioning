# Fork design: per-pod SM caps, scoped MPS mounts, Sysbox memory fix

Base: upstream v0.1.6 (pinned by KAI-Scheduler v0.18.3). Status: **design validated, code not written yet.**

## Findings that drive the design (dev03, A100-40GB, driver 615.71.09, Sysbox 0.7.1)
- Memory limits come from the Container Toolkit CDI hook `apply-cuda-memory-limits`
  (NVML `SetMemoryLimits_v1(cgroupPath)`), set on the container scope only.
- NVML keys limits by **full host cgroup path**; the **nearest entry wins**
  (scope=7G -> process sees 7; init.scope=8G -> sees 8).
- Under Sysbox, processes live in `<scope>/init.scope`; that child sometimes carries a
  stray entry with another pod's value -> wrong/over-allocated memory (6/12 in churn).
- Prototype `hack/fork/assert_limits.py` (assert KAI limit on the whole cgroup subtree
  every 0.5 s): **0/12** stale vs 1-6/12 without. A Set from a privileged pod with host
  `/sys/fs/cgroup` mounted at the same path works identically.
- `MPSPipeMount()` mounts the whole `/run/nvidia-mps` in time-slicing mode -> cross-tenant
  socket/dir access. Mutating MPS commands need real UID 0 (blocked by Sysbox userns).

## Planned changes
1. fractiond: new compute mode `sm-limit` (+ flags `--default-compute-mode`,
   `--enforce-compute-mode`). Writes a request file (ns, memMiB, pct), waits briefly for
   `<pipe>/shared/<ns>/control`, mounts only that dir at `/tmp/nvidia-mps`. Fails closed
   if not ready (kubelet retries). Records compute mode in the mapping.
2. mpsd: reconcile request dir -> `namespace create/set/delete` via the control CLI;
   pct = ceil(memMiB / device_total * 100) (+ optional lower request), pinned limit = memMiB.
3. metricsd: (a) guard: assert NVML limit on container cgroup subtree (host path,
   hostPath /sys/fs/cgroup mounted at /sys/fs/cgroup); (b) kill non-MPS GPU processes of
   sm-limit containers (NVML compute vs MPS process lists).
4. operator: flags, request-dir hostPath for fractiond+mpsd, cgroup mount for metricsd.
