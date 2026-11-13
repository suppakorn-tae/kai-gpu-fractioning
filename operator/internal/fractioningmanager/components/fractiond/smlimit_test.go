// Copyright 2026 suppakorn-tae. Part of a fork of kai-scheduler/gpu-fractioning.
// SPDX-License-Identifier: Apache-2.0

package fractiond

import (
	"testing"

	v1alpha1 "github.com/kai-scheduler/gpu-fractioning/api/v1alpha1"
	"github.com/kai-scheduler/gpu-fractioning/operator/internal/common/daemonmgr"
	corev1 "k8s.io/api/core/v1"
)

func container(t *testing.T, cs []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("container %s missing", name)
	return corev1.Container{}
}

func envOf(c corev1.Container) map[string]string {
	m := map[string]string{}
	for _, e := range c.Env {
		m[e.Name] = e.Value
	}
	return m
}

func mountedAt(c corev1.Container, path string) bool {
	for _, m := range c.VolumeMounts {
		if m.MountPath == path {
			return true
		}
	}
	return false
}

func TestSMLimitAndMemoryGuardWiring(t *testing.T) {
	metrics := &v1alpha1.MetricsAgentSpec{Enabled: true}
	ds := NewFractiondDaemon(nil, metrics, true,
		WithSMLimit(&v1alpha1.SMLimitSpec{Enabled: true, Enforce: true}),
		WithMemoryGuard(&v1alpha1.MemoryGuardSpec{Enabled: true}),
	).BuildDaemonSet(daemonmgr.BuildOptions{})
	cs := ds.Spec.Template.Spec.Containers

	f := container(t, cs, daemonName)
	if e := envOf(f); e["DEFAULT_COMPUTE_MODE"] != "sm-limit" || e["ENFORCE_COMPUTE_MODE"] != "true" || e["NAMESPACE_REQUEST_DIR"] != nsRequestDir {
		t.Fatalf("fractiond env = %v", e)
	}
	if !mountedAt(f, nsRequestDir) {
		t.Fatal("fractiond lacks the namespace request dir")
	}

	m := container(t, cs, metricsdName)
	if e := envOf(m); e["MEMORY_GUARD_INTERVAL"] == "" || e["SM_LIMIT_ENFORCE_INTERVAL"] == "" {
		t.Fatalf("metricsd env = %v", e)
	}
	if !mountedAt(m, hostCgroupDir) {
		t.Fatal("metricsd lacks the host cgroup mount")
	}
}

func TestSMLimitOffByDefaultAndWithoutSMSharing(t *testing.T) {
	metrics := &v1alpha1.MetricsAgentSpec{Enabled: true}
	cases := map[string]*daemon{
		"default":       NewFractiondDaemon(nil, metrics, true).(*daemon),
		"no sm-sharing": NewFractiondDaemon(nil, metrics, false, WithSMLimit(&v1alpha1.SMLimitSpec{Enabled: true})).(*daemon),
	}
	for name, d := range cases {
		cs := d.BuildDaemonSet(daemonmgr.BuildOptions{}).Spec.Template.Spec.Containers
		f := container(t, cs, daemonName)
		if _, ok := envOf(f)["DEFAULT_COMPUTE_MODE"]; ok || mountedAt(f, nsRequestDir) {
			t.Fatalf("%s: sm-limit wired: env=%v", name, envOf(f))
		}
		m := container(t, cs, metricsdName)
		if mountedAt(m, hostCgroupDir) || envOf(m)["MEMORY_GUARD_INTERVAL"] != "" {
			t.Fatalf("%s: memory guard wired", name)
		}
	}
}
