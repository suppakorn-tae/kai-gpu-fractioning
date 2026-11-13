// Copyright 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mpsns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamespaceName(t *testing.T) {
	id := "091d5e1668d8d2aa32e19df57ceef60249ce28b3902b22e3b5585621be32d318"
	got := NamespaceName(id)
	if len(got) != 30 || got != NamespaceName(id) || got == NamespaceName(id+"x") {
		t.Fatalf("NamespaceName = %q: want 30 chars, deterministic, distinct", got)
	}
	if strings.Contains(got, id[:8]) {
		t.Fatalf("NamespaceName %q leaks the container ID", got)
	}
	if !IsOwned(got) || IsOwned("default") || IsOwned("fc") {
		t.Fatal("IsOwned misclassifies")
	}
}

func TestWriteListRemove(t *testing.T) {
	dir := t.TempDir()
	r := Request{Namespace: NamespaceName("abc123"), ContainerID: "abc123", MemoryMiB: 4096, SMPercent: 5}
	if err := Write(dir, r); err != nil {
		t.Fatal(err)
	}
	// noise that List must skip or reject
	_ = os.WriteFile(filepath.Join(dir, ".tmp-x"), []byte("{"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "fcother.json"), []byte(`{"namespace":"fcmismatch"}`), 0o600)

	reqs, errs := List(dir)
	if len(reqs) != 1 || reqs[r.Namespace].MemoryMiB != 4096 || reqs[r.Namespace].SMPercent != 5 {
		t.Fatalf("List = %+v", reqs)
	}
	if len(errs) != 1 {
		t.Fatalf("expected the mismatched file to be reported, errs=%v", errs)
	}
	if err := Remove(dir, r.Namespace); err != nil {
		t.Fatal(err)
	}
	if err := Remove(dir, r.Namespace); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if reqs, _ := List(dir); len(reqs) != 0 {
		t.Fatalf("after Remove: %+v", reqs)
	}
}

func TestListMissingDir(t *testing.T) {
	reqs, errs := List(filepath.Join(t.TempDir(), "absent"))
	if len(reqs) != 0 || len(errs) != 0 {
		t.Fatalf("List(absent) = %v, %v", reqs, errs)
	}
}
