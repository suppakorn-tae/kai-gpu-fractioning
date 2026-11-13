// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nsreconcile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mpsns"
)

// fakeMPS emulates the control CLI's namespace commands.
type fakeMPS struct {
	ns    map[string][2]string // name -> {pinned, pct}
	calls []string
	down  bool
}

func newFakeMPS() *fakeMPS {
	return &fakeMPS{ns: map[string][2]string{"default": {"unlimited", "100.00"}}}
}

func (f *fakeMPS) Run(_ context.Context, args ...string) (string, error) {
	if f.down {
		return "", errors.New("daemon not running")
	}
	f.calls = append(f.calls, strings.Join(args, " "))
	switch {
	case args[1] == "list":
		var names []string
		for n := range f.ns {
			names = append(names, n)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, n := range names {
			fmt.Fprintf(&b, "%s,shared,Ready,%s,%s,0,/run/nvidia-mps/shared/%s\n", n, f.ns[n][0], f.ns[n][1], n)
		}
		return b.String(), nil
	case args[1] == "create":
		f.ns[args[2]] = [2]string{"unlimited", "100.00"}
	case args[1] == "delete":
		delete(f.ns, args[2])
	case args[1] == "set":
		v := f.ns[args[2]]
		for _, a := range args[3:] {
			if p, ok := strings.CutPrefix(a, "--active-thread-percentage="); ok {
				n, _ := strconv.Atoi(p)
				v[1] = fmt.Sprintf("%d.00", n)
			}
			if p, ok := strings.CutPrefix(a, "--pinned-memory-limit="); ok {
				v[0] = p
			}
		}
		f.ns[args[2]] = v
	}
	return "", nil
}

type fixedMem int64

func (m fixedMem) TotalMiB(string) (int64, error) { return int64(m), nil }

func TestSMPercent(t *testing.T) {
	for _, tc := range []struct {
		mem, total int64
		req, want  int
	}{
		{10240, 40960, 0, 25},
		{4096, 40960, 0, 10},
		{13516, 40960, 0, 33}, // 32.998 -> ceil
		{30720, 40960, 0, 75},
		{40960, 40960, 0, 100},
		{50000, 40960, 0, 100},
		{1, 40960, 0, 1},
		{10240, 40960, 20, 20}, // may lower
		{10240, 40960, 90, 25}, // never raise
	} {
		if got := SMPercent(tc.mem, tc.total, tc.req); got != tc.want {
			t.Errorf("SMPercent(%d,%d,%d) = %d, want %d", tc.mem, tc.total, tc.req, got, tc.want)
		}
	}
}

func TestReconcileCreatesSetsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	mps := newFakeMPS()
	mps.ns["fcstale"] = [2]string{"4096M", "10.00"}    // owned, request gone
	mps.ns["userns"] = [2]string{"unlimited", "50.00"} // not ours: never touched
	r := New(Config{RequestDir: dir}, mps, fixedMem(40960), nil)

	a := mpsns.Request{Namespace: mpsns.NamespaceName("aaaa"), ContainerID: "aaaa", MemoryMiB: 10240}
	b := mpsns.Request{Namespace: mpsns.NamespaceName("bbbb"), ContainerID: "bbbb", MemoryMiB: 30720, SMPercent: 50}
	for _, q := range []mpsns.Request{a, b} {
		if err := mpsns.Write(dir, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := mps.ns[a.Namespace]; got != [2]string{"10240M", "25.00"} {
		t.Fatalf("namespace a = %v", got)
	}
	if got := mps.ns[b.Namespace]; got != [2]string{"30720M", "50.00"} {
		t.Fatalf("namespace b = %v (requested 50%% lowers 75%%)", got)
	}
	if _, ok := mps.ns["fcstale"]; ok {
		t.Fatal("orphaned owned namespace not deleted")
	}
	if _, ok := mps.ns["userns"]; !ok {
		t.Fatal("deleted a namespace it does not own")
	}

	// Steady state: only the list call.
	mps.calls = nil
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(mps.calls) != 1 {
		t.Fatalf("steady state issued %v", mps.calls)
	}

	// Request removed -> namespace removed.
	if err := mpsns.Remove(dir, a.Namespace); err != nil {
		t.Fatal(err)
	}
	_ = r.Reconcile(context.Background())
	if _, ok := mps.ns[a.Namespace]; ok {
		t.Fatal("namespace kept after its request was removed")
	}
}

// A daemon restart drops every namespace; the next pass recreates them.
func TestReconcileHealsDaemonRestart(t *testing.T) {
	dir := t.TempDir()
	mps := newFakeMPS()
	r := New(Config{RequestDir: dir}, mps, fixedMem(40960), nil)
	q := mpsns.Request{Namespace: mpsns.NamespaceName("cccc"), ContainerID: "cccc", MemoryMiB: 4096}
	_ = mpsns.Write(dir, q)
	_ = r.Reconcile(context.Background())

	mps.down = true
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected an error while the daemon is down")
	}
	mps.down = false
	mps.ns = map[string][2]string{"default": {"unlimited", "100.00"}}
	_ = r.Reconcile(context.Background())
	if got := mps.ns[q.Namespace]; got != [2]string{"4096M", "10.00"} {
		t.Fatalf("after restart: %v", got)
	}
}

func TestParseMiB(t *testing.T) {
	for in, want := range map[string]int64{"13516M": 13516, "10G": 10240, "1024K": 1, "unlimited": 0, "": 0} {
		if got := parseMiB(in); got != want {
			t.Errorf("parseMiB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFractionPercent(t *testing.T) {
	for _, tc := range []struct {
		f    float64
		req  int
		want int
	}{{0.25, 0, 25}, {0.1, 0, 10}, {0.33, 0, 33}, {0.777, 0, 78}, {1, 0, 100}, {0.001, 0, 1}, {0.5, 20, 20}, {0.5, 90, 50}} {
		if got := FractionPercent(tc.f, tc.req); got != tc.want {
			t.Errorf("FractionPercent(%v,%d) = %d, want %d", tc.f, tc.req, got, tc.want)
		}
	}
}

// A request made by fraction must not depend on the (cgroup-limited) NVML total.
func TestReconcileUsesFraction(t *testing.T) {
	dir := t.TempDir()
	mps := newFakeMPS()
	r := New(Config{RequestDir: dir}, mps, fixedMem(4096), nil) // a wrong, shrunken total
	q := mpsns.Request{Namespace: mpsns.NamespaceName("dddd"), ContainerID: "dddd", MemoryMiB: 10240, Fraction: 0.25}
	_ = mpsns.Write(dir, q)
	_ = r.Reconcile(context.Background())
	if got := mps.ns[q.Namespace]; got != [2]string{"10240M", "25.00"} {
		t.Fatalf("namespace = %v", got)
	}
}
