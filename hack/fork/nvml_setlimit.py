# Set an NVML per-cgroup GPU memory limit: nvml_setlimit.py <cgroup-path> <GiB|max>
import ctypes, sys
nvml = ctypes.CDLL("libnvidia-ml.so.1")
class Lim(ctypes.Structure):
    _fields_ = [("ns", ctypes.c_char_p), ("soft", ctypes.c_uint64), ("hard", ctypes.c_uint64)]
assert nvml.nvmlInit_v2() == 0
dev = ctypes.c_void_p(); assert nvml.nvmlDeviceGetHandleByIndex_v2(0, ctypes.byref(dev)) == 0
b = 2**64 - 1 if sys.argv[2] == "max" else int(float(sys.argv[2]) * 2**30)
print("set", sys.argv[1], sys.argv[2], "-> ret", nvml.nvmlDeviceSetMemoryLimits_v1(dev, ctypes.byref(Lim(sys.argv[1].encode(), b, b))))
