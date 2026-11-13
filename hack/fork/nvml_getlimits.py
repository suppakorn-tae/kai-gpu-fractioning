# Read-only: query NVML per-cgroup GPU memory limits for the given cgroup paths.
import ctypes, sys
nvml = ctypes.CDLL("libnvidia-ml.so.1")
class Lim(ctypes.Structure):
    _fields_ = [("ns", ctypes.c_char_p), ("soft", ctypes.c_uint64), ("hard", ctypes.c_uint64), ("used", ctypes.c_uint64)]
assert nvml.nvmlInit_v2() == 0
dev = ctypes.c_void_p(); assert nvml.nvmlDeviceGetHandleByIndex_v2(0, ctypes.byref(dev)) == 0
fn = nvml.nvmlDeviceGetMemoryLimits_v1
for p in sys.argv[1:]:
    l = Lim(p.encode(), 0, 0, 0); r = fn(dev, ctypes.byref(l))
    if r == 0: print(f"ret=0 hard={l.hard/2**30:7.2f}GiB soft={l.soft/2**30:6.2f}GiB used={l.used/2**20:8.1f}MiB  {p}")
    else:      print(f"ret={r} (no limit)                                  {p}")
