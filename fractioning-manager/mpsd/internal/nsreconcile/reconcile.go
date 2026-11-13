// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package nsreconcile turns fractiond's sm-limit namespace requests
// (common/mpsns) into MPS namespaces on the shared server.
//
// For each request it keeps one namespace whose active-thread percentage is
// the container's GPU memory share of the device (rounded up; a request may
// only lower it) and whose pinned-memory limit is the memory allotment. It
// deletes namespaces it owns (mpsns.NamePrefix) once their request is gone.
// It only ever runs the control CLI against the daemon mpsd supervises, so a
// daemon restart (which drops all namespaces) is healed on the next pass.
package nsreconcile

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mpsns"
)

// Runner executes nvidia-cuda-mps-control with the given arguments.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// DeviceMemory reports a GPU's total memory in MiB. uuid may be empty, meaning
// "the node's GPU" (the largest, if several).
type DeviceMemory interface {
	TotalMiB(uuid string) (int64, error)
}

// Config configures the reconciler.
type Config struct {
	RequestDir string
	Server     string        // MPS server the namespaces live on (mpsns.SharedServer)
	Interval   time.Duration // pass interval; keep well under fractiond's --namespace-wait
}

// Reconciler keeps MPS namespaces in line with the request files.
type Reconciler struct {
	cfg  Config
	run  Runner
	mem  DeviceMemory
	log  *slog.Logger
	last map[string]state // last applied state, for logging changes only
}

type state struct {
	pct    float64
	memMiB int64
}

// New returns a Reconciler.
func New(cfg Config, run Runner, mem DeviceMemory, log *slog.Logger) *Reconciler {
	if cfg.Server == "" {
		cfg.Server = mpsns.SharedServer
	}
	if cfg.RequestDir == "" {
		cfg.RequestDir = mpsns.DefaultRequestDir
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 100 * time.Millisecond
	}
	if log == nil {
		log = slog.Default()
	}
	return &Reconciler{cfg: cfg, run: run, mem: mem, log: log, last: map[string]state{}}
}

// Run reconciles every Interval until ctx is done.
func (r *Reconciler) Run(ctx context.Context) {
	r.log.Info("MPS namespace reconciler started", "requestDir", r.cfg.RequestDir, "server", r.cfg.Server, "interval", r.cfg.Interval)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		if err := r.Reconcile(ctx); err != nil {
			r.log.Debug("namespace reconcile pass incomplete", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SMPercent derives the active-thread percentage for a request.
func SMPercent(memMiB, totalMiB int64, requested int) int {
	if totalMiB <= 0 || memMiB <= 0 {
		return 1
	}
	pct := int(math.Ceil(float64(memMiB) * 100 / float64(totalMiB)))
	pct = max(1, min(100, pct))
	if requested > 0 && requested < pct {
		pct = requested
	}
	return pct
}

// Reconcile performs one pass.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	reqs, errs := mpsns.List(r.cfg.RequestDir)
	for _, err := range errs {
		r.log.Warn("ignoring unreadable namespace request", "error", err)
	}
	have, err := r.list(ctx)
	if err != nil {
		return err // daemon not up yet, or restarting
	}

	for ns, req := range reqs {
		uuid := ""
		if len(req.GPUUUIDs) > 0 {
			uuid = req.GPUUUIDs[0]
		}
		total, err := r.mem.TotalMiB(uuid)
		if err != nil {
			r.log.Warn("cannot size namespace: unknown GPU memory", "namespace", ns, "gpu", uuid, "error", err)
			continue
		}
		want := state{pct: float64(SMPercent(req.MemoryMiB, total, req.SMPercent)), memMiB: req.MemoryMiB}
		cur, exists := have[ns]
		if !exists {
			if _, err := r.run.Run(ctx, "namespace", "create", ns, "--server="+r.cfg.Server); err != nil {
				r.log.Warn("creating MPS namespace", "namespace", ns, "error", err)
				continue
			}
		}
		if !exists || cur.pct != want.pct || cur.memMiB != want.memMiB {
			if _, err := r.run.Run(ctx, "namespace", "set", ns, "--server="+r.cfg.Server,
				"--active-thread-percentage="+strconv.Itoa(int(want.pct)),
				"--pinned-memory-limit="+strconv.FormatInt(want.memMiB, 10)+"M"); err != nil {
				r.log.Warn("setting MPS namespace limits", "namespace", ns, "error", err)
				continue
			}
		}
		if r.last[ns] != want {
			r.log.Info("MPS namespace ready", "namespace", ns, "pod", req.PodNS+"/"+req.Pod, "container", req.Container,
				"smPercent", want.pct, "memoryMiB", want.memMiB, "gpuTotalMiB", total, "requestedSMPercent", req.SMPercent)
			r.last[ns] = want
		}
	}

	for ns := range have {
		if _, wanted := reqs[ns]; wanted || !mpsns.IsOwned(ns) {
			continue
		}
		if _, err := r.run.Run(ctx, "namespace", "delete", ns, "--server="+r.cfg.Server); err != nil {
			r.log.Warn("deleting MPS namespace", "namespace", ns, "error", err)
			continue
		}
		delete(r.last, ns)
		r.log.Info("deleted MPS namespace (request gone)", "namespace", ns)
	}
	return nil
}

// list parses `namespace list --server=<s> --format=csv,noheader`:
// NAME,SERVER,SERVER STATUS,PINNED MEMORY LIMIT,ACTIVE THREAD PERCENTAGE,CLIENT PRIORITY,PIPE DIRECTORY
func (r *Reconciler) list(ctx context.Context) (map[string]state, error) {
	out, err := r.run.Run(ctx, "namespace", "list", "--server="+r.cfg.Server, "--format=csv,noheader")
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing namespace list: %w", err)
	}
	have := map[string]state{}
	for _, f := range rows {
		if len(f) < 5 || f[1] != r.cfg.Server {
			continue
		}
		pct, _ := strconv.ParseFloat(strings.TrimSpace(f[4]), 64)
		have[strings.TrimSpace(f[0])] = state{pct: pct, memMiB: parseMiB(strings.TrimSpace(f[3]))}
	}
	return have, nil
}

// parseMiB converts "13516M", "10G", "1024K" to MiB; "unlimited" (or anything
// unparseable) to 0.
func parseMiB(s string) int64 {
	if len(s) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil {
		return 0
	}
	switch s[len(s)-1] {
	case 'K':
		return int64(math.Round(v / 1024))
	case 'M':
		return int64(math.Round(v))
	case 'G':
		return int64(math.Round(v * 1024))
	}
	return 0
}

// CLIRunner runs the MPS control binary against a pipe directory.
type CLIRunner struct {
	Binary   string
	Protocol string // value for -p ("3")
	PipeDir  string
}

// Run implements Runner.
func (c CLIRunner) Run(ctx context.Context, args ...string) (string, error) {
	full := args
	if c.Protocol != "" {
		full = append([]string{"-p", c.Protocol}, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Binary, full...)
	cmd.Env = append(cmd.Environ(), "CUDA_MPS_PIPE_DIRECTORY="+c.PipeDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", c.Binary, strings.Join(full, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
