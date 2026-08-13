// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjmudd/mysql-rpm-builder/go/config"
)

// newTestRunner builds a Runner for ApplyPatches tests, with HOME (and hence
// rpmbuildHome) pointed at a throwaway directory.
func newTestRunner(t *testing.T, dataDir, label string, patches []string) *Runner {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return &Runner{
		Cfg: config.Resolved{
			Label: label,
			Build: config.Build{Patches: patches},
		},
		DataDir: dataDir,
	}
}

func TestApplyPatchesNoPatchesDeclaredNoCustomConfig(t *testing.T) {
	r := newTestRunner(t, t.TempDir(), "9.9.9", nil)
	if err := r.ApplyPatches(); err != nil {
		t.Fatalf("ApplyPatches() = %v, want nil", err)
	}
}

func TestApplyPatchesDeclaredAndPresentSucceeds(t *testing.T) {
	dataDir := t.TempDir()
	base := filepath.Join(dataDir, "config", "9.9.9", "SOURCES")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "000.foo.diff"), []byte("diff content"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newTestRunner(t, dataDir, "9.9.9", []string{"SOURCES/000.foo.diff"})
	if err := r.ApplyPatches(); err != nil {
		t.Fatalf("ApplyPatches() = %v, want nil", err)
	}
}

func TestApplyPatchesDeclaredButMissingErrors(t *testing.T) {
	dataDir := t.TempDir()
	base := filepath.Join(dataDir, "config", "9.9.9", "SOURCES")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}

	r := newTestRunner(t, dataDir, "9.9.9", []string{"SOURCES/000.foo.diff"})
	err := r.ApplyPatches()
	if err == nil {
		t.Fatal("ApplyPatches() = nil, want error for missing declared patch")
	}
	if !strings.Contains(err.Error(), "000.foo.diff") {
		t.Errorf("error %q does not mention the missing patch file", err)
	}
}

func TestApplyPatchesDeclaredButNoCustomConfigDirErrors(t *testing.T) {
	r := newTestRunner(t, t.TempDir(), "9.9.9", []string{"SOURCES/000.foo.diff"})
	err := r.ApplyPatches()
	if err == nil {
		t.Fatal("ApplyPatches() = nil, want error when config/<label>/ is missing but patches are declared")
	}
}
