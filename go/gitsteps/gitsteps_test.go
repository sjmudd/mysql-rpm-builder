// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package gitsteps

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sjmudd/mysql-rpm-builder/go/osrelease"
)

// These are deliberately limited to the pure logic (path/label derivation,
// file parsing): Clone/Configure/AssembleSRPM/Run all shell out to
// git/cmake/rpmbuild or need a real container, the same reason go/steps and
// go/osprep have no unit tests for their command-running paths either (see
// CLAUDE.md's Testing section -- that behavior is exercised by an actual
// ./build-rpm-from-git run instead).

func testRunner(tag string) *Runner {
	return &Runner{
		OS:        osrelease.Info{ID: "ol", Major: 10},
		Tag:       tag,
		DataDir:   "/data",
		OutputDir: DefaultOutputDir,
	}
}

func TestVersionStripsMysqlPrefix(t *testing.T) {
	if got, want := testRunner("mysql-9.7.1").version(), "9.7.1"; got != want {
		t.Errorf("version() = %q, want %q", got, want)
	}
	// A bare tag/branch with no "mysql-" prefix is passed through unchanged.
	if got, want := testRunner("9.7.1").version(), "9.7.1"; got != want {
		t.Errorf("version() = %q, want %q", got, want)
	}
}

func TestOSAndRPMDefines(t *testing.T) {
	r := testRunner("mysql-9.7.1")
	if got, want := r.osLabel(), "ol10"; got != want {
		t.Errorf("osLabel() = %q, want %q", got, want)
	}
	if got, want := r.elDefine(), "el10"; got != want {
		t.Errorf("elDefine() = %q, want %q", got, want)
	}
	if got, want := r.rpmDefine(), "el10 1"; got != want {
		t.Errorf("rpmDefine() = %q, want %q", got, want)
	}
}

func TestOutputSubdir(t *testing.T) {
	r := testRunner("mysql-9.7.1")
	got := r.outputSubdir()
	want := filepath.Join("/data", DefaultOutputDir, "ol10__mysql-9.7.1")
	if got != want {
		t.Errorf("outputSubdir() = %q, want %q", got, want)
	}
}

func TestFindBoostDir(t *testing.T) {
	src := t.TempDir()
	boost := filepath.Join(src, "extra", "boost", "boost_1_87_0")
	if err := os.MkdirAll(boost, 0o755); err != nil {
		t.Fatalf("setting up fixture: %v", err)
	}

	got, err := findBoostDir(src)
	if err != nil {
		t.Fatalf("findBoostDir: %v", err)
	}
	if got != boost {
		t.Errorf("findBoostDir() = %q, want %q", got, boost)
	}
}

func TestFindBoostDirMissing(t *testing.T) {
	// 8.0.x/8.4.x pull boost as a separate Source instead of bundling it
	// under extra/boost/ -- this should error clearly, not panic or silently
	// pick something wrong.
	src := t.TempDir()
	if _, err := findBoostDir(src); err == nil {
		t.Error("findBoostDir on a tree with no extra/boost/boost_* = nil error, want one")
	}
}

func TestLoadDepsPackages(t *testing.T) {
	dir := t.TempDir()
	content := "oses:\n  ol10:\n    packages:\n      - git\n      - cmake\n      - bison\n"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	got, err := loadDepsPackages(dir, "ol10", "mysql-9.7.1")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want := []string{"git", "cmake", "bison"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("loadDepsPackages()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadDepsPackagesPerTagOverride(t *testing.T) {
	dir := t.TempDir()
	content := "oses:\n" +
		"  ol9:\n" +
		"    packages:\n" +
		"      - git\n" +
		"      - cmake\n" +
		"    builds:\n" +
		"      mysql-9.7.1:\n" +
		"        packages:\n" +
		"          - gcc-toolset-14-gcc\n" +
		"      mysql-8.0.45:\n" +
		"        packages: []\n"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	// A tag with an override gets the OS base plus that tag's packages appended.
	got, err := loadDepsPackages(dir, "ol9", "mysql-9.7.1")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want := []string{"git", "cmake", "gcc-toolset-14-gcc"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages(mysql-9.7.1) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("loadDepsPackages(mysql-9.7.1)[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// A tag with an empty override list still just gets the OS base, no error.
	got, err = loadDepsPackages(dir, "ol9", "mysql-8.0.45")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want = []string{"git", "cmake"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages(mysql-8.0.45) = %v, want %v", got, want)
	}

	// A tag with no builds: entry at all also just gets the OS base.
	got, err = loadDepsPackages(dir, "ol9", "mysql-8.4.10")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want = []string{"git", "cmake"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages(mysql-8.4.10) = %v, want %v", got, want)
	}
}

func TestLoadDepsPackagesMissingOS(t *testing.T) {
	dir := t.TempDir()
	content := "oses:\n  ol10:\n    packages:\n      - git\n"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := loadDepsPackages(dir, "rocky9", "mysql-9.7.1"); err == nil {
		t.Error("loadDepsPackages for an OS with no entry = nil error, want one")
	}
}

func TestLoadDepsPackagesUnknownField(t *testing.T) {
	// KnownFields(true), same as go/config -- a typo'd/unexpected key should
	// fail loudly rather than be silently ignored.
	dir := t.TempDir()
	content := "oses:\n  ol10:\n    package: [git]\n" // typo: "package" not "packages"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := loadDepsPackages(dir, "ol10", "mysql-9.7.1"); err == nil {
		t.Error("loadDepsPackages with an unknown field = nil error, want one")
	}
}
