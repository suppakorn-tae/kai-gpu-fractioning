// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package guard

import (
	"errors"
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// NVMLLimiter implements Limiter with NVML's per-cgroup memory limits
// (nvmlDeviceGet/SetMemoryLimits_v1, driver r615+).
type NVMLLimiter struct {
	devices map[int]nvml.Device // by minor number
	minors  []int
}

// NewNVMLLimiter initialises NVML and indexes the node's GPUs by minor number.
// The caller must call Close.
func NewNVMLLimiter() (*NVMLLimiter, error) {
	if ret := nvml.Init(); !errors.Is(ret, nvml.SUCCESS) {
		return nil, fmt.Errorf("initialize NVML: %s", ret.Error())
	}
	n, ret := nvml.DeviceGetCount()
	if !errors.Is(ret, nvml.SUCCESS) {
		_ = nvml.Shutdown()
		return nil, fmt.Errorf("get device count: %s", ret.Error())
	}
	l := &NVMLLimiter{devices: map[int]nvml.Device{}}
	for i := 0; i < n; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if !errors.Is(ret, nvml.SUCCESS) {
			continue
		}
		minor, ret := dev.GetMinorNumber()
		if !errors.Is(ret, nvml.SUCCESS) {
			continue
		}
		l.devices[minor] = dev
		l.minors = append(l.minors, minor)
	}
	if len(l.minors) == 0 {
		_ = nvml.Shutdown()
		return nil, errors.New("no GPUs found")
	}
	return l, nil
}

// Minors implements Limiter.
func (l *NVMLLimiter) Minors() []int { return l.minors }

// HardLimit implements Limiter. Driver r615 answers ERROR_NOT_SUPPORTED (and
// may answer ERROR_NOT_FOUND) for a cgroup with no applicable entry.
func (l *NVMLLimiter) HardLimit(minor int, cgroupPath string) (uint64, bool, error) {
	dev, ok := l.devices[minor]
	if !ok {
		return 0, false, fmt.Errorf("unknown GPU minor %d", minor)
	}
	lim, ret := dev.GetMemoryLimits_v1(cgroupPath)
	switch {
	case errors.Is(ret, nvml.SUCCESS):
		return lim.HardLimit, true, nil
	case errors.Is(ret, nvml.ERROR_NOT_FOUND), errors.Is(ret, nvml.ERROR_NOT_SUPPORTED):
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("get memory limits: %s", ret.Error())
	}
}

// SetLimits implements Limiter.
func (l *NVMLLimiter) SetLimits(minor int, cgroupPath string, soft, hard uint64) error {
	dev, ok := l.devices[minor]
	if !ok {
		return fmt.Errorf("unknown GPU minor %d", minor)
	}
	if ret := dev.SetMemoryLimits_v1(cgroupPath, soft, hard); !errors.Is(ret, nvml.SUCCESS) {
		return fmt.Errorf("set memory limits: %s", ret.Error())
	}
	return nil
}

// Close releases NVML.
func (l *NVMLLimiter) Close() error {
	if ret := nvml.Shutdown(); !errors.Is(ret, nvml.SUCCESS) {
		return fmt.Errorf("shutdown NVML: %s", ret.Error())
	}
	return nil
}
