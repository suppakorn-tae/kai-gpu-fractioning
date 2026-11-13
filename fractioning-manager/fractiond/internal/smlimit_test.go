// Copyright 2026 suppakorn-tae. Part of a fork of kai-scheduler/gpu-fractioning.
// SPDX-License-Identifier: Apache-2.0

package internal

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/configuration"
	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mpsns"
	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/fractiond/internal/annotations"
)

const smLimitCtrID = "091d5e1668d8d2aa32e19df57ceef60249ce28b3902b22e3b5585621be32d318"

func newSMLimitPlugin(t *testing.T, enforce bool, wait time.Duration) (p *Plugin, pipeDir, reqDir string) {
	t.Helper()
	pipeDir, reqDir = t.TempDir(), t.TempDir()
	p, err := NewPlugin(Config{
		AnnotationPrefix:    configuration.DefaultAnnotationPrefix,
		MPSPipeDirectory:    pipeDir,
		SupportSMSharing:    true,
		DefaultComputeMode:  annotations.ComputeModeSMLimit,
		EnforceComputeMode:  enforce,
		NamespaceRequestDir: reqDir,
		NamespaceWait:       wait,
		MapDir:              t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatalf("NewPlugin: %v", err)
	}
	// Registered after the TempDirs, so it runs before they are removed: the
	// mapping is written asynchronously.
	t.Cleanup(p.Flush)
	return p, pipeDir, reqDir
}

func smLimitPod(extra map[string]string) *api.PodSandbox {
	a := map[string]string{
		"nvidia.com/container.main.gpu-memory.limit": "10Gi",
		"nvidia.com/container.main.gpus.devices":     "k8s.device-plugin.nvidia.com/gpu=GPU-54e712a0-ac65-5c55-1fb6-4bb22e965d46",
	}
	for k, v := range extra {
		a[k] = v
	}
	return &api.PodSandbox{Name: "inst", Namespace: "tenants", Annotations: a}
}

// fakeMPSD creates the namespace's control socket as soon as the request shows
// up, as mpsd would.
func fakeMPSD(t *testing.T, pipeDir, reqDir string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			reqs, _ := mpsns.List(reqDir)
			for ns := range reqs {
				sock := mpsns.ControlSocket(pipeDir, ns)
				if _, err := os.Stat(sock); err != nil {
					_ = os.MkdirAll(filepath.Dir(sock), 0o755)
					if l, err := net.Listen("unix", sock); err == nil {
						t.Cleanup(func() { _ = l.Close() })
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

func mountSources(adj *api.ContainerAdjustment) []string {
	var out []string
	for _, m := range adj.GetMounts() {
		out = append(out, m.Source+"->"+m.Destination)
	}
	return out
}

func envValue(adj *api.ContainerAdjustment, key string) string {
	for _, e := range adj.GetEnv() {
		if e.Key == key {
			return e.Value
		}
	}
	return ""
}

func TestSMLimit_MountsOnlyOwnNamespace(t *testing.T) {
	p, pipeDir, reqDir := newSMLimitPlugin(t, true, time.Second)
	fakeMPSD(t, pipeDir, reqDir)

	adj, _, err := p.CreateContainer(context.Background(), smLimitPod(map[string]string{
		"gpu-sm-percent": "20",
	}), &api.Container{Id: smLimitCtrID, Name: "main"})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	ns := mpsns.NamespaceName(smLimitCtrID)
	want := mpsns.PipeDir(pipeDir, ns) + "->" + configuration.ContainerMPSPipeDirectory
	got := mountSources(adj)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("mounts = %v, want only %s", got, want)
	}
	if strings.Contains(got[0], pipeDir+"->") {
		t.Fatal("whole MPS pipe tree mounted")
	}
	if v := envValue(adj, "CUDA_MPS_PIPE_DIRECTORY"); v != configuration.ContainerMPSPipeDirectory {
		t.Fatalf("CUDA_MPS_PIPE_DIRECTORY = %q", v)
	}

	reqs, _ := mpsns.List(reqDir)
	r, ok := reqs[ns]
	if !ok || r.MemoryMiB != 10240 || r.SMPercent != 20 || len(r.GPUUUIDs) != 1 || r.GPUUUIDs[0] != "GPU-54e712a0-ac65-5c55-1fb6-4bb22e965d46" {
		t.Fatalf("request = %+v", r)
	}

	if err := p.RemoveContainer(context.Background(), nil, &api.Container{Id: smLimitCtrID, Name: "main"}); err != nil {
		t.Fatal(err)
	}
	if reqs, _ := mpsns.List(reqDir); len(reqs) != 0 {
		t.Fatalf("request not removed: %v", reqs)
	}
}

// Enforced policy: a workload asking for time-slicing still gets sm-limit.
func TestSMLimit_EnforceIgnoresOptOut(t *testing.T) {
	p, pipeDir, reqDir := newSMLimitPlugin(t, true, time.Second)
	fakeMPSD(t, pipeDir, reqDir)
	adj, _, err := p.CreateContainer(context.Background(), smLimitPod(map[string]string{
		"nvidia.com/container.main.gpu-compute.mode": "time-slicing",
	}), &api.Container{Id: smLimitCtrID, Name: "main"})
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if got := mountSources(adj); len(got) != 1 || !strings.Contains(got[0], "/shared/fc") {
		t.Fatalf("opt-out was honoured: mounts %v", got)
	}
}

// Without mpsd the creation fails instead of starting the container uncapped.
func TestSMLimit_FailsClosedWhenNamespaceMissing(t *testing.T) {
	p, _, _ := newSMLimitPlugin(t, true, 60*time.Millisecond)
	adj, _, err := p.CreateContainer(context.Background(), smLimitPod(nil), &api.Container{Id: smLimitCtrID, Name: "main"})
	if err == nil || adj != nil {
		t.Fatalf("expected creation to fail, got adj=%v err=%v", adj, err)
	}
}

func TestSMLimit_SynchronizePrunesStaleRequests(t *testing.T) {
	p, _, reqDir := newSMLimitPlugin(t, true, time.Second)
	for _, id := range []string{"aaaa1111", "bbbb2222"} {
		if err := mpsns.Write(reqDir, mpsns.Request{Namespace: mpsns.NamespaceName(id), ContainerID: id, MemoryMiB: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Synchronize(context.Background(), nil, []*api.Container{{Id: "aaaa1111", Name: "main"}}); err != nil {
		t.Fatal(err)
	}
	p.Flush()
	reqs, _ := mpsns.List(reqDir)
	if _, ok := reqs[mpsns.NamespaceName("aaaa1111")]; !ok || len(reqs) != 1 {
		t.Fatalf("after Synchronize: %v", reqs)
	}
}

func TestGPUUUIDs(t *testing.T) {
	got := gpuUUIDs("k8s.device-plugin.nvidia.com/gpu=GPU-a, GPU-b,void")
	if len(got) != 2 || got[0] != "GPU-a" || got[1] != "GPU-b" {
		t.Fatalf("gpuUUIDs = %v", got)
	}
}
