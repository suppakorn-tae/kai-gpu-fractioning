// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"time"

	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/common/mapping/fsstore"
	"github.com/kai-scheduler/gpu-fractioning/fractioning-manager/metricsd/internal/metrics"
	"github.com/kai-scheduler/gpu-fractioning/pkg/env"
)

const (
	defaultLogLevel = "info"
	defaultAddress  = ":2112"
	defaultProcRoot = "/proc"
)

type cliFlags struct {
	// mapDir is the shared dir where fractiond writes the container->pod mapping.
	mapDir string
	// logLevel is the log level: debug, info, warn, error.
	logLevel string
	// metricsEnabled controls whether to run the exporter at all.
	metricsEnabled bool
	// address is the Prometheus listen address.
	address string
	// path is the Prometheus metrics HTTP path.
	path string
	// procRoot is the /proc root used to resolve PID->cgroup.
	procRoot string
	// interval is the NVML sampling interval (floored by the metrics package).
	interval time.Duration
	// smUtilWindow is the sliding-window averaging for SM util (0 disables).
	smUtilWindow time.Duration
	// metricNames overrides the exported Prometheus metric names; empty fields
	// fall back to the built-in gpu_fractioning_* defaults. Set these (e.g. via Helm)
	// to make metricsd emit the names a downstream consumer expects.
	metricNames metrics.MetricNames

	// memoryGuardInterval is how often the cgroup-subtree GPU memory guard
	// re-asserts each container's limit (0 disables it).
	memoryGuardInterval time.Duration
	// cgroupRoot is where the host cgroup v2 hierarchy is mounted in this container.
	cgroupRoot string
	// nvmlCgroupPrefix is the host path NVML expects cgroup paths to start with.
	nvmlCgroupPrefix string
}

// parseFlags reads the metricsd configuration from CLI flags, each with an env
// fallback and a default. There is no config file; overrides are made
// just-in-time via DaemonSet args/env.
func parseFlags() cliFlags {
	var f cliFlags
	flag.StringVar(&f.mapDir, "map-dir", env.String("MAP_DIR", fsstore.DefaultMapDir), "shared directory fractiond writes the container->pod mapping to")
	flag.StringVar(&f.logLevel, "log-level", env.String("LOG_LEVEL", defaultLogLevel), "log level: debug, info, warn, or error")
	flag.BoolVar(&f.metricsEnabled, "metrics-enabled", env.Bool("METRICS_ENABLED", true), "run the Prometheus metrics exporter")
	flag.StringVar(&f.address, "metrics-address", env.String("METRICS_ADDRESS", defaultAddress), "Prometheus exporter listen address")
	flag.StringVar(&f.path, "metrics-path", env.String("METRICS_PATH", metrics.DefaultPath), "Prometheus metrics HTTP path")
	flag.StringVar(&f.procRoot, "proc-root", env.String("PROC_ROOT", defaultProcRoot), "/proc root used to resolve GPU process PIDs to cgroups")
	flag.DurationVar(&f.interval, "metrics-interval", env.Duration("METRICS_INTERVAL", metrics.DefaultCollectInterval), "NVML sampling interval (floored at 2s)")
	flag.DurationVar(&f.smUtilWindow, "sm-util-window", env.Duration("SM_UTIL_WINDOW", 0), "sliding-window averaging for SM utilization; 0 disables")
	flag.StringVar(&f.metricNames.GPUMemoryUsedBytes, "metric-name-gpu-memory-used-bytes", env.String("METRIC_NAME_GPU_MEMORY_USED_BYTES", ""), "override the GPU memory-used-bytes metric name (default gpu_fractioning_gpu_memory_used_bytes)")
	flag.StringVar(&f.metricNames.GPUSMUtilizationPercent, "metric-name-gpu-sm-utilization-percent", env.String("METRIC_NAME_GPU_SM_UTILIZATION_PERCENT", ""), "override the GPU SM-utilization metric name (default gpu_fractioning_gpu_sm_utilization_percent)")
	flag.StringVar(&f.metricNames.GPUSMUtilizationPercentNormalized, "metric-name-gpu-sm-utilization-percent-normalized", env.String("METRIC_NAME_GPU_SM_UTILIZATION_PERCENT_NORMALIZED", ""), "override the normalized GPU SM-utilization metric name (default gpu_fractioning_gpu_sm_utilization_percent_normalized)")
	flag.DurationVar(&f.memoryGuardInterval, "memory-guard-interval", env.Duration("MEMORY_GUARD_INTERVAL", 0), "re-assert each GPU-fractioning container's NVML memory limit on its whole cgroup subtree this often; 0 disables")
	flag.StringVar(&f.cgroupRoot, "cgroup-root", env.String("CGROUP_ROOT", "/sys/fs/cgroup"), "where the host cgroup v2 hierarchy is mounted in this container")
	flag.StringVar(&f.nvmlCgroupPrefix, "nvml-cgroup-prefix", env.String("NVML_CGROUP_PREFIX", "/sys/fs/cgroup"), "host path of the cgroup v2 mount, as NVML expects it in cgroup paths")
	flag.Parse()
	return f
}
