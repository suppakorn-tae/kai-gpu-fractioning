// Copyright 2026 suppakorn-tae. Part of a fork of kai-scheduler/gpu-fractioning.
// SPDX-License-Identifier: Apache-2.0

package enforce

import (
	"errors"
	"strings"
	"testing"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mapping/store"
)

type pids []uint32

func (p pids) NativeComputePIDs() ([]uint32, error) { return p, nil }

type cgroups map[uint32]string

func (c cgroups) CgroupPaths(pid uint32) ([]string, error) {
	if p, ok := c[pid]; ok {
		return []string{p}, nil
	}
	return nil, errors.New("gone")
}

type byID []store.ContainerInfo

func (s byID) MatchCgroupPaths(paths []string) (store.ContainerInfo, bool) {
	for _, p := range paths {
		for _, c := range s {
			if strings.Contains(p, c.ContainerID) {
				return c, true
			}
		}
	}
	return store.ContainerInfo{}, false
}
func (byID) ActivePodUIDs() map[string]struct{}        { return nil }
func (s byID) ActiveContainers() []store.ContainerInfo { return s }

func TestEnforceKillsOnlyNativeContextsInSMLimitContainers(t *testing.T) {
	reader := byID{
		{ContainerID: "capped", Pod: "inst", ComputeMode: "sm-limit"},
		{ContainerID: "sliced", Pod: "job", ComputeMode: "time-slicing"},
	}
	cg := cgroups{
		10: "/../kubepods-burstable-podx.slice/cri-containerd-capped.scope/init.scope",
		11: "/../kubepods-besteffort-pody.slice/cri-containerd-sliced.scope",
		12: "/system.slice/host-tool.service", // not a fractional container
	}
	var killed []int
	e := New(1, pids{10, 11, 12, 13}, cg, reader, func(pid int) error { killed = append(killed, pid); return nil }, nil)
	if n := e.Enforce(); n != 1 || len(killed) != 1 || killed[0] != 10 {
		t.Fatalf("killed %v (n=%d), want only pid 10", killed, n)
	}
}
