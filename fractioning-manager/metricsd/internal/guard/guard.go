// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package guard keeps the NVML per-cgroup GPU memory limit of every
// GPU-fractioning container correct on the container's whole cgroup subtree.
//
// The NVIDIA container toolkit's apply-cuda-memory-limits CDI hook sets the
// limit once, on the container's own cgroup, at creation. The driver resolves a
// process's limit from the nearest cgroup that carries an entry, walking up.
// Runtimes that run the workload in a child cgroup (Sysbox puts every process in
// <container>.scope/init.scope; systemd-in-container images create deeper
// slices) are exposed to a stray entry on such a child — observed in practice
// carrying another pod's limit — which then wins over the container's correct
// one, so the workload gets the wrong (sometimes larger) GPU memory.
//
// The guard re-reads the effective limit of each cgroup in the subtree on every
// tick and re-asserts the container's allotment wherever it differs. Explicit
// entries on descendants are harmless when they match, so the container's limit
// holds no matter which cgroup its processes live in.
package guard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mapping/store"
)

const mib = uint64(1) << 20

// sweepMinAge protects cgroups the mapping may not list yet.
const sweepMinAge = 30 * time.Second

// Unlimited is NVML's "no limit" value (DEVICE_MEMORY_LIMIT_MAX).
const Unlimited = ^uint64(0)

// Limiter reads and writes NVML per-cgroup GPU memory limits. GPUs are
// addressed by Linux device minor number, matching store.GPUDevice.
type Limiter interface {
	// Minors lists the minor numbers of the GPUs on this node.
	Minors() []int
	// HardLimit returns the effective hard limit (bytes) NVML reports for the
	// cgroup path on the GPU, and false when no entry applies.
	HardLimit(minor int, cgroupPath string) (uint64, bool, error)
	// SetLimits writes soft and hard limits (bytes) for the cgroup path.
	SetLimits(minor int, cgroupPath string, soft, hard uint64) error
}

// Config configures the guard.
type Config struct {
	// Interval between passes. Zero disables the guard.
	Interval time.Duration
	// CgroupRoot is where the host's cgroup v2 hierarchy is visible to this
	// process (the operator mounts the host's /sys/fs/cgroup there).
	CgroupRoot string
	// NVMLCgroupPrefix is the prefix NVML expects for cgroup paths: the host
	// path of the cgroup v2 mount, normally /sys/fs/cgroup.
	NVMLCgroupPrefix string
	// SweepInterval is how often cgroups outside fractional containers are
	// checked for stray limit entries (default 10s; negative disables).
	SweepInterval time.Duration
}

// Guard reconciles per-cgroup GPU memory limits.
type Guard struct {
	cfg     Config
	reader  store.Reader
	limiter Limiter
	log     *slog.Logger

	// asserted remembers the value last written per cgroup/GPU so corrections
	// (a value that changed underneath us) are logged once, not every tick.
	asserted map[string]uint64
	// lastSweep is when stray entries were last swept.
	lastSweep time.Time
}

// New returns a Guard. reader supplies the GPU-fractioning containers recorded
// by fractiond.
func New(cfg Config, reader store.Reader, limiter Limiter, log *slog.Logger) *Guard {
	if cfg.CgroupRoot == "" {
		cfg.CgroupRoot = "/sys/fs/cgroup"
	}
	if cfg.NVMLCgroupPrefix == "" {
		cfg.NVMLCgroupPrefix = "/sys/fs/cgroup"
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = 10 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Guard{cfg: cfg, reader: reader, limiter: limiter, log: log, asserted: map[string]uint64{}}
}

// Run reconciles every Interval until ctx is done.
func (g *Guard) Run(ctx context.Context) {
	if g.cfg.Interval <= 0 {
		g.log.Info("GPU memory guard disabled")
		return
	}
	g.log.Info("GPU memory guard started", "interval", g.cfg.Interval, "cgroupRoot", g.cfg.CgroupRoot)
	t := time.NewTicker(g.cfg.Interval)
	defer t.Stop()
	for {
		g.Reconcile()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Reconcile performs one pass over all recorded containers, and periodically
// sweeps stray entries off every other Kubernetes cgroup.
func (g *Guard) Reconcile() {
	live := map[string]struct{}{}
	var protected []string
	defer func() {
		if g.cfg.SweepInterval > 0 && time.Since(g.lastSweep) >= g.cfg.SweepInterval {
			g.sweep(protected)
			g.lastSweep = time.Now()
		}
	}()
	for _, c := range g.reader.ActiveContainers() {
		if c.RequestedMemoryMiB <= 0 || c.ContainerID == "" {
			continue
		}
		dir, ok := ContainerCgroupDir(g.cfg.CgroupRoot, c.PodUID, c.ContainerID)
		if !ok {
			continue // container not (or no longer) running on this node
		}
		protected = append(protected, dir)
		want := uint64(c.RequestedMemoryMiB) * mib
		minors := g.minorsFor(c)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fs.SkipDir // cgroup removed mid-walk
				}
				return err
			}
			if !d.IsDir() {
				return nil
			}
			nvmlPath := g.cfg.NVMLCgroupPrefix + strings.TrimPrefix(path, g.cfg.CgroupRoot)
			for _, minor := range minors {
				key := fmt.Sprintf("%d:%s", minor, nvmlPath)
				live[key] = struct{}{}
				g.assert(c, minor, nvmlPath, key, want)
			}
			return nil
		})
		if err != nil {
			g.log.Warn("walking container cgroup", "pod", c.Pod, "namespace", c.Namespace, "dir", dir, "error", err)
		}
	}
	for k := range g.asserted {
		if _, ok := live[k]; !ok {
			delete(g.asserted, k)
		}
	}
}

