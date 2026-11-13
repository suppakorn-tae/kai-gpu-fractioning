# Read-only: list cgroups under kubepods.slice that carry their own NVML GPU
# memory limit entry (value differs from the parent's effective value).
import ctypes, os, sys
nvml = ctypes.CDLL("libnvidia-ml.so.1")
class Lim(ctypes.Structure):
    _fields_ = [("ns", ctypes.c_char_p), ("s", ctypes.c_uint64), ("h", ctypes.c_uint64), ("u", ctypes.c_uint64)]
nvml.nvmlInit_v2(); d = ctypes.c_void_p(); nvml.nvmlDeviceGetHandleByIndex_v2(0, ctypes.byref(d))
def get(p):
    l = Lim(p.encode(), 0, 0, 0)
    return l.h if nvml.nvmlDeviceGetMemoryLimits_v1(d, ctypes.byref(l)) == 0 else None
for root, dirs, _ in os.walk("/sys/fs/cgroup/kubepods.slice"):
    v, pv = get(root), get(os.path.dirname(root))
    if v is not None and v != pv:
        print("%.2f %s" % (v / 2**30, root.replace("/sys/fs/cgroup/", "")))
