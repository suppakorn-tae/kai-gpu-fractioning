// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package enforce stops sm-limit containers from running GPU work outside
// their MPS namespace.
//
// An sm-limit container's SM cap lives on its MPS namespace. A process in the
// container that skips MPS (e.g. by pointing CUDA_MPS_PIPE_DIRECTORY
// elsewhere) gets an ordinary CUDA context and the whole GPU's SMs. NVML
// reports such processes as compute processes that are not MPS clients; the
// enforcer kills those whose cgroup belongs to an sm-limit container.
package enforce

import (
	"context"
	"log/slog"
	"syscall"
	"time"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mapping/store"
)

// ComputeModeSMLimit is the mapping's compute mode for capped containers.
const ComputeModeSMLimit = "sm-limit"

// ProcessLister returns PIDs running a native (non-MPS) CUDA context on any GPU.
type ProcessLister interface {
	NativeComputePIDs() ([]uint32, error)
}

// CgroupResolver maps a PID to its cgroup paths.
type CgroupResolver interface {
	CgroupPaths(pid uint32) ([]string, error)
}

// Killer terminates a process.
type Killer func(pid int) error

// Enforcer kills native GPU contexts in sm-limit containers.
type Enforcer struct {
	interval time.Duration
	procs    ProcessLister
	cgroups  CgroupResolver
	reader   store.Reader
	kill     Killer
	log      *slog.Logger
}

// New returns an Enforcer. kill defaults to SIGKILL.
func New(interval time.Duration, procs ProcessLister, cgroups CgroupResolver, reader store.Reader, kill Killer, log *slog.Logger) *Enforcer {
	if kill == nil {
		kill = func(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
	}
	if log == nil {
		log = slog.Default()
	}
	return &Enforcer{interval: interval, procs: procs, cgroups: cgroups, reader: reader, kill: kill, log: log}
}

// Run enforces every interval until ctx is done.
func (e *Enforcer) Run(ctx context.Context) {
	if e.interval <= 0 {
		e.log.Info("sm-limit enforcement disabled")
		return
	}
	e.log.Info("sm-limit enforcement started", "interval", e.interval)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		e.Enforce()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Enforce performs one pass and returns how many processes it killed.
func (e *Enforcer) Enforce() int {
	pids, err := e.procs.NativeComputePIDs()
	if err != nil {
		e.log.Debug("listing GPU processes", "error", err)
		return 0
	}
	killed := 0
	for _, pid := range pids {
		paths, err := e.cgroups.CgroupPaths(pid)
		if err != nil {
			continue // exited
		}
		c, ok := e.reader.MatchCgroupPaths(paths)
		if !ok || c.ComputeMode != ComputeModeSMLimit {
			continue
		}
		if err := e.kill(int(pid)); err != nil {
			e.log.Warn("failed to stop native GPU context in sm-limit container", "pid", pid,
				"pod", c.Pod, "namespace", c.Namespace, "container", c.Container, "error", err)
			continue
		}
		killed++
		e.log.Warn("killed native GPU context in sm-limit container (bypassed its MPS namespace)",
			"pid", pid, "pod", c.Pod, "namespace", c.Namespace, "container", c.Container, "podUID", c.PodUID)
	}
	return killed
}