func (g *Guard) assert(c store.ContainerInfo, minor int, nvmlPath, key string, want uint64) {
	have, ok, err := g.limiter.HardLimit(minor, nvmlPath)
	if err != nil {
		g.log.Debug("reading GPU memory limit", "path", nvmlPath, "minor", minor, "error", err)
	}
	if ok && have == want {
		g.asserted[key] = want
		return
	}
	if err := g.limiter.SetLimits(minor, nvmlPath, want, want); err != nil {
		g.log.Warn("setting GPU memory limit", "pod", c.Pod, "namespace", c.Namespace, "path", nvmlPath, "minor", minor, "error", err)
		return
	}
	prev, seen := g.asserted[key]
	switch {
	case !seen:
		g.log.Info("asserted GPU memory limit", "pod", c.Pod, "namespace", c.Namespace, "cgroup", nvmlPath,
			"minor", minor, "limitMiB", want/mib, "foundMiB", mibOrNone(have, ok))
	case prev == want:
		g.log.Warn("corrected GPU memory limit changed underneath", "pod", c.Pod, "namespace", c.Namespace,
			"cgroup", nvmlPath, "minor", minor, "limitMiB", want/mib, "foundMiB", mibOrNone(have, ok))
	}
	g.asserted[key] = want
}

func mibOrNone(v uint64, ok bool) any {
	if !ok {
		return "none"
	}
	return v / mib
}

// minorsFor returns the GPUs a container was assigned, or every GPU on the node
// when the mapping does not record any (fractional containers whose GPU is
// injected through CDI carry no device nodes in the NRI view).
func (g *Guard) minorsFor(c store.ContainerInfo) []int {
	node := g.limiter.Minors()
	if len(c.GPUDevices) == 0 {
		return node
	}
	present := map[int]bool{}
	for _, m := range node {
		present[m] = true
	}
	var out []int
	for _, d := range c.GPUDevices {
		m := d.MinorNumber
		if m == 0 && d.Index != 0 {
			m = d.Index
		}
		if present[m] {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return node
	}
	return out
}

// ContainerCgroupDir locates a container's cgroup directory under root for the
// kubelet's systemd cgroup driver with containerd:
//
//	kubepods.slice/kubepods[-<qos>].slice/kubepods[-<qos>]-pod<uid>.slice/cri-containerd-<id>.scope
//
// The pod UID's dashes are escaped to underscores by systemd. The QoS level is
// not recorded, so each of the three layouts is tried.
func ContainerCgroupDir(root, podUID, containerID string) (string, bool) {
	if podUID == "" || containerID == "" {
		return "", false
	}
	uid := strings.ReplaceAll(podUID, "-", "_")
	scope := "cri-containerd-" + containerID + ".scope"
	for _, qos := range []string{"", "burstable", "besteffort"} {
		parent, podSlice := "kubepods.slice", "kubepods-pod"+uid+".slice"
		if qos != "" {
			parent = filepath.Join("kubepods.slice", "kubepods-"+qos+".slice")
			podSlice = "kubepods-" + qos + "-pod" + uid + ".slice"
		}
		dir := filepath.Join(root, parent, podSlice, scope)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir, true
		}
	}
	return "", false
}

// sweep resets limit entries on Kubernetes cgroups outside fractional
// containers. Such entries are never legitimate (only fractional containers
// get a GPU memory limit) but were observed on unrelated pods — including GPU
// feature discovery, whose NVML view then shrank and made it publish a smaller
// nvidia.com/gpu.memory, skewing every fraction the scheduler sized from it.
// The walk is top-down, so a cleared parent exposes a child's own entry in the
// same pass.
func (g *Guard) sweep(protected []string) {
	root := filepath.Join(g.cfg.CgroupRoot, "kubepods.slice")
	inside := func(p string) bool {
		for _, d := range protected {
			if p == d || strings.HasPrefix(p, d+"/") {
				return true
			}
		}
		return false
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if inside(path) {
			return fs.SkipDir
		}
		// A container's limit is set at creation, before fractiond's mapping
		// (written asynchronously) lists it: leave young cgroups alone.
		if info, err := d.Info(); err != nil || time.Since(info.ModTime()) < sweepMinAge {
			return fs.SkipDir
		}
		nvmlPath := g.cfg.NVMLCgroupPrefix + strings.TrimPrefix(path, g.cfg.CgroupRoot)
		parent := g.cfg.NVMLCgroupPrefix + strings.TrimPrefix(filepath.Dir(path), g.cfg.CgroupRoot)
		for _, minor := range g.limiter.Minors() {
			have, ok, _ := g.limiter.HardLimit(minor, nvmlPath)
			if !ok || have == Unlimited {
				continue
			}
			if ph, pok, _ := g.limiter.HardLimit(minor, parent); pok && ph == have {
				continue // inherited, the ancestor's entry is handled on its own visit
			}
			if err := g.limiter.SetLimits(minor, nvmlPath, Unlimited, Unlimited); err != nil {
				g.log.Warn("clearing stray GPU memory limit", "cgroup", nvmlPath, "minor", minor, "error", err)
				continue
			}
			g.log.Warn("cleared stray GPU memory limit on a non-fractional cgroup", "cgroup", nvmlPath,
				"minor", minor, "foundMiB", have/mib)
		}
		return nil
	})
}
