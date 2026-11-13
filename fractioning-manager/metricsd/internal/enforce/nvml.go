// Copyright 2026 suppakorn-tae. Part of a fork of kai-scheduler/gpu-fractioning.
// SPDX-License-Identifier: Apache-2.0

package enforce

import (
	"errors"
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// NVMLProcesses lists native compute processes: those NVML reports as compute
// processes but not as MPS clients.
type NVMLProcesses struct{}

// NewNVMLProcesses initialises NVML (reference-counted; pair with Close).
func NewNVMLProcesses() (*NVMLProcesses, error) {
	if ret := nvml.Init(); !errors.Is(ret, nvml.SUCCESS) {
		return nil, fmt.Errorf("initialize NVML: %s", ret.Error())
	}
	return &NVMLProcesses{}, nil
}

// Close releases NVML.
func (NVMLProcesses) Close() { _ = nvml.Shutdown() }

// NativeComputePIDs implements ProcessLister.
func (NVMLProcesses) NativeComputePIDs() ([]uint32, error) {
	n, ret := nvml.DeviceGetCount()
	if !errors.Is(ret, nvml.SUCCESS) {
		return nil, fmt.Errorf("device count: %s", ret.Error())
	}
	var out []uint32
	for i := 0; i < n; i++ {
		dev, ret := nvml.DeviceGetHandleByIndex(i)
		if !errors.Is(ret, nvml.SUCCESS) {
			continue
		}
		compute, ret := dev.GetComputeRunningProcesses()
		if !errors.Is(ret, nvml.SUCCESS) {
			continue
		}
		mps := map[uint32]bool{}
		if clients, ret := dev.GetMPSComputeRunningProcesses(); errors.Is(ret, nvml.SUCCESS) {
			for _, p := range clients {
				mps[p.Pid] = true
			}
		} else {
			// Without the MPS list every client would look native; do nothing
			// rather than kill legitimate work.
			continue
		}
		for _, p := range compute {
			if !mps[p.Pid] {
				out = append(out, p.Pid)
			}
		}
	}
	return out, nil
}
