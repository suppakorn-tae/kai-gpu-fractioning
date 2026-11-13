// Copyright 2026 suppakorn-tae. Part of a fork of kai-scheduler/gpu-fractioning.
// SPDX-License-Identifier: Apache-2.0

package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mapping/store"
)

// fakeLimiter models the driver: entries keyed by cgroup path, and a lookup
// that returns the nearest ancestor (including self) carrying an entry.
type fakeLimiter struct {
	entries map[string]uint64
	sets    int
}

func (f *fakeLimiter) Minors() []int { return []int{0} }

func (f *fakeLimiter) HardLimit(_ int, p string) (uint64, bool, error) {
	for q := p; q != "/" && q != "."; q = filepath.Dir(q) {
		if v, ok := f.entries[q]; ok {
			return v, true, nil
		}
	}
	return 0, false, nil
}

func (f *fakeLimiter) SetLimits(_ int, p string, _, hard uint64) error {
	f.entries[p] = hard
	f.sets++
	return nil
}

type fakeReader struct{ c []store.ContainerInfo }

func (r fakeReader) MatchCgroupPaths([]string) (store.ContainerInfo, bool) {
	return store.ContainerInfo{}, false
}
func (r fakeReader) ActivePodUIDs() map[string]struct{}      { return nil }
func (r fakeReader) ActiveContainers() []store.ContainerInfo { return r.c }

const (
	podUID = "6f2dd019-2eb1-4a38-a70b-12cbbc001dec"
	ctrID  = "091d5e16"
)

func mkTree(t *testing.T, qos string) (root, scope string) {
	t.Helper()
	root = t.TempDir()
	uid := strings.ReplaceAll(podUID, "-", "_")
	scope = filepath.Join(root, "kubepods.slice", "kubepods-"+qos+".slice",
		"kubepods-"+qos+"-pod"+uid+".slice", "cri-containerd-"+ctrID+".scope")
	for _, d := range []string{"init.scope", "init.scope/system.slice/ssh.service"} {
		if err := os.MkdirAll(filepath.Join(scope, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, scope
}

func newGuard(root string, l Limiter, mibs int64) *Guard {
	r := fakeReader{c: []store.ContainerInfo{{Pod: "p", Namespace: "n", PodUID: podUID, ContainerID: ctrID, RequestedMemoryMiB: mibs}}}
	return New(Config{CgroupRoot: root, NVMLCgroupPrefix: "/sys/fs/cgroup"}, r, l, nil)
}

func nv(root, dir string) string { return "/sys/fs/cgroup" + strings.TrimPrefix(dir, root) }

func TestContainerCgroupDirQoSLayouts(t *testing.T) {
	for _, qos := range []string{"burstable", "besteffort"} {
		root, scope := mkTree(t, qos)
		got, ok := ContainerCgroupDir(root, podUID, ctrID)
		if !ok || got != scope {
			t.Fatalf("qos %s: got %q ok=%v, want %q", qos, got, ok, scope)
		}
	}
	if _, ok := ContainerCgroupDir(t.TempDir(), podUID, ctrID); ok {
		t.Fatal("found a cgroup that does not exist")
	}
}

// A stray entry on init.scope (another pod's 30 GiB) must be overwritten with
// the container's own 4 GiB; deeper cgroups that inherit correctly are left alone.
func TestReconcileOverridesStrayChildEntry(t *testing.T) {
	root, scope := mkTree(t, "burstable")
	l := &fakeLimiter{entries: map[string]uint64{
		nv(root, scope): 4 << 30, // set by the toolkit hook
		nv(root, filepath.Join(scope, "init.scope")): 30 << 30, // stray
	}}
	g := newGuard(root, l, 4096)
	g.Reconcile()

	deep := nv(root, filepath.Join(scope, "init.scope/system.slice/ssh.service"))
	for _, p := range []string{nv(root, scope), nv(root, filepath.Join(scope, "init.scope")), deep} {
		if v, _, _ := l.HardLimit(0, p); v != 4<<30 {
			t.Fatalf("%s: effective limit %d, want 4 GiB", p, v)
		}
	}
	if l.sets != 1 {
		t.Fatalf("expected exactly one corrective Set, got %d", l.sets)
	}

	// Steady state: nothing to do.
	g.Reconcile()
	if l.sets != 1 {
		t.Fatalf("steady state wrote again: %d sets", l.sets)
	}
}

// With no entry at all (hook did not run), the guard sets the container scope,
// and children inherit it.
func TestReconcileSetsMissingLimit(t *testing.T) {
	root, scope := mkTree(t, "burstable")
	l := &fakeLimiter{entries: map[string]uint64{}}
	newGuard(root, l, 10240).Reconcile()
	if v, ok, _ := l.HardLimit(0, nv(root, filepath.Join(scope, "init.scope"))); !ok || v != 10<<30 {
		t.Fatalf("init.scope effective limit = %d (ok=%v), want 10 GiB", v, ok)
	}
}

func TestReconcileSkipsContainersWithoutMemory(t *testing.T) {
	root, _ := mkTree(t, "burstable")
	l := &fakeLimiter{entries: map[string]uint64{}}
	newGuard(root, l, 0).Reconcile()
	if l.sets != 0 {
		t.Fatalf("wrote %d limits for a container without a memory allotment", l.sets)
	}
}

func age(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chtimes(p, old, old)
		}
		return nil
	})
}

// Strays on cgroups that are not fractional containers are reset to unlimited
// (including a pod slice and its child carrying the same stray value); the
// fractional container keeps its limit; young cgroups are left alone.
func TestSweepClearsStraysOutsideFractionalContainers(t *testing.T) {
	root, scope := mkTree(t, "burstable")
	other := filepath.Join(root, "kubepods.slice", "kubepods-besteffort.slice", "kubepods-besteffort-podgfd.slice")
	otherScope := filepath.Join(other, "cri-containerd-gfd.scope")
	young := filepath.Join(root, "kubepods.slice", "kubepods-besteffort.slice", "kubepods-besteffort-podnew.slice", "cri-containerd-new.scope")
	for _, d := range []string{otherScope, young} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	age(t, root)
	now := time.Now()
	_ = os.Chtimes(young, now, now)
	_ = os.Chtimes(filepath.Dir(young), now, now)

	l := &fakeLimiter{entries: map[string]uint64{
		nv(root, scope):      4 << 30,  // fractional container: keep
		nv(root, other):      30 << 30, // stray on a pod slice
		nv(root, otherScope): 30 << 30, // and on its container, same value
		nv(root, young):      8 << 30,  // too young to judge
	}}
	g := newGuard(root, l, 4096)
	g.Reconcile()

	if v, _, _ := l.HardLimit(0, nv(root, scope)); v != 4<<30 {
		t.Fatalf("fractional container limit changed to %d", v)
	}
	for _, p := range []string{other, otherScope} {
		if v, _, _ := l.HardLimit(0, nv(root, p)); v != Unlimited {
			t.Fatalf("%s: stray not cleared (%d)", p, v)
		}
	}
	if v, _, _ := l.HardLimit(0, nv(root, young)); v != 8<<30 {
		t.Fatal("young cgroup was swept")
	}
}
