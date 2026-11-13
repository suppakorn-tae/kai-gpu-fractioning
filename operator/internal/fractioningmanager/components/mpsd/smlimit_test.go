// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mpsd

import (
	"testing"

	v1alpha1 "github.com/kai-scheduler/gpu-fractioning/api/v1alpha1"
	"github.com/kai-scheduler/gpu-fractioning/operator/internal/common/daemonmgr"
)

func TestMpsdMountsRequestDirOnlyWithSMLimit(t *testing.T) {
	has := func(sm *v1alpha1.SMLimitSpec, smSharing bool) bool {
		ds := NewMpsdDaemon(nil, true, smSharing, WithSMLimit(sm)).BuildDaemonSet(daemonmgr.BuildOptions{})
		for _, m := range ds.Spec.Template.Spec.Containers[0].VolumeMounts {
			if m.MountPath == nsRequestDir {
				return true
			}
		}
		return false
	}
	if !has(&v1alpha1.SMLimitSpec{Enabled: true}, true) {
		t.Fatal("request dir not mounted with sm-limit enabled")
	}
	if has(nil, true) || has(&v1alpha1.SMLimitSpec{Enabled: true}, false) {
		t.Fatal("request dir mounted without sm-limit (or without sm-sharing support)")
	}
}
