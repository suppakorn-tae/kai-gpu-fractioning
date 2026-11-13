# Prototype: every 0.5s, for each running fraction pod using nvidia-sysbox, set the
# KAI-assigned GPU memory limit on its container cgroup AND every descendant cgroup.
import ctypes, glob, json, os, re, subprocess, time
nvml = ctypes.CDLL("libnvidia-ml.so.1")
class Lim(ctypes.Structure):
    _fields_ = [("ns", ctypes.c_char_p), ("soft", ctypes.c_uint64), ("hard", ctypes.c_uint64)]
nvml.nvmlInit_v2(); dev = ctypes.c_void_p(); nvml.nvmlDeviceGetHandleByIndex_v2(0, ctypes.byref(dev))
KC = os.environ.get("KUBECTL", "kubectl")
def pods():
    out = subprocess.run([KC, "get", "pods", "-A", "-o", "json"], capture_output=True, text=True).stdout
    for p in json.loads(out)["items"]:
        if p["spec"].get("runtimeClassName") != "nvidia-sysbox" or p["status"].get("phase") != "Running": continue
        for k, v in p["metadata"].get("annotations", {}).items():
            m = re.match(r"^nvidia\.com/container\.[^.]+\.gpu-memory\.request$", k)
            mm = re.match(r"^(\d+)(Mi|Gi)$", v)
            if m and mm:
                yield p["metadata"]["uid"].replace("-", "_"), int(mm.group(1)) * (1024 if mm.group(2) == "Gi" else 1)
seen = {}
while True:
    for uid, mib in pods():
        for scope in glob.glob(f"/sys/fs/cgroup/kubepods.slice/*/*pod{uid}.slice/cri-containerd-*.scope"):
            for root, dirs, _ in os.walk(scope):
                b = mib * 2**20
                r = nvml.nvmlDeviceSetMemoryLimits_v1(dev, ctypes.byref(Lim(root.encode(), b, b)))
                if seen.get(root) != mib: print(time.strftime("%H:%M:%S"), "set", mib, "MiB ret", r, root[-60:], flush=True); seen[root] = mib
    time.sleep(0.5)
