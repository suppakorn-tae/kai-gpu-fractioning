// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nsreconcile

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// NVMLDeviceMemory implements DeviceMemory with NVML, caching each device's
// total for cacheTTL.
type NVMLDeviceMemory struct {
	mu    sync.Mutex
	cache map[string]cached
	init  bool
}

// cached expires: NVML reports memory as limited by the caller's cgroup, so a
// stray limit entry on mpsd's own cgroup (cleared later by metricsd's guard)
// must not stick for the daemon's lifetime.
type cached struct {
	mib int64
	at  time.Time
}

const cacheTTL = time.Minute

// TotalMiB implements DeviceMemory.
func (d *NVMLDeviceMemory) TotalMiB(uuid string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if v, ok := d.cache[uuid]; ok && time.Since(v.at) < cacheTTL {
		return v.mib, nil
	}
	if !d.init {
		if ret := nvml.Init(); !errors.Is(ret, nvml.SUCCESS) {
			return 0, fmt.Errorf("initialize NVML: %s", ret.Error())
		}
		d.init = true
		d.cache = map[string]cached{}
	}
	var total int64
	if uuid != "" {
		dev, ret := nvml.DeviceGetHandleByUUID(uuid)
		if !errors.Is(ret, nvml.SUCCESS) {
			return 0, fmt.Errorf("GPU %s: %s", uuid, ret.Error())
		}
		mem, ret := dev.GetMemoryInfo()
		if !errors.Is(ret, nvml.SUCCESS) {
			return 0, fmt.Errorf("GPU %s memory: %s", uuid, ret.Error())
		}
		total = int64(mem.Total >> 20)
	} else {
		n, ret := nvml.DeviceGetCount()
		if !errors.Is(ret, nvml.SUCCESS) {
			return 0, fmt.Errorf("device count: %s", ret.Error())
		}
		for i := 0; i < n; i++ {
			dev, ret := nvml.DeviceGetHandleByIndex(i)
			if !errors.Is(ret, nvml.SUCCESS) {
				continue
			}
			if mem, ret := dev.GetMemoryInfo(); errors.Is(ret, nvml.SUCCESS) {
				total = max(total, int64(mem.Total>>20))
			}
		}
	}
	if total <= 0 {
		return 0, errors.New("no GPU memory reported")
	}
	d.cache[uuid] = cached{mib: total, at: time.Now()}
	return total, nil
}
