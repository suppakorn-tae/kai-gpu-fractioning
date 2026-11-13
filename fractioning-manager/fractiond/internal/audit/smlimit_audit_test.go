// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"testing"

	"github.com/containerd/nri/pkg/api"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/configuration"
	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mpsns"
	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/fractiond/internal/annotations"
)

func TestDetectorSMLimitExpectsScopedMount(t *testing.T) {
	d := detector{
		annotationPrefix: configuration.DefaultAnnotationPrefix,
		mpsPipeDirectory: "/run/nvidia-mps",
		smSharingEnabled: true,
		computeDefault:   annotations.ComputeModeSMLimit,
		computeEnforce:   true,
	}
	pod := &api.PodSandbox{Id: "sb", Name: "inst", Annotations: map[string]string{
		"nvidia.com/container.main.gpu-memory.limit": "4Gi",
		"nvidia.com/container.main.gpu-compute.mode": "time-slicing", // ignored: enforced
	}}
	env := []string{"CUDA_MPS_PIPE_DIRECTORY=/tmp/nvidia-mps", "NVIDIA_GPU_MEMORY_LIMIT=4096", "NVIDIA_GPU_MEMORY_REQUEST=4096"}
	scoped := &api.Container{Id: "c1abc", PodSandboxId: "sb", Name: "main", State: api.ContainerState_CONTAINER_RUNNING, Env: env,
		Mounts: []*api.Mount{{Source: mpsns.PipeDir("/run/nvidia-mps", mpsns.NamespaceName("c1abc")), Destination: configuration.ContainerMPSPipeDirectory}}}
	if _, bad := d.check(pod, scoped); bad {
		t.Fatal("correctly scoped sm-limit container reported as a violator")
	}
	whole := &api.Container{Id: "c1abc", PodSandboxId: "sb", Name: "main", State: api.ContainerState_CONTAINER_RUNNING, Env: env,
		Mounts: []*api.Mount{{Source: "/run/nvidia-mps", Destination: "/run/nvidia-mps"}}}
	if _, bad := d.check(pod, whole); !bad {
		t.Fatal("container with the whole MPS tree was not flagged")
	}
}
