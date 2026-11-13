// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package mpsns is the contract between fractiond and mpsd for per-container
// MPS namespaces (the sm-limit compute mode).
//
// fractiond writes one request file per sm-limit container into a host
// directory that only the two daemons mount; mpsd turns each request into an
// MPS namespace on the shared server, with an active-thread percentage derived
// from the container's GPU memory share, and deletes the namespace when the
// request disappears. fractiond then bind-mounts only that namespace's pipe
// directory into the container, so the workload can reach its own namespace
// socket and nothing else of the node's MPS tree.
package mpsns

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultRequestDir is the host directory holding the request files.
	// It must not be mounted into workloads.
	DefaultRequestDir = "/run/gpu-fractioning/mps-ns"

	// SharedServer is the MPS server mpsd renders ([servers.shared]); the
	// namespaces are created on it.
	SharedServer = "shared"

	// NamePrefix marks namespaces owned by this mechanism, so mpsd only ever
	// deletes its own.
	NamePrefix = "fc"

	nameIDLen = 28 // MPS rejects long names; fc + 28 hex = 30 chars
	fileExt   = ".json"
)

// Request asks mpsd for a namespace.
type Request struct {
	Namespace   string `json:"namespace"`
	ContainerID string `json:"containerID"`
	Container   string `json:"container,omitempty"`
	Pod         string `json:"pod,omitempty"`
	PodNS       string `json:"podNamespace,omitempty"`
	// MemoryMiB is the container's GPU memory allotment; mpsd derives the SM
	// share from it (MemoryMiB / device total) and also sets it as the
	// namespace's pinned-memory limit.
	MemoryMiB int64 `json:"memoryMiB"`
	// SMPercent optionally lowers the derived SM share (1-100, 0 = derive).
	// It can never raise it above the memory share.
	SMPercent int `json:"smPercent,omitempty"`
	// GPUUUIDs are the GPUs the scheduler assigned, when known.
	GPUUUIDs []string `json:"gpuUUIDs,omitempty"`
}

// NamespaceName returns the MPS namespace name for a container ID: "fc" plus
// the first 28 characters of the (hex) ID, lowercased and stripped of anything
// MPS would reject.
func NamespaceName(containerID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(containerID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() == nameIDLen {
				break
			}
		}
	}
	return NamePrefix + b.String()
}

// IsOwned reports whether an MPS namespace name was produced by NamespaceName.
func IsOwned(name string) bool {
	return strings.HasPrefix(name, NamePrefix) && len(name) > len(NamePrefix)
}

// PipeDir is the host directory of a namespace's control socket.
func PipeDir(mpsPipeDir, namespace string) string {
	return filepath.Join(mpsPipeDir, SharedServer, namespace)
}

// ControlSocket is the namespace's control socket, which exists once mpsd has
// created the namespace.
func ControlSocket(mpsPipeDir, namespace string) string {
	return filepath.Join(PipeDir(mpsPipeDir, namespace), "control")
}

// Write atomically writes the request into dir.
func Write(dir string, r Request) error {
	if r.Namespace == "" {
		return errors.New("request without namespace")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, r.Namespace+fileExt))
}

// Remove deletes the request for a namespace; a missing file is not an error.
func Remove(dir, namespace string) error {
	err := os.Remove(filepath.Join(dir, namespace+fileExt))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List reads every request in dir, keyed by namespace. Unreadable or malformed
// files are reported in errs and skipped.
func List(dir string) (reqs map[string]Request, errs []error) {
	reqs = map[string]Request{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return reqs, nil
		}
		return reqs, []error{err}
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, fileExt) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var r Request
		if err := json.Unmarshal(data, &r); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if r.Namespace != strings.TrimSuffix(name, fileExt) || !IsOwned(r.Namespace) {
			errs = append(errs, fmt.Errorf("%s: namespace %q does not match file", name, r.Namespace))
			continue
		}
		reqs[r.Namespace] = r
	}
	return reqs, errs
}
